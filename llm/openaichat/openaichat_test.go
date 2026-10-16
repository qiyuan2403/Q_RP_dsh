package openaichat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qiyuan2403/Q_RP_dsh/llm"
)

// TestCompleteSendsSystemMessagesAndTools pins the wire shape: the system
// prompt is a leading message, history follows, and tool specs are attached.
func TestCompleteSendsSystemMessagesAndTools(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "hello"}}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4},
		})
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL})
	resp, err := c.Complete(context.Background(), llm.Request{
		Model:  "m",
		System: "be brief",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Text: "hi"},
		},
		Tools: []llm.ToolSpec{{
			Name:        "t",
			Description: "a tool",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text != "hello" {
		t.Fatalf("Text = %q", resp.Text)
	}
	if resp.Usage.TotalTokens != 4 {
		t.Fatalf("Usage = %+v", resp.Usage)
	}

	msgs := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (system + user)", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be brief" {
		t.Fatalf("first message = %v", first)
	}
	if got["stream"] != false {
		t.Fatalf("stream = %v, want false", got["stream"])
	}
	tools := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", tools)
	}
}

// TestCompleteParsesToolCalls pins the response side of a tool request.
func TestCompleteParsesToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": "",
					"tool_calls": []map[string]any{{
						"id":       "c1",
						"type":     "function",
						"function": map[string]any{"name": "read", "arguments": `{"path":"/x"}`},
					}},
				},
			}},
		})
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL})
	resp, err := c.Complete(context.Background(), llm.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "read" || resp.ToolCalls[0].ID != "c1" {
		t.Fatalf("tool call = %+v", resp.ToolCalls[0])
	}
}

// TestCompleteDefaultsMissingArguments pins that an omitted argument string
// becomes an empty object, so the tool validator's contract still holds.
func TestCompleteDefaultsMissingArguments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"tool_calls": []map[string]any{{
						"id":       "c1",
						"type":     "function",
						"function": map[string]any{"name": "noargs", "arguments": ""},
					}},
				},
			}},
		})
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL})
	resp, err := c.Complete(context.Background(), llm.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.ToolCalls[0].Arguments != "{}" {
		t.Fatalf("Arguments = %q, want {}", resp.ToolCalls[0].Arguments)
	}
}

// TestCompleteSurfacesProviderErrorMessage pins that the provider's own error
// text reaches the caller, since it names the cause.
func TestCompleteSurfacesProviderErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "Invalid token", "type": "auth"},
		})
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL})
	_, err := c.Complete(context.Background(), llm.Request{Model: "m"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Invalid token") {
		t.Fatalf("error dropped the provider message: %v", err)
	}
}

// TestCompleteRejectsResponseWithoutChoices pins that an empty choices list is
// an error rather than a silent empty reply.
func TestCompleteRejectsResponseWithoutChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{}})
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL})
	if _, err := c.Complete(context.Background(), llm.Request{Model: "m"}); err == nil {
		t.Fatal("an empty choices list was accepted")
	}
}

// TestCompleteRequiresBaseURLAndModel pins both preconditions.
func TestCompleteRequiresBaseURLAndModel(t *testing.T) {
	if _, err := New(Config{}).Complete(context.Background(), llm.Request{Model: "m"}); err == nil {
		t.Fatal("a missing base URL was accepted")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	if _, err := New(Config{BaseURL: srv.URL}).Complete(context.Background(), llm.Request{}); err == nil {
		t.Fatal("a missing model was accepted")
	}
}

// TestCompleteSendsAuthorizationAndExtraHeaders pins the header assembly.
func TestCompleteSendsAuthorizationAndExtraHeaders(t *testing.T) {
	var auth, extra string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		extra = r.Header.Get("X-Custom")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "ok"}}},
		})
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", ExtraHeaders: map[string]string{"X-Custom": "v"}})
	if _, err := c.Complete(context.Background(), llm.Request{Model: "m"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if auth != "Bearer k" {
		t.Fatalf("Authorization = %q", auth)
	}
	if extra != "v" {
		t.Fatalf("X-Custom = %q", extra)
	}
}

// TestCompleteSendsToolResultsBack pins the multi-step wire shape: an assistant
// tool request and its result both go back to the model.
func TestCompleteSendsToolResultsBack(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "ok"}}},
		})
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL})
	_, err := c.Complete(context.Background(), llm.Request{
		Model: "m",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Text: "go"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "t", Arguments: "{}"}}},
			{Role: llm.RoleTool, ToolCallID: "c1", Text: "result"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	assistant := msgs[1].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Fatalf("second message role = %v", assistant["role"])
	}
	if _, ok := assistant["tool_calls"]; !ok {
		t.Fatal("the assistant message lost its tool_calls")
	}
	// An assistant message with only tool calls must send an empty string, not
	// null, which some providers reject.
	if assistant["content"] != "" {
		t.Fatalf("assistant content = %v, want an empty string", assistant["content"])
	}
	toolMsg := msgs[2].(map[string]any)
	if toolMsg["tool_call_id"] != "c1" {
		t.Fatalf("tool message = %v", toolMsg)
	}
}
