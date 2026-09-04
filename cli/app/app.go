package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"core/cli/app/internal/runner"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type Options struct {
	WorkspaceRoot             string
	WorkspaceRootExplicit     bool
	SessionID                 string
	WorkspaceContextSessionID string
	AgentRole                 *string
	Model                     string
	ProviderOverride          string
	ThinkingLevel             string
	Theme                     string
	ModelTimeoutSeconds       int
	Tools                     string
	OpenAIBaseURL             string
	OpenAIBaseURLExplicit     bool
	ConfigRoot                string
}

func Run(ctx context.Context, opts Options) error {
	interactor := newInteractiveAuthInteractor()
	return runner.RunInteractive(ctx, runnerRequestFromOptions(opts), runner.Dependencies{
		StartSessionServer: func(ctx context.Context) (io.Closer, error) {
			return startSessionServer(ctx, opts, interactor, true)
		},
		RunSessionLifecycle: func(ctx context.Context, server io.Closer, intent *serverapi.SessionLaunchIntent, overrides serverapi.RunPromptOverrides) error {
			interactive, ok := server.(interactiveSessionServer)
			if !ok {
				return errors.New("interactive session server is required")
			}
			return runSessionLifecycleWithOptions(ctx, interactive, interactor, sessionLifecycleOptions{
				Intent:    intent,
				Overrides: overrides,
			})
		},
	})
}

func RunPrompt(ctx context.Context, opts Options, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (RunPromptResult, error) {
	workspaceConfig, err := resolveRunPromptWorkspaceConfig(opts)
	if err != nil {
		return RunPromptResult{}, err
	}
	runClient, closeFn, err := startRunPromptClientWithWorkspaceConfig(ctx, workspaceConfig)
	if err != nil {
		return RunPromptResult{}, err
	}
	defer func() {
		if closeFn != nil {
			_ = closeFn()
		}
	}()
	runOptions := workspaceConfig.Options
	if strings.TrimSpace(opts.SessionID) != "" {
		runOptions.Theme = ""
	}
	if strings.TrimSpace(opts.SessionID) != "" && strings.TrimSpace(opts.ThinkingLevel) != "" {
		sessionID, sessionIDErr := runtimeids.ParseSessionID(strings.TrimSpace(opts.SessionID))
		if sessionIDErr != nil {
			return RunPromptResult{}, sessionIDErr
		}
		thinkingLevel := strings.TrimSpace(opts.ThinkingLevel)
		controls, closeControls, controlErr := startRuntimeControlRemote(ctx, workspaceConfig.Options)
		if controlErr != nil {
			return RunPromptResult{}, controlErr
		}
		thinkingResponse, thinkingErr := controls.MutateChatSettings(ctx, serverapi.ChatSettingsMutationRequest{
			Target: serverapi.SessionChatSettingsTarget(sessionID),
			Operation: serverapi.ChatSettingsMutationOperation{
				Kind:  serverapi.ChatSettingsMutationThinking,
				Value: &thinkingLevel,
			},
		})
		if thinkingErr == nil && thinkingResponse.Result.Kind == serverapi.ChatSettingsMutationRejected {
			thinkingErr = fmt.Errorf("thinking level mutation rejected: %s", thinkingResponse.Result.Rejected.Reason)
		}
		if closeControls != nil {
			thinkingErr = errors.Join(thinkingErr, closeControls())
		}
		if thinkingErr != nil {
			return RunPromptResult{}, thinkingErr
		}
		runOptions.ThinkingLevel = ""
	}
	return runPrompt(ctx, runClient, runOptions, workspaceConfig.CallerContext, strings.TrimSpace(opts.SessionID), prompt, timeout, progress)
}

func runnerRequestFromOptions(opts Options) runner.Request {
	return runner.Request{
		SessionID:                 opts.SessionID,
		WorkspaceContextSessionID: opts.WorkspaceContextSessionID,
		AgentRole:                 opts.AgentRole,
	}
}
