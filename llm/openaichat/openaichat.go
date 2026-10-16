// Package openaichat implements the chat-completions provider.
//
// The wire format is the one DeepSeek, OpenAI, and the OpenAI-compatible
// gateways all speak, so one adapter covers the deployments this harness talks
// to. Field names are the wire vocabulary and must not change.
package openaichat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/llm"
)

// Config describes one endpoint.
type Config struct {
	// BaseURL is the API root, e.g. https://api.deepseek.com/v1. The
	// `/chat/completions` path is appended.
	BaseURL string
	// APIKey authorizes requests.
	APIKey string
	// Timeout bounds one call. Empty uses a generous default, because a long
	// completion is normal.
	Timeout time.Duration
	// HTTPClient overrides the client.
	HTTPClient *http.Client
	// ExtraHeaders are added to every request.
	ExtraHeaders map[string]string
}

// Client is a chat-completions provider.
type Client struct {
	cfg Config
}

var _ llm.Provider = (*Client)(nil)

// New builds a client.
func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Minute
	}
	return &Client{cfg: cfg}
}

// wireRequest is the chat-completions payload.
type wireRequest struct {
	Model       string        `json:"model"`
	Messages    []wireMessage `json:"messages"`
	Tools       []wireTool    `json:"tools,omitempty"`
	Stream      bool          `json:"stream"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
}

type wireMessage struct {
	Role string `json:"role"`
	// Content is a string. An assistant message that only requests tools sends
	// an empty string rather than null, which some providers reject.
	Content   string         `json:"content"`
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
	// ToolCallID keys a tool result to its call.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type wireResponse struct {
	Choices []struct {
		Message      wireMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Complete performs one model call.
//
// A provider error body is surfaced rather than reduced to a status code: the
// body names the cause (an invalid key, an unknown model, a context overflow),
// and losing it makes the failure unactionable.
func (c *Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if strings.TrimSpace(c.cfg.BaseURL) == "" {
		return llm.Response{}, errors.New("openaichat: no base URL configured")
	}
	if strings.TrimSpace(req.Model) == "" {
		return llm.Response{}, errors.New("openaichat: no model specified")
	}

	msgs := make([]wireMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, wireMessage{Role: string(llm.RoleSystem), Content: req.System})
	}
	for _, m := range req.Messages {
		wm := wireMessage{
			Role:       string(m.Role),
			Content:    m.Text,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			var wtc wireToolCall
			wtc.ID = tc.ID
			wtc.Type = "function"
			wtc.Function.Name = tc.Name
			wtc.Function.Arguments = tc.Arguments
			wm.ToolCalls = append(wm.ToolCalls, wtc)
		}
		msgs = append(msgs, wm)
	}

	payload := wireRequest{
		Model:    req.Model,
		Messages: msgs,
		Stream:   false,
	}
	for _, spec := range req.Tools {
		var wt wireTool
		wt.Type = "function"
		wt.Function.Name = spec.Name
		wt.Function.Description = spec.Description
		wt.Function.Parameters = spec.Parameters
		payload.Tools = append(payload.Tools, wt)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return llm.Response{}, err
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return llm.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	for k, v := range c.cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	client := c.cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: c.cfg.Timeout}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return llm.Response{}, fmt.Errorf("openaichat: request to %s failed: %w", url, err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return llm.Response{}, fmt.Errorf("openaichat: reading response: %w", err)
	}

	var parsed wireResponse
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		return llm.Response{}, fmt.Errorf("openaichat: response is not valid JSON: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return llm.Response{}, fmt.Errorf("openaichat: %s", parsed.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(buf.String())
		if len(detail) > 512 {
			detail = detail[:512] + "..."
		}
		return llm.Response{}, fmt.Errorf("openaichat: backend returned %d: %s", resp.StatusCode, detail)
	}
	if len(parsed.Choices) == 0 {
		return llm.Response{}, errors.New("openaichat: response contained no choices")
	}

	choice := parsed.Choices[0]
	out := llm.Response{
		Text: choice.Message.Content,
		Usage: llm.Usage{
			PromptTokens:     parsed.Usage.PromptTokens,
			CompletionTokens: parsed.Usage.CompletionTokens,
			TotalTokens:      parsed.Usage.TotalTokens,
		},
	}
	for _, tc := range choice.Message.ToolCalls {
		args := tc.Function.Arguments
		if strings.TrimSpace(args) == "" {
			// Some providers omit arguments for a no-argument call; an empty
			// object keeps the tool validator's contract.
			args = "{}"
		}
		out.ToolCalls = append(out.ToolCalls, llm.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}
	return out, nil
}
