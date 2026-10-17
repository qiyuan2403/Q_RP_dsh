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
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/agent"
	"github.com/qiyuan2403/Q_RP_dsh/llm"
	"github.com/qiyuan2403/Q_RP_dsh/llm/openaichat"
	"github.com/qiyuan2403/Q_RP_dsh/session"
	"github.com/qiyuan2403/Q_RP_dsh/systemprompt"
	"github.com/qiyuan2403/Q_RP_dsh/tools"
	builtintools "github.com/qiyuan2403/Q_RP_dsh/tools/builtin"
	toolweb "github.com/qiyuan2403/Q_RP_dsh/tools/web"
	"github.com/qiyuan2403/Q_RP_dsh/web"
	"github.com/qiyuan2403/Q_RP_dsh/web/deepseeksearch"
	"github.com/qiyuan2403/Q_RP_dsh/web/httpfetch"
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
	registry, err := buildToolset(workDir, filepath.Join(home, "plugins"))
	if err != nil {
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

	// A resumed session must start from the conversation its log records: the
	// agent object is rebuilt on every turn, so its history lives only here and
	// only now. Skipping this makes every turn after the first look like the
	// start of a conversation to the model. A fresh session has no history to
	// restore and stays unseeded.
	if req.SessionID != "" {
		history, historyErr := sessionHistory(log, registry)
		if historyErr != nil {
			return encode(runResponse{Error: historyErr.Error()})
		}
		a.Seed(history)
	}

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

// buildToolset returns the tools one turn offers.
//
// Every tool is bounded by root, the working directory the app granted the
// turn. The web tools are registered even when no provider exists: the web
// service resolves its provider per call, so a build without one reports a
// structured "no usable web provider" tool error instead of hiding the
// capability from the model and the prompt.
//
// There is exactly one built-in registration here. A second one would re-add
// names already in the registry, and the registry refuses a duplicate rather
// than replacing it, so the whole turn would fail before the model runs.
func buildToolset(root, profileDir string) (*tools.Registry, error) {
	registry := tools.NewRegistry()
	if err := builtintools.Register(registry, builtintools.Options{
		Root:        root,
		AllowShell:  true,
		ProfileDir:  profileDir,
		HostVersion: Version,
	}); err != nil {
		return nil, err
	}
	w := web.New(web.Config{})
	// Providers are mounted here rather than by a profile row because the
	// Android host composes no profile for a turn: it builds the harness
	// directly. Both are registered even when unusable (no DEEPSEEK_API_KEY on
	// a fresh install), because the seam's structured selection error is more
	// useful to the model than a silently absent capability.
	fetch, err := httpfetch.New(httpfetch.Config{})
	if err != nil {
		return nil, fmt.Errorf("jni: constructing the http fetch provider: %w", err)
	}
	if _, err := w.RegisterFetchProvider(fetch); err != nil {
		return nil, fmt.Errorf("jni: registering the http fetch provider: %w", err)
	}
	search, err := deepseeksearch.New(deepseeksearch.Config{})
	if err != nil {
		return nil, fmt.Errorf("jni: constructing the deepseek search provider: %w", err)
	}
	if _, err := w.RegisterSearchProvider(search); err != nil {
		return nil, fmt.Errorf("jni: registering the deepseek search provider: %w", err)
	}
	if err := toolweb.Register(registry, w, toolweb.Options{}); err != nil {
		return nil, err
	}
	return registry, nil
}

// sessionHistory rebuilds the model-visible history of an open session.
//
// The log is the only place the earlier turns survive, because the agent is
// constructed per turn. The rebuilt list must match what the live loop had in
// memory, so it goes through RebuildHistory with the same tool Render hooks
// the registry holds now: a session resumed under a different projection would
// show the model a different conversation than the one recorded.
func sessionHistory(log *session.Log, registry *tools.Registry) ([]llm.Message, error) {
	// Flush first: an event still sitting in the log's write buffer IS recorded,
	// and a resumed turn that missed it would replay a shorter conversation than
	// the log says happened. ReadLog then reads the whole session directory;
	// Path names the log file inside it.
	if err := log.Flush(); err != nil {
		return nil, fmt.Errorf("jni: flushing session events: %w", err)
	}
	events, err := session.ReadLog(filepath.Dir(log.Path()))
	if err != nil {
		return nil, fmt.Errorf("jni: reading session events: %w", err)
	}
	lookup := func(name string) func(any) string {
		tool, ok := registry.Lookup(name)
		if !ok || tool.Render == nil {
			return nil
		}
		return tool.Render
	}
	history, err := agent.RebuildHistory(events, lookup)
	if err != nil {
		return nil, fmt.Errorf("jni: rebuilding the session history: %w", err)
	}
	return history, nil
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
	// Each web section is attached only when its tool is registered, and the
	// search guidance drops the web_fetch recommendation when that tool is
	// absent: telling the model about a tool it cannot call is worse than
	// saying nothing.
	_, haveSearch := registry.Lookup(toolweb.SearchToolName)
	_, haveFetch := registry.Lookup(toolweb.FetchToolName)
	if err := toolweb.AttachPromptSectionsFor(r, haveSearch, haveFetch); err != nil {
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
