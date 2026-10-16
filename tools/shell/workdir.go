package shell

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// WorkingDirectory is one session's current directory: the default for every
// directory-based operation, and the base a per-call `workdir` resolves
// against.
//
// It is the official working-directory contract, minus the process seam:
// a directory change is DURABLE (recorded so a resumed session keeps it), it
// is per-session (existing processes keep their own directories), and it
// serializes concurrent changes per session, because a directory change is a
// state transition, not a concurrent write.
type WorkingDirectory struct {
	mu      sync.Mutex
	current string
	root    string
}

// NewWorkingDirectory builds the directory state over the bounded root. A
// session starts at the root, never at the process's own working directory:
// on Android the process cwd is `/`, which no tool may write.
func NewWorkingDirectory(root string) *WorkingDirectory {
	return &WorkingDirectory{current: root, root: root}
}

// Current returns the effective directory for the next directory-based
// operation.
func (w *WorkingDirectory) Current() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.current
}

// Set changes the directory. A relative path resolves against the current
// one; an absolute path is used as-is, and both must stay inside the bounded
// root. The change is committed only after the target is verified to exist
// and be a directory.
func (w *WorkingDirectory) Set(path string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	target, err := resolveWorkdir(path, w.current, w.root)
	if err != nil {
		return "", err
	}
	// The change commits only after the target is verified to exist and be
	// a directory: an os.Chdir into a file or a missing path would leave the
	// session pointing at something every later tool call would fail on.
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errString("workdir: not a directory: " + target)
	}
	w.current = target
	return target, nil
}

// resolveWorkdir resolves a model-supplied per-call override against the
// session's current directory, in the shape of the official resolveWorkdir:
// an omitted override means the current directory; a relative override joins
// it; an absolute one is used as-is. Every outcome must stay inside root.
func resolveWorkdir(modelWorkdir string, sessionCwd string, root string) (string, error) {
	if strings.TrimSpace(modelWorkdir) == "" {
		return sessionCwd, nil
	}
	target := modelWorkdir
	if !filepath.IsAbs(target) {
		if sessionCwd == "" {
			return "", errWorkdirNoBase
		}
		target = filepath.Join(sessionCwd, target)
	}
	return contained(target, root)
}

// contained verifies an absolute path against the bounded root on the
// RESOLVED path, so `..` segments cannot walk out. Symlink resolution is the
// caller's containment guard (the file tools' `resolve`); this check covers
// the lexical escape a `workdir` argument can express.
func contained(target, root string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errWorkdirOutsideRoot
	}
	return absTarget, nil
}

var (
	errWorkdirNoBase      = errString("workdir: no base directory to resolve a relative path against")
	errWorkdirOutsideRoot = errString("workdir: directory is outside the working root")
)

type errString string

func (e errString) Error() string { return string(e) }
