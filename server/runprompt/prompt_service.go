package runprompt

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/shared/serverapi"
)

// Sentinel errors returned by PromptService input validation. Callers and tests
// must match these with errors.Is rather than comparing rendered message text.
var (
	// ErrPromptServiceLauncherRequired is returned when the service has no
	// configured headless prompt launcher.
	ErrPromptServiceLauncherRequired = errors.New("prompt service launcher is required")
	// ErrClientRequestIDRequired is returned when a request omits its
	// client_request_id.
	ErrClientRequestIDRequired = errors.New("client_request_id is required")
	// ErrPromptRequired is returned when a request omits a non-blank prompt.
	ErrPromptRequired = errors.New("prompt is required")
)

type PromptAssistantMessage struct {
	SessionID     string
	SessionName   string
	Content       string
	Warnings      []string
	DroppedEvents uint64
}

type PromptSessionRuntime interface {
	RecordPromptHistory(ctx context.Context, clientRequestID string, prompt string) error
	SubmitUserMessage(ctx context.Context, prompt string) (PromptAssistantMessage, error)
	Logf(format string, args ...any)
	Close() error
}

type failedPromptSessionRuntime interface {
	CloseWithFailure() error
}

type HeadlessPromptLauncher interface {
	PrepareHeadlessPrompt(ctx context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (PromptSessionRuntime, error)
}

type PromptService struct {
	launcher HeadlessPromptLauncher
}

func NewPromptService(launcher HeadlessPromptLauncher) *PromptService {
	return &PromptService{launcher: launcher}
}

func (s *PromptService) RunPrompt(ctx context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error) {
	if s == nil || s.launcher == nil {
		return serverapi.RunPromptResponse{}, ErrPromptServiceLauncherRequired
	}
	req.ClientRequestID = strings.TrimSpace(req.ClientRequestID)
	req.SelectedSessionID = strings.TrimSpace(req.SelectedSessionID)
	req.ParentSessionID = strings.TrimSpace(req.ParentSessionID)
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.ClientRequestID == "" {
		return serverapi.RunPromptResponse{}, ErrClientRequestIDRequired
	}
	if req.Prompt == "" {
		return serverapi.RunPromptResponse{}, ErrPromptRequired
	}
	if err := req.Overrides.ValidateAgentRoleOverride(); err != nil {
		return serverapi.RunPromptResponse{}, err
	}

	runtimeHandle, err := s.launcher.PrepareHeadlessPrompt(ctx, req, progress)
	if err != nil {
		return serverapi.RunPromptResponse{}, err
	}
	closeWithFailure := false
	defer func() {
		if closeWithFailure {
			if failureRuntime, ok := runtimeHandle.(failedPromptSessionRuntime); ok {
				_ = failureRuntime.CloseWithFailure()
				return
			}
		}
		_ = runtimeHandle.Close()
	}()

	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	startedAt := time.Now()
	if err := runtimeHandle.RecordPromptHistory(runCtx, req.ClientRequestID, req.Prompt); err != nil {
		closeWithFailure = true
		return serverapi.RunPromptResponse{}, err
	}
	assistant, runErr := runtimeHandle.SubmitUserMessage(runCtx, req.Prompt)
	duration := time.Since(startedAt)
	result := serverapi.RunPromptResponse{
		SessionID:   assistant.SessionID,
		SessionName: assistant.SessionName,
		Result:      assistant.Content,
		Duration:    duration,
		Warnings:    append([]string(nil), assistant.Warnings...),
	}
	if dropped := assistant.DroppedEvents; dropped > 0 {
		runtimeHandle.Logf("runtime.event.drop.total=%d", dropped)
	}
	if runErr != nil {
		closeWithFailure = true
		runtimeHandle.Logf("app.run_prompt.exit err=%q", runErr.Error())
		return result, runErr
	}
	runtimeHandle.Logf("app.run_prompt.exit ok")
	return result, nil
}
