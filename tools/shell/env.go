package shell

import (
	"fmt"
	"sort"
	"strings"
)

// EnvPrefix is the prefix every managed environment variable carries. It is
// the official DSH_ENV_PREFIX: a command can enumerate harness facts by
// their name alone, and a contributor that declares a key outside the
// namespace fails at registration rather than at the first shell call.
const EnvPrefix = "DSH_"

// Managed keys the registry owns. A contributor that declares one is refused:
// a plugin must not be able to shadow a built-in fact, because the model
// reads these values as the harness's own account of itself.
var reservedKeys = map[string]string{
	EnvPrefix + "HOME":        "the harness home directory",
	EnvPrefix + "SHELL":       "the shell executor's presence",
	EnvPrefix + "SESSION_ID":  "the owning session",
	EnvPrefix + "PROFILE":     "the composed profile",
	EnvPrefix + "PROFILE_DIR": "the composed profile's directory",
}

// EnvVar is the model-visible metadata for one managed variable.
type EnvVar struct {
	// Description states the environment fact the variable represents.
	Description string
}

// EnvContributor contributes trusted per-execution variables. Declared keys
// make ownership conflicts detectable before the first command; Resolve
// computes only the values available for the current execution.
type EnvContributor struct {
	// Name is the stable contributor name used in diagnostics and duplicate
	// detection.
	Name string
	// Variables is the complete set of keys this contributor may return.
	Variables map[string]EnvVar
	// Resolve computes this contributor's values for one execution. It must
	// return only declared keys: an undeclared key is a bug that surfaces as
	// an error rather than as a silent env leak.
	Resolve func() map[string]string
}

// EnvRegistry owns the trusted `DSH_*` variables every shell command
// receives. It is the official shell-env contract: a command's environment
// is an explicit, enumerable overlay rather than the process's own
// environment, so host credentials are NEVER inherited by default.
type EnvRegistry struct {
	// Home is the harness home directory exposed as DSH_HOME.
	Home string
	// SessionID is the owning session's id, when a session owns the call.
	SessionID string
	// Profile and ProfileDir describe the composed profile, when one is
	// composed.
	Profile    string
	ProfileDir string

	contributors map[string]*EnvContributor
}

// NewEnvRegistry builds a registry over the harness's own facts.
func NewEnvRegistry(home string) *EnvRegistry {
	return &EnvRegistry{Home: home, contributors: map[string]*EnvContributor{}}
}

// Register adds a contributor. A duplicate name, an invalid key, or a
// reserved key is refused rather than replacing: an env overlay that silently
// changes under a running session is exactly the kind of drift the registry
// exists to prevent.
func (r *EnvRegistry) Register(c *EnvContributor) error {
	if c.Name == "" {
		return fmt.Errorf("shell env: contributor name is empty")
	}
	if _, exists := r.contributors[c.Name]; exists {
		return fmt.Errorf("shell env: contributor %q is already registered", c.Name)
	}
	for key := range c.Variables {
		if !validKey(key) {
			return fmt.Errorf("shell env: contributor %q declared invalid key %q", c.Name, key)
		}
		if _, reserved := reservedKeys[key]; reserved {
			return fmt.Errorf("shell env: contributor %q declared reserved key %q", c.Name, key)
		}
	}
	r.contributors[c.Name] = c
	return nil
}

// Collect builds the trusted `DSH_*` snapshot for one shell execution: the
// built-ins, then every contributor's current values, sorted by key so the
// overlay is stable across calls.
//
// A contributor that returns an undeclared key fails the call: an undeclared
// value is an env leak, and env leaks must be loud.
func (r *EnvRegistry) Collect() (map[string]string, error) {
	values := map[string]string{
		EnvPrefix + "HOME":  r.Home,
		EnvPrefix + "SHELL": "1",
	}
	if r.SessionID != "" {
		values[EnvPrefix+"SESSION_ID"] = r.SessionID
	}
	if r.Profile != "" {
		values[EnvPrefix+"PROFILE"] = r.Profile
	}
	if r.ProfileDir != "" {
		values[EnvPrefix+"PROFILE_DIR"] = r.ProfileDir
	}
	names := make([]string, 0, len(r.contributors))
	for name := range r.contributors {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := r.contributors[name]
		resolved := c.Resolve()
		for key, value := range resolved {
			if _, declared := c.Variables[key]; !declared {
				return nil, fmt.Errorf("shell env: contributor %q returned undeclared key %q", name, key)
			}
			values[key] = value
		}
	}
	return values, nil
}

// List enumerates the contributors' declarations without resolving them, so
// diagnostics can name the env surface without running any resolver.
func (r *EnvRegistry) List() []string {
	names := make([]string, 0, len(r.contributors))
	for name := range r.contributors {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []string
	for _, name := range names {
		keys := make([]string, 0, len(r.contributors[name].Variables))
		for key := range r.contributors[name].Variables {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out = append(out, keys...)
	}
	return out
}

func validKey(key string) bool {
	if !strings.HasPrefix(key, EnvPrefix) {
		return false
	}
	suffix := strings.TrimPrefix(key, EnvPrefix)
	if suffix == "" {
		return false
	}
	for _, c := range suffix {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
