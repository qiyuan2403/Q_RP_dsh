// Package builtintools registers the tools the harness offers out of the box.
//
// The set is deliberately small: read, write, edit, list, and shell. Each one
// is bounded by the working directory it is given, because a tool the model can
// call is a tool a model mistake can call too, and "delete the wrong file" is
// not recoverable.
//
// Every tool's arguments are validated against its JSON Schema by the registry
// before it runs, so these bodies may assume well-formed input.
package builtintools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/systemprompt"
	"github.com/qiyuan2403/Q_RP_dsh/tools"
	"github.com/qiyuan2403/Q_RP_dsh/tools/shell"
)

// Options configures the tool set.
type Options struct {
	// Root bounds every filesystem operation. Empty means the process's working
	// directory, resolved at registration.
	Root string
	// MaxReadBytes caps a file read, so a large file cannot exhaust memory.
	MaxReadBytes int64
	// MaxOutputBytes caps a command's captured output.
	MaxOutputBytes int
	// ShellTimeout bounds one command.
	ShellTimeout time.Duration
	// AllowShell enables the shell tool. It is a separate switch because a
	// deployment may want file access without command execution.
	AllowShell bool
	// ProfileDir is the npm plugin profile the plugin_manager tool manages.
	// Empty registers no plugin tool: without a profile there is nothing to
	// manage.
	ProfileDir string
	// HostVersion is the harness version the plugin engine check runs
	// against. Empty disables the engine check.
	HostVersion string
	// Interpreter is the shell executor the shell tool runs on. Zero means
	// the platform default. Exactly one executor is mounted per registry:
	// the official harness fails a duplicate mount loudly rather than
	// silently replacing, and so does this one.
	Interpreter shell.Executor
}

func (o *Options) withDefaults() Options {
	out := *o
	if out.Root == "" {
		if cwd, err := os.Getwd(); err == nil {
			out.Root = cwd
		}
	}
	if out.MaxReadBytes <= 0 {
		out.MaxReadBytes = 1 << 20
	}
	if out.MaxOutputBytes <= 0 {
		out.MaxOutputBytes = 128 << 10
	}
	if out.ShellTimeout <= 0 {
		out.ShellTimeout = 2 * time.Minute
	}
	// Exactly one shell executor is mounted: the explicit one when given,
	// the platform default otherwise. A second mount is refused at Register
	// rather than silently replacing what an existing conversation resolves.
	if out.Interpreter.Command == "" {
		out.Interpreter = shell.PlatformInterpreter(runtime.GOOS)
	}
	if out.Interpreter.Budgets.MaxOutputBytes <= 0 {
		out.Interpreter.Budgets.MaxOutputBytes = out.MaxOutputBytes
	}
	if out.Interpreter.Budgets.DefaultTimeout <= 0 {
		out.Interpreter.Budgets.DefaultTimeout = out.ShellTimeout
	}
	// The managed overlay carries the harness facts the model reads: the
	// home the shell tool is bounded by, and the interpreter that runs. The
	// registry owns the reserved keys, so a future contributor cannot
	// shadow these.
	if out.Interpreter.Budgets.Env == nil {
		env := shell.NewEnvRegistry(out.Root)
		managed, err := env.Collect()
		if err == nil {
			out.Interpreter.Budgets.Env = managed
		}
		// A Collect failure leaves the overlay off: the command still starts
		// from the scrubbed base, it just reads no DSH_* facts. That is
		// degraded, not broken.
	}
	return out
}

// Register adds the tools to a registry.
//
// Registering twice with AllowShell is a duplicate shell mount: the second
// call fails loudly rather than silently replacing the executor an existing
// conversation's tool calls resolve to. The registry already refuses a
// duplicate tool name with the same semantics; this guard only names the
// cause in the error the caller sees.
func Register(r *tools.Registry, opts Options) error {
	cfg := opts.withDefaults()
	if cfg.AllowShell {
		if _, exists := r.Lookup("bash"); exists {
			return errors.New("builtintools: a shell tool is already registered; exactly one shell executor may be mounted")
		}
	}
	for _, t := range []tools.Tool{
		readTool(cfg),
		writeTool(cfg),
		editTool(cfg),
		listTool(cfg),
	} {
		if err := r.Register(t); err != nil {
			return err
		}
	}
	if cfg.AllowShell {
		if err := r.Register(shellTool(cfg)); err != nil {
			return err
		}
	}
	if cfg.ProfileDir != "" {
		if err := r.Register(pluginManagerTool(cfg, cfg.HostVersion)); err != nil {
			return err
		}
	}
	return nil
}

