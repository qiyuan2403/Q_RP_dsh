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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/tools"
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
	return out
}

// Register adds the tools to a registry.
func Register(r *tools.Registry, opts Options) error {
	cfg := opts.withDefaults()
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
			text := string(data)
			count := strings.Count(text, in.Old)
			switch {
			case count == 0:
				return nil, fmt.Errorf("the old text was not found in %s", in.Path)
			case count > 1:
				return nil, fmt.Errorf("the old text appears %d times in %s; include more context to make it unique", count, in.Path)
			}
			updated := strings.Replace(text, in.Old, in.New, 1)
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
	return tools.Tool{
		Name: "bash",
		Description: "Run a shell command in the working directory. " +
			"Returns the exit code and the captured output.",
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
			timeout := cfg.ShellTimeout
			if in.TimeoutMS > 0 {
				timeout = time.Duration(in.TimeoutMS) * time.Millisecond
			}
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			cmd := exec.CommandContext(runCtx, "bash", "-c", in.Command)
			cmd.Dir = cfg.Root
			// The environment is the process's own: a shell command is an
			// explicit user-authorized action, unlike a config expression.
			cmd.Env = os.Environ()

			var buf strings.Builder
			cmd.Stdout = &limitedWriter{buf: &buf, limit: cfg.MaxOutputBytes}
			cmd.Stderr = &limitedWriter{buf: &buf, limit: cfg.MaxOutputBytes}
			err := cmd.Run()

			result := map[string]any{"command": in.Command, "output": buf.String()}
			if cmd.ProcessState != nil {
				result["exitCode"] = cmd.ProcessState.ExitCode()
			}
			if runCtx.Err() == context.DeadlineExceeded {
				result["timedOut"] = true
				return result, fmt.Errorf("command timed out after %s", timeout)
			}
			if err != nil {
				// A non-zero exit is a RESULT the model should see, not a tool
				// failure: the output explains what went wrong.
				return result, nil
			}
			return result, nil
		},
	}
}

// limitedWriter appends to a buffer up to a limit, so a command producing
// unbounded output cannot exhaust memory.
type limitedWriter struct {
	buf   *strings.Builder
	limit int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		// Report success so the command keeps running; the output is truncated
		// rather than the process killed.
		return len(p), nil
	}
	if len(p) > remaining {
		w.buf.Write(p[:remaining])
		w.buf.WriteString("\n... output truncated")
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}
