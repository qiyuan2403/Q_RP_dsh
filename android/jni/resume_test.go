package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/agent"
	"github.com/qiyuan2403/Q_RP_dsh/llm"
	"github.com/qiyuan2403/Q_RP_dsh/session"
	"github.com/qiyuan2403/Q_RP_dsh/systemprompt"
	"github.com/qiyuan2403/Q_RP_dsh/tools"
	toolweb "github.com/qiyuan2403/Q_RP_dsh/tools/web"
)

// recordingProvider captures every request so a test can assert what the model
// would actually see, without a network call.
type recordingProvider struct {
	requests []llm.Request
}

func (p *recordingProvider) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	p.requests = append(p.requests, req)
	return llm.Response{Text: "done"}, nil
}

// appendTestEvent records one event the way the agent loop would.
func appendTestEvent(t *testing.T, log *session.Log, eventType string, data any) {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal %s: %v", eventType, err)
	}
	if _, err := log.Append(session.Event{Type: eventType, Data: raw}); err != nil {
		t.Fatalf("append %s: %v", eventType, err)
	}
}

// newTestLog opens a session in a temporary home.
func newTestLog(t *testing.T, id string) *session.Log {
	t.Helper()
	log, err := session.Create(filepath.Join(t.TempDir(), "sessions", id), session.Header{
		ID:        id,
		CreatedAt: time.Now().UnixMilli(),
		CWD:       t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// TestResumedSessionSeedsTheModelHistory pins the P0 behavior: a turn on an
// existing session must show the model the recorded conversation, not only the
// new task. It covers the whole chain — log events, Render lookup, Seed — and
// checks the resulting provider request.
func TestResumedSessionSeedsTheModelHistory(t *testing.T) {
	log := newTestLog(t, "s1")
	appendTestEvent(t, log, "user/message", map[string]any{"text": "first question"})
	appendTestEvent(t, log, "assistant/message", map[string]any{
		"text": "checking",
		"toolCalls": []map[string]any{{
			"id":        "call-1",
			"name":      "echo",
			"arguments": `{"value":"x"}`,
		}},
	})
	appendTestEvent(t, log, "tool/result", map[string]any{
		"callId": "call-1",
		"name":   "echo",
		"output": map[string]any{"value": "x"},
	})

	registry := tools.NewRegistry()
	if err := registry.Register(tools.Tool{
		Name: "echo",
		Run: func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"value": "x"}, nil
		},
		Render: func(any) string { return "rendered: x" },
	}); err != nil {
		t.Fatalf("register echo: %v", err)
	}

	history, err := sessionHistory(log, registry)
	if err != nil {
		t.Fatalf("sessionHistory: %v", err)
	}
	want := []llm.Message{
		{Role: llm.RoleUser, Text: "first question"},
		{
			Role:      llm.RoleAssistant,
			Text:      "checking",
			ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "echo", Arguments: `{"value":"x"}`}},
		},
		{Role: llm.RoleTool, ToolCallID: "call-1", Text: "rendered: x"},
	}
	if !reflect.DeepEqual(history, want) {
		t.Fatalf("rebuilt history = %+v, want %+v", history, want)
	}

	provider := &recordingProvider{}
	a := agent.New(agent.Config{
		Provider: provider,
		Registry: registry,
		Model:    "test",
		MaxSteps: 2,
	}, log)
	a.Seed(history)
	if _, err := a.RunTurn(context.Background(), "second question"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	if len(provider.requests) != 1 {
		t.Fatalf("model was called %d times, want 1", len(provider.requests))
	}
	want = append(want, llm.Message{Role: llm.RoleUser, Text: "second question"})
	if got := provider.requests[0].Messages; !reflect.DeepEqual(got, want) {
		t.Fatalf("model saw %+v, want %+v", got, want)
	}
}

