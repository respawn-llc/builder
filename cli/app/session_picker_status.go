package app

import (
	"context"
	"strings"

	"core/cli/app/internal/status"

	tea "github.com/charmbracelet/bubbletea"
)

type sessionPickerStatusMsg struct {
	cwd    *string
	branch *string
	auth   *string
	model  *string
}

func collectSessionPickerStatusCmd(header sessionPickerHeaderInfo) tea.Cmd {
	req := populateStatusRequestCacheKeys(header.StatusRequest)
	if !sessionPickerStatusRequestUseful(req, header.AuthManager) {
		return nil
	}
	authManager := status.NormalizeAuthStateResolver(header.AuthManager)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), statusRefreshTimeout)
		defer cancel()

		collector := defaultUIStatusCollector{authManager: authManager}.adapter()
		base := collector.CollectBase(req)
		gitResult := collector.CollectGit(ctx, req, base)
		authInfo := status.FastAuthInfo(ctx, authManager, req.Settings)
		if authManager == nil && req.AuthStatus != nil {
			authInfo = collector.CollectAuth(ctx, req, base).Auth
		}

		var branch *string
		if gitResult.Git.Visible && strings.TrimSpace(gitResult.Git.Error) == "" {
			value := strings.TrimSpace(gitResult.Git.Branch)
			if value != "" && value != "unknown" {
				branch = &value
			}
		}
		return sessionPickerStatusMsg{
			cwd:    optionalSessionPickerStatusText(statusDisplayPath(base.Workdir, "")),
			branch: branch,
			auth:   optionalSessionPickerStatusText(status.AuthDisplayLabel(authInfo)),
			model:  optionalSessionPickerStatusText(base.Model.Summary),
		}
	}
}

func optionalSessionPickerStatusText(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func sessionPickerStatusRequestUseful(req uiStatusRequest, authManager status.AuthStateResolver) bool {
	if strings.TrimSpace(req.WorkspaceRoot) != "" {
		return true
	}
	if strings.TrimSpace(req.ModelName) != "" || strings.TrimSpace(req.ConfiguredModelName) != "" {
		return true
	}
	if strings.TrimSpace(req.Settings.Model) != "" {
		return true
	}
	if req.AuthStatus != nil {
		return true
	}
	return status.NormalizeAuthStateResolver(authManager) != nil
}
