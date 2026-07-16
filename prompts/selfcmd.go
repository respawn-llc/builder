package prompts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	brand "core/shared/config"
)

const fallbackBinaryName = brand.Command

// loginShellLookupTimeout bounds the single login-shell PATH resolution so prompt
// assembly can never hang on a misbehaving shell profile.
const loginShellLookupTimeout = 2 * time.Second

// effectiveExecutablePathCache memoizes the resolved launch command for the
// process lifetime. The running binary and the operator's login PATH are stable
// for a server process, the resolution shells out, and prompt assembly calls this
// many times, so resolving once also keeps injected commands stable for provider
// prompt caching.
var effectiveExecutablePathCache struct {
	once sync.Once
	path string
}

func LaunchCommand() string {
	return formatLaunchCommand(effectiveExecutablePath())
}

// ContinueRunCommandWithRoot builds the continuation command and includes
// `--persistence-root <root>` when persistenceRoot is non-empty, so a run that
// selected a non-default root via the flag emits a command that targets the same
// instance instead of the caller's default/inherited root.
func ContinueRunCommandWithRoot(sessionID, persistenceRoot string) string {
	return formatContinueRunCommandWithRoot(effectiveExecutablePath(), sessionID, persistenceRoot)
}

// effectiveExecutablePath prefers the short binary name (e.g. `kent`) over the
// verbose absolute path whenever the short command resolves to the very same
// binary that is currently running. This keeps injected commands terse for the
// common case while still surfacing the full path when `kent` is not resolvable
// or points at a different on-disk binary than the running executable.
func effectiveExecutablePath() string {
	effectiveExecutablePathCache.once.Do(func() {
		path := currentExecutablePath()
		if shortCommandResolvesToRunningBinary(path, loginShellLookPath, filepath.EvalSymlinks) {
			effectiveExecutablePathCache.path = fallbackBinaryName
			return
		}
		effectiveExecutablePathCache.path = path
	})
	return effectiveExecutablePathCache.path
}

// shortCommandResolvesToRunningBinary reports whether the terse brand command
// (e.g. `kent`) resolves to the currently running binary, so injected commands
// can use the short name instead of a verbose absolute path.
//
// Resolution must compare the actual on-disk binary, never the file name: several
// distinct binaries can be named `kent`, and only the one the short command
// actually resolves to is safe to collapse onto. The brand command is resolved
// with lookPath and both paths are reduced to their canonical on-disk targets via
// evalSymlinks; the short command is used only when those targets are identical.
// If resolution fails or yields a different binary, the verbose path is kept.
func shortCommandResolvesToRunningBinary(executablePath string, lookPath func(string) (string, error), evalSymlinks func(string) (string, error)) bool {
	executablePath = strings.TrimSpace(executablePath)
	if executablePath == "" || executablePath == fallbackBinaryName {
		return false
	}
	resolved, err := lookPath(fallbackBinaryName)
	if err != nil {
		return false
	}
	canonicalResolved := canonicalPath(resolved, evalSymlinks)
	canonicalExecutable := canonicalPath(executablePath, evalSymlinks)
	if canonicalResolved == "" || canonicalExecutable == "" {
		return false
	}
	return canonicalResolved == canonicalExecutable
}

// loginShellLookupShell mirrors the shell tool's default execution shell
// (server/tools/shell/exec_command_tool.go): the operator's `$SHELL`, falling
// back to `/bin/sh` when it is unset. Keep these in sync so a collapsed short
// command is only emitted when the shell that will run it can resolve it.
const loginShellLookupShell = "/bin/sh"

// loginShellLookPath resolves a command to its absolute path through the same
// login shell the shell tool uses to execute injected commands (`$SHELL -lc`,
// or `/bin/sh -lc` when $SHELL is unset). This matters because the server can run
// with a stripped environment (e.g. macOS launchd/GUI start, PATH =
// /usr/bin:/bin:/usr/sbin:/sbin) while that login shell sources the operator's
// profile and reconstructs the real PATH. Resolution is deliberately scoped to the
// execution shell rather than the process PATH: a short command must only be
// collapsed onto when the shell that will run it can find the same binary, so any
// resolution failure returns an error and the caller keeps the absolute path.
func loginShellLookPath(name string) (string, error) {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		shell = loginShellLookupShell
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginShellLookupTimeout)
	defer cancel()
	// name is the compile-time brand command constant, not operator input, so the
	// command text carries no untrusted data.
	out, err := exec.CommandContext(ctx, shell, "-lc", "command -v -- "+name).Output()
	if err != nil {
		return "", err
	}
	resolved := strings.TrimSpace(string(out))
	if !filepath.IsAbs(resolved) {
		// `command -v` returns a bare word for shell builtins/aliases/functions;
		// only an absolute path identifies a real on-disk binary to compare.
		return "", exec.ErrNotFound
	}
	return resolved, nil
}

// canonicalPath resolves symlinks (best effort) and cleans the path so two
// references to the same binary compare equal.
func canonicalPath(path string, evalSymlinks func(string) (string, error)) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if resolved, err := evalSymlinks(path); err == nil {
		path = strings.TrimSpace(resolved)
	}
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func formatLaunchCommand(executablePath string) string {
	executablePath = strings.TrimSpace(executablePath)
	if executablePath == "" {
		return fallbackBinaryName
	}
	if executablePath == fallbackBinaryName {
		return fallbackBinaryName
	}
	return strconv.Quote(executablePath)
}

func formatRunCommandPrefix(executablePath string) string {
	return formatLaunchCommand(executablePath) + " run"
}

func formatContinueRunCommand(executablePath, sessionID string) string {
	return formatContinueRunCommandWithRoot(executablePath, sessionID, "")
}

func formatContinueRunCommandWithRoot(executablePath, sessionID, persistenceRoot string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	prefix := formatRunCommandPrefix(executablePath)
	if root := strings.TrimSpace(persistenceRoot); root != "" {
		prefix += " --persistence-root " + strconv.Quote(root)
	}
	return fmt.Sprintf("%s --continue %s %s", prefix, strconv.Quote(sessionID), strconv.Quote("follow-up"))
}

func currentExecutablePath() string {
	path, err := os.Executable()
	if err != nil {
		return fallbackBinaryName
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return fallbackBinaryName
	}
	cleaned := filepath.Clean(path)
	if strings.TrimSpace(cleaned) == "." {
		return fallbackBinaryName
	}
	return cleaned
}