// TestRunTurnJSONResumesSessionHistory covers the wiring above sessionHistory:
// a turn addressed to an open session must send the logged conversation to the
// model, and the registered web tools must appear in the same request. The
// endpoint is a local stub, so no device and no real model are involved.
func TestRunTurnJSONResumesSessionHistory(t *testing.T) {
	home := t.TempDir()
	id := "resume-1"
	log, err := session.Create(filepath.Join(home, "sessions", id), session.Header{
		ID:        id,
		CreatedAt: time.Now().UnixMilli(),
		CWD:       home,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	appendTestEvent(t, log, "user/message", map[string]any{"text": "first question"})
	appendTestEvent(t, log, "assistant/message", map[string]any{"text": "first answer"})

	// Register the open log the way openSession does, and remove it again so the
	// process-wide map does not leak into other tests.
	sessionsMu.Lock()
	sessions[id] = log
	sessionsMu.Unlock()
	t.Cleanup(func() {
		sessionsMu.Lock()
		delete(sessions, id)
		sessionsMu.Unlock()
		_ = log.Close()
	})

	var captured struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode model request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"second answer"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	raw := runTurnJSON(runRequest{
		Task:      "second question",
		Home:      home,
		WorkDir:   t.TempDir(),
		BaseURL:   srv.URL,
		Model:     "test",
		SessionID: id,
	})
	var resp runResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, raw)
	}
	if !resp.OK {
		t.Fatalf("turn failed: %s", raw)
	}

	// Message zero is the system prompt; the rest must be the recorded
	// conversation followed by the new task.
	var got []string
	for _, m := range captured.Messages[1:] {
		got = append(got, m.Role+"|"+m.Content)
	}
	want := []string{
		"user|first question",
		"assistant|first answer",
		"user|second question",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("model saw messages %q, want %q", got, want)
	}

	names := map[string]bool{}
	for _, tool := range captured.Tools {
		names[tool.Function.Name] = true
	}
	for _, want := range []string{toolweb.SearchToolName, toolweb.FetchToolName} {
		if !names[want] {
			t.Errorf("model was not offered %q; offered %v", want, names)
		}
	}
}

// TestSessionHistoryFailsLoudly pins the failure semantics: a log the rebuild
// cannot decode must produce a contextual error, because continuing from
// partial history would silently drop the conversation the log records.
func TestSessionHistoryFailsLoudly(t *testing.T) {
	log := newTestLog(t, "s1")
	// Append validates that data is JSON, not that it matches the event's
	// shape; an incompatible writer can leave exactly this behind.
	if _, err := log.Append(session.Event{
		Type: "user/message",
		Data: json.RawMessage(`"not an object"`),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	history, err := sessionHistory(log, tools.NewRegistry())
	if err == nil {
		t.Fatal("a corrupt log was rebuilt without an error")
	}
	if history != nil {
		t.Fatalf("history = %+v, want nil on failure", history)
	}
	for _, want := range []string{"jni:", "user/message"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to contain %q", err, want)
		}
	}
}

// TestBuildToolsetRegistersWebTools pins both halves of the wiring: the web
// tools are present, and the built-ins are still registered only once — a
// duplicate registration fails buildToolset, so success here proves it.
func TestBuildToolsetRegistersWebTools(t *testing.T) {
	registry, err := buildToolset(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("buildToolset: %v", err)
	}
	for _, name := range []string{"read", "bash", "plugin_manager", toolweb.SearchToolName, toolweb.FetchToolName} {
		if _, ok := registry.Lookup(name); !ok {
			t.Errorf("tool %q is not registered", name)
		}
	}

	assembly, err := buildPrompt(registry)
	if err != nil {
		t.Fatalf("buildPrompt: %v", err)
	}
	text, err := systemprompt.Render(assembly)
	if err != nil {
		t.Fatalf("render prompt: %v", err)
	}
	for _, want := range []string{toolweb.SearchToolName, toolweb.FetchToolName} {
		if !strings.Contains(text, want) {
			t.Errorf("system prompt does not mention %q:\n%s", want, text)
		}
	}
}

// TestBuildPromptOmitsWebSectionsWithoutTheTools keeps the conditional
// attachment honest: a registry without web tools must not describe them.
func TestBuildPromptOmitsWebSectionsWithoutTheTools(t *testing.T) {
	registry := tools.NewRegistry()
	if err := registry.Register(tools.Tool{
		Name: "read",
		Run:  func(context.Context, json.RawMessage) (any, error) { return "ok", nil },
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	assembly, err := buildPrompt(registry)
	if err != nil {
		t.Fatalf("buildPrompt: %v", err)
	}
	text, err := systemprompt.Render(assembly)
	if err != nil {
		t.Fatalf("render prompt: %v", err)
	}
	if strings.Contains(text, toolweb.FetchToolName) {
		t.Fatalf("prompt describes web_fetch without the tool registered:\n%s", text)
	}
}
