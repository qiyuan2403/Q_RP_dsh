package shell

import (
	"os"
	"sort"
	"strings"
)

// EnvOverrides are the model-friendly environment values merged FIRST into a
// spawn's explicit environment, so a trusted caller's own entry still wins.
// They disable colors, pagers, and interactive terminal features that would
// garble captured output: the same set the official bash-local hardcodes,
// which matches what Claude Code achieves via TERM=dumb.
var EnvOverrides = map[string]string{
	"NO_COLOR":  "1",
	"TERM":      "dumb",
	"PAGER":     "cat",
	"GIT_PAGER": "cat",
}

// sensitiveEnvSubstrings name the credential shapes scrubbed from an
// inherited environment. It is the official SENSITIVE_ENV_PATTERN: the
// harness's own keys and secrets must not leak into a spawned process
// implicitly, because a shell command is a user-authorized action but not a
// credential grant.
var sensitiveEnvSubstrings = []string{"key", "password", "secret", "token"}

// ScrubbedParentEnv is the ambient parent environment minus credential-shaped
// names and minus all `DSH_*` names: the canonical base every harness child
// starts from. PATH, HOME, locale, and proxy variables survive, so child CLIs
// run normally; harness identity never leaks implicitly, and a deliberately
// forwarded credential or current DSH_* fact goes through the executor's
// explicit env, which merges after this scrub.
//
// Both scrubs match case-insensitively: Windows environment names are
// case-insensitive, so a parent `dsh_*` entry would otherwise survive and read
// back as `$env:DSH_*` in the child; deliberate lowercase `dsh_*` names on
// POSIX are implausible. Ported from the official scrubbedParentEnv.
func ScrubbedParentEnv() []string {
	var out []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if sensitiveEnvName(name) || dshEnvName(name) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// MergedEnv builds the environment one shell command receives: the scrubbed
// parent base, then the model-friendly overrides, then the managed `DSH_*`
// overlay, in that order so the managed overlay wins over the base but a
// trusted caller's explicit entry (already inside the base) is the only thing
// that can defeat an override.
//
// The result is deterministic per call, so a command's environment does not
// drift between two invocations of the same command.
func MergedEnv(managed map[string]string) []string {
	base := ScrubbedParentEnv()
	baseByName := make(map[string]string, len(base))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		baseByName[name] = entry
	}
	// Overrides merge after the scrub: a trusted caller's own entry still
	// wins, which is why the override does not overwrite an existing key
	// unless it came from the ambient base.
	for name, value := range EnvOverrides {
		if _, ambient := baseByName[name]; !ambient {
			baseByName[name] = name + "=" + value
		}
	}
	for name, value := range managed {
		baseByName[name] = name + "=" + value
	}
	names := make([]string, 0, len(baseByName))
	for name := range baseByName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(baseByName))
	for _, name := range names {
		out = append(out, baseByName[name])
	}
	return out
}

func sensitiveEnvName(name string) bool {
	upper := strings.ToUpper(name)
	for _, pattern := range sensitiveEnvSubstrings {
		if strings.Contains(upper, strings.ToUpper(pattern)) {
			return true
		}
	}
	return false
}

func dshEnvName(name string) bool {
	return strings.HasPrefix(strings.ToUpper(name), EnvPrefix)
}
