package app

import (
	"context"
	"strings"

	"core/cli/app/internal/status"
	"core/shared/textutil"

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
		var model *string
		if sessionPickerStatusHasModel(req) {
			model = textutil.OptionalTrimmedString(base.Model.Summary)
		}
		return sessionPickerStatusMsg{
			cwd:    textutil.OptionalTrimmedString(statusDisplayPath(base.Workdir, "")),
			branch: branch,
			auth:   textutil.OptionalTrimmedString(status.AuthDisplayLabel(authInfo)),
			model:  model,
		}
	}
}

func sessionPickerStatusRequestUseful(req uiStatusRequest, authManager status.AuthStateResolver) bool {
	if strings.TrimSpace(req.WorkspaceRoot) != "" {
		return true
	}
	if sessionPickerStatusHasModel(req) {
		return true
	}
	if req.AuthStatus != nil {
		return true
	}
	return status.NormalizeAuthStateResolver(authManager) != nil
}

func sessionPickerStatusHasModel(req uiStatusRequest) bool {
	return strings.TrimSpace(req.ModelName) != "" ||
		strings.TrimSpace(req.ConfiguredModelName) != "" ||
		strings.TrimSpace(req.Settings.Model) != ""
}
