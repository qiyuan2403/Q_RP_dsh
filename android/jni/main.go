// Package main builds the harness as a shared library for the Android app.
//
// The JNI entry points are exported from Go: the app calls
// `com.sta1n.qrpdsh.Native.version()` and `runTurn(...)`, which the JVM resolves
// to the symbols in jni.go. Names follow the JNI convention exactly —
// `Java_<package with underscores>_<class>_<method>` — because the JVM looks
// them up by name rather than through a registration call.
//
// Everything that is not the JNI wrapper lives here, in a file with no cgo, so
// the turn logic compiles and its tests run on the build host. jni.go is gated
// on GOOS=android, which is an implicit build constraint, so a host build never
// needs jni.h.
package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/agent"
	"github.com/qiyuan2403/Q_RP_dsh/llm/openaichat"
	"github.com/qiyuan2403/Q_RP_dsh/session"
	"github.com/qiyuan2403/Q_RP_dsh/systemprompt"
	"github.com/qiyuan2403/Q_RP_dsh/tools"
	builtintools "github.com/qiyuan2403/Q_RP_dsh/tools/builtin"
	"github.com/qiyuan2403/Y_Cordis/jsbridge"
	"github.com/qiyuan2403/Y_Cordis/loader"
)

// Version is the harness version reported to the app.
const Version = "0.0.1"

// runRequest is the JSON the app sends for one turn.
type runRequest struct {
	Task      string `json:"task"`
	WorkDir   string `json:"workDir"`
	BaseURL   string `json:"baseUrl"`
	APIKey    string `json:"apiKey"`
	Model     string `json:"model"`
	Home      string `json:"home"`
	SessionID string `json:"sessionId,omitempty"`
	// NodePath is the embedded node executable extracted by the app
	// (NodeRuntime). Empty means the JS layer is unavailable: !!js
	// expressions fail loudly and npm plugins stay dormant.
	NodePath string `json:"nodePath,omitempty"`
}

