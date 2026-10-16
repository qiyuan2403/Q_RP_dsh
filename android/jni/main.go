// Package main builds the harness as a shared library for the Android app.
//
// The JNI entry points are exported from Go: the app calls
// `com.sta1n.qrpdsh.Native.version()` and `runTurn(...)`, which the JVM resolves
// to the symbols below. Names follow the JNI convention exactly —
// `Java_<package with underscores>_<class>_<method>` — because the JVM looks
// them up by name rather than through a registration call.
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unsafe"

	"github.com/qiyuan2403/Q_RP_dsh/agent"
	"github.com/qiyuan2403/Q_RP_dsh/llm/openaichat"
	"github.com/qiyuan2403/Q_RP_dsh/session"
	"github.com/qiyuan2403/Q_RP_dsh/systemprompt"
	"github.com/qiyuan2403/Q_RP_dsh/tools"
	builtintools "github.com/qiyuan2403/Q_RP_dsh/tools/builtin"
)

// Version is the harness version reported to the app.
const Version = "0.0.1"

//export Java_com_sta1n_qrpdsh_Native_version
func Java_com_sta1n_qrpdsh_Native_version(env, clazz unsafe.Pointer) *C.char {
	return C.CString(Version)
}

// runRequest is the JSON the app sends for one turn.
type runRequest struct {
	Task    string `json:"task"`
	WorkDir string `json:"workDir"`
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
	Home    string `json:"home"`
}

// runResponse is the JSON returned to the app.
type runResponse struct {
	OK     bool   `json:"ok"`
	Text   string `json:"text,omitempty"`
	Reason string `json:"reason,omitempty"`
	Steps  int    `json:"steps,omitempty"`
	Error  string `json:"error,omitempty"`
}

//export Java_com_sta1n_qrpdsh_Native_runTurn
func Java_com_sta1n_qrpdsh_Native_runTurn(env, clazz unsafe.Pointer, task, workDir, baseURL, apiKey, model *C.char) *C.char {
	req := runRequest{
		Task:    C.GoString(task),
		WorkDir: C.GoString(workDir),
		BaseURL: C.GoString(baseURL),
		APIKey:  C.GoString(apiKey),
		Model:   C.GoString(model),
	}
	return C.CString(runTurnJSON(req))
}

// runTurnJSON performs one turn and returns its result as JSON.
//
// It is separated from the JNI wrapper so the whole path is testable without a
// JVM, which is how the Android build is verified in CI.
func runTurnJSON(req runRequest) string {
	encode := func(r runResponse) string {
		raw, err := json.Marshal(r)
		if err != nil {
			return `{"ok":false,"error":"encoding the result failed"}`
		}
		return string(raw)
	}

	if strings.TrimSpace(req.Task) == "" {
		return encode(runResponse{Error: "task is empty"})
	}
	if req.BaseURL == "" || req.Model == "" {
		return encode(runResponse{Error: "baseUrl and model are required"})
	}

	home := req.Home
	if home == "" {
		home = "/tmp/qrpdsh"
	}
	workDir := req.WorkDir
	if workDir == "" {
		workDir = home
	}

	registry := tools.NewRegistry()
	if err := builtintools.Register(registry, builtintools.Options{Root: workDir, AllowShell: true}); err != nil {
		return encode(runResponse{Error: err.Error()})
	}

	assembly, err := buildPrompt()
	if err != nil {
		return encode(runResponse{Error: err.Error()})
	}
	systemText, err := systemprompt.Render(assembly)
	if err != nil {
		return encode(runResponse{Error: err.Error()})
	}

	id := session.NewID()
	log, err := session.Create(home+"/sessions/"+id, session.Header{
		ID:        id,
		CreatedAt: time.Now().UnixMilli(),
		CWD:       workDir,
	})
	if err != nil {
		return encode(runResponse{Error: err.Error()})
	}
	defer log.Close()

	a := agent.New(agent.Config{
		Provider:     openaichat.New(openaichat.Config{BaseURL: req.BaseURL, APIKey: req.APIKey}),
		Registry:     registry,
		Model:        req.Model,
		SystemPrompt: systemText,
		MaxSteps:     32,
	}, log)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	result, err := a.RunTurn(ctx, req.Task)
	if err != nil {
		return encode(runResponse{Error: err.Error()})
	}
	resp := runResponse{
		OK:     result.Reason == agent.StopCompleted,
		Text:   result.Text,
		Reason: string(result.Reason),
		Steps:  result.Steps,
	}
	if result.Err != nil {
		resp.Error = result.Err.Error()
	}
	return encode(resp)
}

// buildPrompt assembles the system prompt for the mobile deployment.
func buildPrompt() (*systemprompt.Assembly, error) {
	r := systemprompt.New()
	if err := r.Section(systemprompt.Section{
		Name:  "harness-identity",
		Order: systemprompt.SectionOrders["HARNESS_IDENTITY"],
		Text:  "You are an agent running on the Q_RP_dsh harness on Android.",
	}); err != nil {
		return nil, err
	}
	return r.Assemble(systemprompt.AssembleContext{})
}

// main is required by c-shared even though the library is never executed.
func main() {}
