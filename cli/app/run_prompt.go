package app

import (
	"context"
	"strings"
	"time"

	"core/cli/app/internal/startupconfig"
	"core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

const subagentSessionSuffix = "subagent"

type RunPromptResult struct {
	SessionID       string
	SessionName     string
	Result          string
	Duration        time.Duration
	Warnings        []string
	CleanupWarnings []string
}

func runPrompt(ctx context.Context, client apicontract.RunPromptService, opts Options, caller startupconfig.CallerContext, initialSessionID, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (RunPromptResult, error) {
	intent, err := runPromptLaunchIntent(opts, initialSessionID)
	if err != nil {
		return RunPromptResult{}, err
	}
	callerSessionID := runPromptCallerSessionID(opts, caller)
	response, err := client.RunPrompt(ctx, serverapi.RunPromptRequest{
		ClientRequestID: uuid.NewString(),
		Intent:          intent,
		CallerSessionID: callerSessionID,
		Prompt:          prompt,
		Timeout:         timeout,
		Overrides:       runPromptOverridesFromOptions(opts),
	}, progress)
	result := RunPromptResult{
		SessionID:   response.SessionID,
		SessionName: response.SessionName,
		Result:      response.Result,
		Duration:    response.Duration,
		Warnings:    append([]string(nil), response.Warnings...),
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func runPromptCallerSessionID(opts Options, caller startupconfig.CallerContext) *string {
	if caller.Kind != startupconfig.CallerKindKentSession {
		return nil
	}
	sessionID := strings.TrimSpace(opts.WorkspaceContextSessionID)
	if sessionID == "" {
		return nil
	}
	return &sessionID
}

func runPromptLaunchIntent(opts Options, initialSessionID string) (serverapi.SessionLaunchIntent, error) {
	if selected := strings.TrimSpace(initialSessionID); selected != "" {
		sessionID, err := runtimeids.ParseSessionID(selected)
		if err != nil {
			return serverapi.SessionLaunchIntent{}, err
		}
		return serverapi.OpenExistingSessionLaunchIntent(sessionID), nil
	}
	if rawParentID := strings.TrimSpace(opts.WorkspaceContextSessionID); rawParentID != "" {
		parsedParentID, err := runtimeids.ParseSessionID(rawParentID)
		if err != nil {
			return serverapi.SessionLaunchIntent{}, err
		}
		return serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(parsedParentID)), nil
	}
	return serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()), nil
}

func runPromptOverridesFromOptions(opts Options) serverapi.RunPromptOverrides {
	var agentRole *string
	if opts.AgentRole != nil {
		value := strings.TrimSpace(*opts.AgentRole)
		agentRole = &value
	}
	return serverapi.RunPromptOverrides{
		AgentRole:           agentRole,
		Model:               strings.TrimSpace(opts.Model),
		ProviderOverride:    strings.TrimSpace(opts.ProviderOverride),
		ThinkingLevel:       strings.TrimSpace(opts.ThinkingLevel),
		Theme:               strings.TrimSpace(opts.Theme),
		ModelTimeoutSeconds: opts.ModelTimeoutSeconds,
		Tools:               strings.TrimSpace(opts.Tools),
		OpenAIBaseURL:       strings.TrimSpace(opts.OpenAIBaseURL),
	}
}