// resolve maps a caller-supplied path into the root and refuses an escape.
//
// Containment is checked on the RESOLVED path, not the literal one: `..`
// segments and symlinks are both resolved first, so neither can walk out. This
// is the single guard every file tool goes through, which is why it lives in
// one place rather than in each tool.
func resolve(root, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is empty")
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs = filepath.Clean(abs)

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	// A path that does not exist yet still has a resolvable parent, so the
	// check works for a write target as well as a read target.
	resolvedRoot := rootAbs
	if r, err := filepath.EvalSymlinks(rootAbs); err == nil {
		resolvedRoot = r
	}
	resolved := abs
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		resolved = r
	} else if r, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		resolved = filepath.Join(r, filepath.Base(abs))
	}

	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the working directory", path)
	}
	return abs, nil
}

func readTool(cfg Options) tools.Tool {
	return tools.Tool{
		Name:        "read",
		Description: "Read a file's contents. Paths are relative to the working directory.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "File to read"},
				"offset": {"type": "integer", "description": "1-based line to start at"},
				"limit": {"type": "integer", "description": "Maximum lines to read"}
			},
			"required": ["path"]
		}`),
		Run: func(_ context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				Path   string `json:"path"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, err
			}
			full, err := resolve(cfg.Root, in.Path)
			if err != nil {
				return nil, err
			}
			info, err := os.Stat(full)
			if err != nil {
				return nil, err
			}
			if info.IsDir() {
				return nil, fmt.Errorf("%s is a directory; use the list tool", in.Path)
			}
			if info.Size() > cfg.MaxReadBytes {
				return nil, fmt.Errorf("%s is %d bytes, over the %d-byte read limit", in.Path, info.Size(), cfg.MaxReadBytes)
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return nil, err
			}
			text := string(data)

			lines := strings.Split(text, "\n")
			if in.Offset > 0 || in.Limit > 0 {
				start := 0
				if in.Offset > 1 {
					start = in.Offset - 1
				}
				if start > len(lines) {
					return nil, fmt.Errorf("offset %d is past the end of %s (%d lines)", in.Offset, in.Path, len(lines))
				}
				end := len(lines)
				if in.Limit > 0 && start+in.Limit < end {
					end = start + in.Limit
				}
				lines = lines[start:end]
			}
			return map[string]any{
				"path":  in.Path,
				"lines": len(lines),
				"text":  strings.Join(lines, "\n"),
			}, nil
		},
	}
}

func writeTool(cfg Options) tools.Tool {
	return tools.Tool{
		Name:        "write",
		Description: "Write a file, creating parent directories. Overwrites an existing file.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "File to write"},
				"content": {"type": "string", "description": "Full file content"}
			},
			"required": ["path", "content"]
		}`),
		Run: func(_ context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, err
			}
			full, err := resolve(cfg.Root, in.Path)
			if err != nil {
				return nil, err
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(full, []byte(in.Content), 0o644); err != nil {
				return nil, err
			}
			return map[string]any{"path": in.Path, "bytes": len(in.Content)}, nil
		},
	}
}

func editTool(cfg Options) tools.Tool {
	return tools.Tool{
		Name: "edit",
		Description: "Replace one exact string in a file. " +
			"The old text must appear exactly once, so an ambiguous edit is refused rather than guessed at.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "File to edit"},
				"old": {"type": "string", "description": "Exact text to replace (must be unique)"},
				"new": {"type": "string", "description": "Replacement text"}
			},
			"required": ["path", "old", "new"]
		}`),
		Run: func(_ context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				Path string `json:"path"`
				Old  string `json:"old"`
				New  string `json:"new"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, err
			}
			if in.Old == "" {
				return nil, fmt.Errorf("old text is empty; use the write tool to create a file")
			}
			full, err := resolve(cfg.Root, in.Path)
			if err != nil {
				return nil, err
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return nil, err
			}
			// THE EOL CONTRACT: the model supplies LF text; the file may be
			// CRLF. Compare and replace in the canonical LF form, then
			// restore the file's own style on write-back, so a CRLF file is
			// not silently rewritten to LF and an LF `old` still matches.
			text, style := forEdit(data)
			count := strings.Count(text, in.Old)
			switch {
			case count == 0:
				return nil, fmt.Errorf("the old text was not found in %s", in.Path)
			case count > 1:
				return nil, fmt.Errorf("the old text appears %d times in %s; include more context to make it unique", count, in.Path)
			}
			updated := restoreLineEndings(strings.Replace(text, in.Old, in.New, 1), style)
			if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
				return nil, err
			}
			return map[string]any{"path": in.Path, "bytes": len(updated)}, nil
		},
	}
}

func listTool(cfg Options) tools.Tool {
	return tools.Tool{
		Name:        "list",
		Description: "List a directory's entries.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "Directory to list (defaults to the working directory)"}
			}
		}`),
		Run: func(_ context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(raw, &in)
			if in.Path == "" {
				in.Path = "."
			}
			full, err := resolve(cfg.Root, in.Path)
			if err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(full)
			if err != nil {
				return nil, err
			}
			out := make([]map[string]any, 0, len(entries))
			for _, e := range entries {
				item := map[string]any{"name": e.Name(), "dir": e.IsDir()}
				if info, err := e.Info(); err == nil {
					item["size"] = info.Size()
				}
				out = append(out, item)
			}
			return map[string]any{"path": in.Path, "entries": out}, nil
		},
	}
}

