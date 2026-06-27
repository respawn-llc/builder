package tools

import (
	"core/shared/config"
	"core/shared/sessionenv"
	"os"
	"strings"
)

var overrides = []string{
	"AGENT=" + config.Command,
	"TERM=dumb",
	"COLORTERM=",
	"CI=1",
	"NO_COLOR=1",
	"CLICOLOR=0",
	"CLICOLOR_FORCE=0",
	"FORCE_COLOR=0",
	"PAGER=cat",
	"GIT_PAGER=cat",
	"GH_PAGER=cat",
	"MANPAGER=cat",
	"SYSTEMD_PAGER=",
	"BAT_PAGER=cat",
	"GIT_EDITOR=:",
	"EDITOR=:",
	"VISUAL=:",
	"GIT_TERMINAL_PROMPT=0",
	"GCM_INTERACTIVE=Never",
	"DEBIAN_FRONTEND=noninteractive",
	"PY_COLORS=0",
	"CARGO_TERM_COLOR=never",
	"NPM_CONFIG_COLOR=false",
	"npm_config_progress=false",
	"YARN_ENABLE_PROGRESS_BARS=false",
	"DOCKER_CLI_HINTS=false",
	"BUILDKIT_PROGRESS=plain",
	"COMPOSE_PROGRESS=plain",
	"COMPOSE_ANSI=never",
}

func EnrichShellEnv(base []string) []string {
	return EnrichShellEnvForSession(base, "")
}

func EnrichShellEnvForSession(base []string, sessionID string) []string {
	env := make(map[string]string, len(base)+len(overrides))
	order := make([]string, 0, len(base)+len(overrides))

	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		if _, exists := env[key]; !exists {
			order = append(order, key)
		}
		env[key] = value
	}

	for _, entry := range overrides {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		if _, exists := env[key]; !exists {
			order = append(order, key)
		}
		env[key] = value
	}

	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		if _, exists := env[sessionenv.SessionIDEnv]; !exists {
			order = append(order, sessionenv.SessionIDEnv)
		}
		env[sessionenv.SessionIDEnv] = sessionID
	}

	if _, exists := env["RIPGREP_CONFIG_PATH"]; !exists {
		if path, ok := managedRGConfigEnvValue(); ok {
			order = append(order, "RIPGREP_CONFIG_PATH")
			env["RIPGREP_CONFIG_PATH"] = path
		}
	}

	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+env[key])
	}
	return out
}

func managedRGConfigEnvValue() (string, bool) {
	path, err := config.ResolveManagedRGConfigPath()
	if err != nil || strings.TrimSpace(path) == "" {
		return "", false
	}
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return path, true
}
