package app

import (
	"context"
	"strings"
	"time"

	"core/shared/client"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

const subagentSessionSuffix = "subagent"

type RunPromptResult struct {
	SessionID   string
	SessionName string
	Result      string
	Duration    time.Duration
	Warnings    []string
}

func runPrompt(ctx context.Context, client client.RunPromptClient, opts Options, initialSessionID, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (RunPromptResult, error) {
	response, err := client.RunPrompt(ctx, serverapi.RunPromptRequest{
		ClientRequestID:   uuid.NewString(),
		SelectedSessionID: strings.TrimSpace(initialSessionID),
		ParentSessionID:   runPromptParentSessionID(opts, initialSessionID),
		Prompt:            prompt,
		Timeout:           timeout,
		Overrides:         runPromptOverridesFromOptions(opts),
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

func runPromptParentSessionID(opts Options, initialSessionID string) string {
	if strings.TrimSpace(initialSessionID) != "" {
		return ""
	}
	return strings.TrimSpace(opts.WorkspaceContextSessionID)
}

func runPromptOverridesFromOptions(opts Options) serverapi.RunPromptOverrides {
	return serverapi.RunPromptOverrides{
		AgentRole:           strings.TrimSpace(opts.AgentRole),
		Model:               strings.TrimSpace(opts.Model),
		ProviderOverride:    strings.TrimSpace(opts.ProviderOverride),
		ThinkingLevel:       strings.TrimSpace(opts.ThinkingLevel),
		Theme:               strings.TrimSpace(opts.Theme),
		ModelTimeoutSeconds: opts.ModelTimeoutSeconds,
		Tools:               strings.TrimSpace(opts.Tools),
		OpenAIBaseURL:       strings.TrimSpace(opts.OpenAIBaseURL),
	}
}
