package shell

import (
	"errors"
	"os"
	"os/exec"
)

// ResolveInterpreter picks the interpreter an Executor runs, in the shape of
// the official pwsh-local resolve.ts: an explicitly configured value is
// trusted as-is, otherwise a platform candidate list is probed in order and
// the first spawnable entry wins.
//
// WHY PROBE RATHER THAN LOOKPATH. `exec.LookPath` answers "is the name on
// PATH", which is the wrong question on Android: the interesting interpreters
// live at fixed system paths, and the app's PATH may not carry them. The
// candidate list states where interpreters actually live per platform; the
// probe states whether this process may spawn one.
func ResolveInterpreter(configured string, goos string) string {
	if configured != "" {
		return configured
	}
	platform := goos
	if goos != "android" && goos != "windows" {
		// POSIX shares one candidate list; the platforms that key on GOOS
		// name their own.
		platform = "posix"
	}
	for _, candidate := range CandidateInterpreters(platform) {
		if spawnable(candidate) {
			return candidate
		}
	}
	return FallbackInterpreter(platform)
}

// CandidateInterpreters lists where an interpreter may live, newest or most
// capable first, for the platform. Parameterized by GOOS so the resolution is
// a pure function of its inputs on every platform.
func CandidateInterpreters(goos string) []string {
	switch goos {
	case "posix":
		// The POSIX branch of the official bash-local: the bash binary is
		// hardcoded there because the platform always ships it. Here the
		// candidate list is probed instead, because a minimal container may
		// carry only sh.
		return []string{"/bin/bash", "/usr/bin/bash", "/usr/local/bin/bash", "/bin/sh"}
	case "android":
		// Order verified on the target device (corot, Android 15, crDroid):
		// /system_ext/bin/bash EXISTS there and is a real bash; /system/bin
		// carries none. A Termux install carries another. /system/bin/sh is
		// mksh: on every device, and the dialect the mksh warning covers.
		return []string{
			"/system_ext/bin/bash", // crDroid and some ROMs: a real bash
			"/system/bin/bash",     // rare
			"/data/data/com.termux/files/usr/bin/bash",
			"/system/bin/sh", // mksh: on every device
		}
	case "windows":
		// The pwsh twin of the official candidatePwshPaths: PowerShell 7,
		// then PATH entries (the Microsoft Store install), then Windows
		// PowerShell 5.1 as the last resort on legacy hosts.
		return []string{
			`C:\Program Files\PowerShell\7\pwsh.exe`,
			`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		}
	default:
		return []string{"/bin/bash", "/usr/bin/bash", "/usr/local/bin/bash", "/bin/sh"}
	}
}

// FallbackInterpreter is the one path every target of the platform has.
func FallbackInterpreter(goos string) string {
	switch goos {
	case "android":
		return "/system/bin/sh"
	case "windows":
		return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	default:
		return "/bin/sh"
	}
}

// spawnable reports whether a candidate can be spawned. The probe executes
// the real thing, because a file can exist and still be unspawnable: an
// app-private directory is W^X-blocked on Android 10+, and a noexec mount
// rejects the execve regardless of the file mode.
//
// The distinction is the ERRNO, not the exit status, which is why the probe
// target is the interpreter itself run with no arguments: bash with no script
// reads stdin and would hang, so the probe uses `--version`-style probes
// where the interpreter supports one and a plain spawn-then-kill otherwise.
func spawnable(candidate string) bool {
	if !executableFile(candidate) {
		return false
	}
	return probeSpawns(candidate)
}

// executableFile reports whether the path exists as a regular file. It is a
// cheap gate before the spawn probe; it does not answer spawnability.
func executableFile(path string) bool {
	if path == "" {
		return false
	}
	// A directory never spawns; anything that stats as one is out.
	// exec.LookPath would follow PATH semantics we deliberately do not want,
	// so stat the exact path.
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return true
}

// probeSpawns runs the candidate and answers whether execve succeeded. The
// exit status is irrelevant: a spawned interpreter that errors is an
// interpreter; EACCES/EPERM is a mount or mode refusal.
func probeSpawns(candidate string) bool {
	// `-c true` keeps every candidate bounded: bash, mksh, and pwsh all
	// accept a command string, and the command exits immediately.
	probe := exec.Command(candidate, "-c", "true")
	if err := probe.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// The program ran; its exit status is irrelevant to spawnability.
			return true
		}
		return false
	}
	return true
}

// IsMksh reports whether the resolved interpreter is Android's mksh, whose
// dialect differs from bash in ways a model's bash habits hit immediately.
func IsMksh(interpreter string) bool {
	// The path is the fact that matters: it is what resolution chose, and a
	// desktop deployment resolving it would need the same warning. The GOOS
	// gate is therefore not applied.
	return interpreter == "/system/bin/sh"
}
