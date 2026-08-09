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
	var model *string
	if strings.TrimSpace(req.ModelName) != "" ||
		strings.TrimSpace(req.ConfiguredModelName) != "" ||
		strings.TrimSpace(req.Settings.Model) != "" {
		model = textutil.OptionalTrimmedString(status.ModelSummary(req))
	}
	if strings.TrimSpace(req.WorkspaceRoot) == "" && model == nil && req.AuthStatus == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), statusRefreshTimeout)
		defer cancel()

		collector := defaultUIStatusCollector()
		base := collector.CollectBase(req)
		gitResult := collector.CollectGit(ctx, req, base)
		authInfo := collector.CollectAuth(ctx, req, base).Auth

		branch := textutil.OptionalTrimmedString(gitResult.Git.Branch)
		if !gitResult.Git.Visible || strings.TrimSpace(gitResult.Git.Error) != "" ||
			(branch != nil && *branch == "unknown") {
			branch = nil
		}
		return sessionPickerStatusMsg{
			cwd:    textutil.OptionalTrimmedString(statusDisplayPath(base.Workdir, "")),
			branch: branch,
			auth:   textutil.OptionalTrimmedString(status.AuthDisplayLabel(authInfo)),
			model:  model,
		}
	}
}
