// Package openaichat implements the chat-completions provider.
//
// The wire format is the one DeepSeek, OpenAI, and the OpenAI-compatible
// gateways all speak, so one adapter covers the deployments this harness talks
// to. Field names are the wire vocabulary and must not change.
package openaichat

import (
	"bufio"
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

type wireRequest struct {
	Model       string        `json:"model"`
	Messages    []wireMessage `json:"messages"`
	Tools       []wireTool    `json:"tools,omitempty"`
	Stream      bool          `json:"stream"`
	StreamUsage *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
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

// Complete performs one non-streaming model call.
func (c *Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	resp, err := c.do(ctx, req, false)
	if err != nil {
		return llm.Response{}, err
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
	out := llm.Response{Text: choice.Message.Content, Usage: llm.Usage{
		PromptTokens: parsed.Usage.PromptTokens, CompletionTokens: parsed.Usage.CompletionTokens, TotalTokens: parsed.Usage.TotalTokens,
	}}
	for _, tc := range choice.Message.ToolCalls {
		args := tc.Function.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		out.ToolCalls = append(out.ToolCalls, llm.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: args})
	}
	return out, nil
}

type wireStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Stream performs one chat-completions request and emits ordered SSE deltas.
func (c *Client) Stream(ctx context.Context, req llm.Request, yield func(llm.StreamChunk) error) error {
	resp, err := c.do(ctx, req, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		detail := strings.TrimSpace(buf.String())
		if len(detail) > 512 {
			detail = detail[:512] + "..."
		}
		return fmt.Errorf("openaichat: backend returned %d: %s", resp.StatusCode, detail)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var data strings.Builder
	flush := func() error {
		line := strings.TrimSpace(data.String())
		data.Reset()
		if line == "" {
			return nil
		}
		if line == "[DONE]" {
			return errStreamDone
		}
		var part wireStreamChunk
		if err := json.Unmarshal([]byte(line), &part); err != nil {
			return fmt.Errorf("openaichat: invalid stream event: %w", err)
		}
		if part.Error != nil && part.Error.Message != "" {
			return fmt.Errorf("openaichat: %s", part.Error.Message)
		}
		if part.Usage != nil {
			if err := yield(llm.StreamChunk{Usage: &llm.Usage{PromptTokens: part.Usage.PromptTokens, CompletionTokens: part.Usage.CompletionTokens, TotalTokens: part.Usage.TotalTokens}}); err != nil {
				return err
			}
		}
		if len(part.Choices) == 0 {
			return nil
		}
		choice := part.Choices[0]
		if choice.Delta.Content != "" {
			if err := yield(llm.StreamChunk{TextDelta: choice.Delta.Content}); err != nil {
				return err
			}
		}
		for _, call := range choice.Delta.ToolCalls {
			delta := &llm.ToolCallDelta{Index: call.Index, ID: call.ID, Name: call.Function.Name, ArgumentsDelta: call.Function.Arguments}
			if err := yield(llm.StreamChunk{ToolCallDelta: delta}); err != nil {
				return err
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); errors.Is(err, errStreamDone) {
				return nil
			} else if err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("openaichat: reading stream: %w", err)
	}
	if err := flush(); errors.Is(err, errStreamDone) {
		return nil
	} else if err != nil {
		return err
	}
	return nil
}

var errStreamDone = errors.New("openaichat: stream done")

func (c *Client) do(ctx context.Context, req llm.Request, stream bool) (*http.Response, error) {
	if strings.TrimSpace(c.cfg.BaseURL) == "" {
		return nil, errors.New("openaichat: no base URL configured")
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, errors.New("openaichat: no model specified")
	}
	msgs := make([]wireMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, wireMessage{Role: string(llm.RoleSystem), Content: req.System})
	}
	for _, m := range req.Messages {
		wm := wireMessage{Role: string(m.Role), Content: m.Text, ToolCallID: m.ToolCallID}
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
	payload := wireRequest{Model: req.Model, Messages: msgs, Stream: stream}
	for _, spec := range req.Tools {
		var wt wireTool
		wt.Type = "function"
		wt.Function.Name = spec.Name
		wt.Function.Description = spec.Description
		wt.Function.Parameters = spec.Parameters
		payload.Tools = append(payload.Tools, wt)
	}
	if stream {
		payload.StreamUsage = &struct {
			IncludeUsage bool `json:"include_usage"`
		}{IncludeUsage: true}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
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
		return nil, fmt.Errorf("openaichat: request to %s failed: %w", url, err)
	}
	return resp, nil
}