func shellTool(cfg Options) tools.Tool {
	description := "Execute a shell command and return its stdout/stderr. " +
		"Each call runs in a fresh shell; pass a path relative to the working directory instead of using `cd`. " +
		"Long output is truncated to its tail. " +
		"An unset variable expands to an empty string, so guard variables with `${VAR:?}`. " +
		"Before any delete or move, verify that the resolved absolute target path is the intended one; never run it against a computed path you have not checked. " +
		"Non-zero exits are reported as `[exit code: N]` markers: investigate failures before moving on, and never retry a `[sandbox: ...]` denial another way."
	if shell.IsMksh(cfg.Interpreter.Command) {
		// The interpreter's dialect is part of the contract, not trivia: a
		// model assuming bash on mksh generates `[[ ]]` and array syntax that
		// mksh rejects or misparses, which is exactly how a Windows-shaped
		// harness produced its bashism errors.
		description += " The interpreter is mksh, not bash: `[[ ]]` is unavailable (use `[ ]`), arrays exist but differ, and some bash builtins such as `read -a` are missing."
	} else if cfg.Interpreter.Command != "" {
		description += " The interpreter is " + cfg.Interpreter.Command + "."
	}
	return tools.Tool{
		Name:        "bash",
		Description: description,
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {"type": "string", "description": "Command line to run"},
				"timeout_ms": {"type": "integer", "description": "Optional timeout in milliseconds"}
			},
			"required": ["command"]
		}`),
		Run: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				Command   string `json:"command"`
				TimeoutMS int    `json:"timeout_ms"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, err
			}
			if strings.TrimSpace(in.Command) == "" {
				return nil, fmt.Errorf("command is empty")
			}
			executor := cfg.Interpreter
			if in.TimeoutMS > 0 {
				requested := time.Duration(in.TimeoutMS) * time.Millisecond
				if requested > executor.Budgets.MaxTimeout {
					requested = executor.Budgets.MaxTimeout
				}
				executor.Budgets.DefaultTimeout = requested
			}
			result, err := executor.Run(ctx, cfg.Root, in.Command)
			if err != nil {
				// The command never ran: an infrastructure fault the model
				// cannot read a result from.
				return nil, err
			}
			// A finished run is a RESULT, never a tool error: the model reads
			// the markers and decides what to do next.
			return map[string]any{
				"command": in.Command,
				"output":  shell.Render(result),
			}, nil
		},
	}
}

// AttachPromptSections contributes each registered tool's prompt section to
// the assembly, in the official tool-bash shape: the guidance rides with the
// tool that carries the behavior, so it cannot drift from it. A tool without
// a section contributes nothing.
func AttachPromptSections(r *systemprompt.Registry, registry *tools.Registry) error {
	for _, name := range registry.Names() {
		section, ok := toolPromptSection(name)
		if !ok {
			continue
		}
		if err := r.Section(systemprompt.Section{
			Name:  section.Name,
			Order: systemprompt.SectionOrders[section.OrderKey],
			Text:  section.Text,
		}); err != nil {
			return err
		}
	}
	return nil
}

// toolSection is one tool-owned prompt section.
type toolSection struct {
	Name     string
	OrderKey string
	Text     string
}

// toolPromptSections maps a tool name to the prompt section it owns. The
// official harness injects the bash guidance from tool-bash itself; the same
// ownership is preserved here so the text stays next to the behavior.
func toolPromptSections() map[string]toolSection {
	return map[string]toolSection{
		"bash": {
			Name:     "tool:bash",
			OrderKey: "TOOL_BASH",
			Text:     "Check the [exit code: N] marker on every shell result; investigate failures before moving on. Never retry a command whose result carries a `[sandbox: ...]` denial marker another way.",
		},
	}
}

// toolPromptSection returns the section a tool owns, if any.
func toolPromptSection(name string) (toolSection, bool) {
	s, ok := toolPromptSections()[name]
	return s, ok
}