// runResponse is the JSON returned to the app.
type runResponse struct {
	OK     bool   `json:"ok"`
	Text   string `json:"text,omitempty"`
	Reason string `json:"reason,omitempty"`
	Steps  int    `json:"steps,omitempty"`
	Error  string `json:"error,omitempty"`
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

	// The home directory is the app's own writable path. It has no default:
	// guessing one is how a turn fails at its first write, and on Android
	// there is no path an app may assume (/tmp belongs to the shell user).
	home := strings.TrimSpace(req.Home)
	if home == "" {
		return encode(runResponse{Error: "home is required"})
	}
	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" {
		workDir = home
	}

	// The embedded node powers the JS layer (!!js evaluation and npm
	// plugins). Its dynamic libraries live beside the binary, so the child
	// env must carry them via LD_LIBRARY_PATH — jsbridge passes Config.Env
	// through to the spawn.
	//
	// The pool lives for this turn and closes with it: a pool holds node
	// child processes, and the app may run turns hours apart, so keeping
	// workers warm between turns pins RSS for nothing.
	//
	// A failure here is degraded, not broken: the turn proceeds without a
	// JS layer and every !!js expression is refused loudly.
	if req.NodePath != "" {
		binDir := filepath.Dir(req.NodePath)
		libDir := filepath.Join(binDir, "..", "lib")
		conf := filepath.Join(binDir, "..", "etc", "tls", "openssl.cnf")
		pool, poolErr := jsbridge.NewPool(jsbridge.Config{
			Node:        req.NodePath,
			ExecRoot:    home,
			RequireRoot: filepath.Join(home, "plugins"),
			Env: map[string]string{
				"LD_LIBRARY_PATH": libDir,
				// Termux's node build reads an OpenSSL config at startup;
				// without this it aborts before main().
				"OPENSSL_CONF": conf,
			},
		})
		if poolErr != nil {
			// No evaluator this turn: the mount's refusal is the contract.
			_ = poolErr
		} else {
			defer pool.Close()
			// Probe the runtime once. NewPool spawns workers lazily, so a
			// broken install (missing libs) surfaces only at first use; one
			// trivial evaluation settles it now, while the failure can still
			// be reported as a turn-level fact.
			probeCtx, cancelProbe := context.WithTimeout(context.Background(), 15*time.Second)
			_, probeErr := (&jsbridge.Evaluator{Pool: pool}).Eval(probeCtx, "1 + 1", &loader.EvalScope{
				Platform: "android",
				Home:     home,
			})
			cancelProbe()
			if probeErr != nil {
				return encode(runResponse{Error: "embedded node probe failed: " + probeErr.Error()})
			}
		}
	}
	registry := tools.NewRegistry()
	profileDir := filepath.Join(home, "plugins")
	if err := builtintools.Register(registry, builtintools.Options{
		Root:        workDir,
		AllowShell:  true,
		ProfileDir:  profileDir,
		HostVersion: Version,
	}); err != nil {
		return encode(runResponse{Error: err.Error()})
	}

	assembly, err := buildPrompt(registry)
	if err != nil {
		return encode(runResponse{Error: err.Error()})
	}
	systemText, err := systemprompt.Render(assembly)
	if err != nil {
		return encode(runResponse{Error: err.Error()})
	}

	// A turn runs against the session the UI opened (reusing its history so
	// the conversation continues), or its own when no session was passed —
	// which is how the JSON entry point stays testable without a UI.
	var log *session.Log
	if req.SessionID != "" {
		sessionsMu.Lock()
		log = sessions[req.SessionID]
		sessionsMu.Unlock()
		if log == nil {
			return encode(runResponse{Error: "unknown session: " + req.SessionID})
		}
	} else {
		id := session.NewID()
		log, err = session.Create(home+"/sessions/"+id, session.Header{
			ID:        id,
			CreatedAt: time.Now().UnixMilli(),
			CWD:       workDir,
		})
		if err != nil {
			return encode(runResponse{Error: err.Error()})
		}
		defer log.Close()
	}

	a := agent.New(agent.Config{
		Provider:     openaichat.New(openaichat.Config{BaseURL: req.BaseURL, APIKey: req.APIKey}),
		Registry:     registry,
		Model:        req.Model,
		SystemPrompt: systemText,
		MaxSteps:     32,
	}, log)

	// Live events: subscribe before the turn, keep the channel keyed by
	// session id for pollEvent to drain on Java threads. A sessionless turn
	// (the test entry point) publishes nothing.
	var unsub func()
	if req.SessionID != "" {
		ch, u := log.Subscribe(128)
		unsub = u
		streamsMu.Lock()
		eventStreams[req.SessionID] = ch
		streamsMu.Unlock()
		defer func() {
			unsub()
			streamsMu.Lock()
			delete(eventStreams, req.SessionID)
			streamsMu.Unlock()
		}()
	}

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
func buildPrompt(registry *tools.Registry) (*systemprompt.Assembly, error) {
	r := systemprompt.New()
	if err := r.Section(systemprompt.Section{
		Name:  "harness-identity",
		Order: systemprompt.SectionOrders["HARNESS_IDENTITY"],
		Text:  "You are an agent running on the Q_RP_dsh harness on Android.",
	}); err != nil {
		return nil, err
	}
	// Tool-owned prompt sections ride with the tool's registration, not a
	// centrally maintained block: the official harness injects its bash
	// guidance from the tool itself, so the guidance cannot drift from the
	// tool that carries the behavior.
	if err := builtintools.AttachPromptSections(r, registry); err != nil {
		return nil, err
	}
	return r.Assemble(systemprompt.AssembleContext{})
}

// main is required by c-shared even though the library is never executed.
func main() {}

// sessions holds the open session logs, keyed by session id. A turn runs
// against its session so a UI can continue a conversation across turns and
// subscribe to its events; the map is the JNI-side state those calls need.
var (
	sessionsMu sync.Mutex
	sessions   = map[string]*session.Log{}
)

// eventStreams carries the live event channels of running turns, keyed by
// session id. runTurn subscribes before the turn starts; pollEvent drains
// one event per call. The poll calls happen on JAVA threads — the JVM owns
// every JNIEnv we use, which is why events cross the boundary by polling and
// never by a Go thread calling back into the JVM.
var (
	streamsMu    sync.Mutex
	eventStreams = map[string]<-chan session.Event{}
)
