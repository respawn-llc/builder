package runprompt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"core/shared/serverapi"
)

func TestPromptServiceRejectsEmptyPrompt(t *testing.T) {
	service := NewPromptService(&stubHeadlessPromptLauncher{})

	_, err := service.RunPrompt(context.Background(), serverapi.RunPromptRequest{ClientRequestID: "req-1", Prompt: " \n\t "}, nil)
	if !errors.Is(err, ErrPromptRequired) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPromptServiceRejectsMissingClientRequestID(t *testing.T) {
	service := NewPromptService(&stubHeadlessPromptLauncher{})

	_, err := service.RunPrompt(context.Background(), serverapi.RunPromptRequest{Prompt: "hello"}, nil)
	if !errors.Is(err, ErrClientRequestIDRequired) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPromptServiceRejectsInvalidAgentRoleBeforePreparingLauncher(t *testing.T) {
	launcher := &stubHeadlessPromptLauncher{}
	service := NewPromptService(launcher)

	for _, role := range []string{"none", "self"} {
		t.Run(role, func(t *testing.T) {
			_, err := service.RunPrompt(context.Background(), serverapi.RunPromptRequest{
				ClientRequestID: "req-1",
				Prompt:          "hello",
				Overrides:       serverapi.RunPromptOverrides{AgentRole: role},
			}, nil)
			if err == nil {
				t.Fatal("expected invalid agent role error")
			}
			if launcher.lastRequest.ClientRequestID != "" {
				t.Fatal("launcher should not be prepared for invalid role overrides")
			}
		})
	}
}

func TestPromptServiceRunsPromptThroughPreparedRuntime(t *testing.T) {
	launcher := &stubHeadlessPromptLauncher{
		runtime: &stubPromptSessionRuntime{
			assistant: PromptAssistantMessage{SessionID: "session-1", SessionName: "session one", Content: "done", Warnings: []string{"warning one"}},
		},
	}
	service := NewPromptService(launcher)
	progresses := make([]serverapi.RunPromptProgress, 0, 1)

	result, err := service.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID:   "  req-123  ",
		SelectedSessionID: "  abc-123  ",
		Prompt:            "  hello world  ",
	}, serverapi.RunPromptProgressFunc(func(progress serverapi.RunPromptProgress) {
		progresses = append(progresses, progress)
	}))
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if launcher.lastRequest.SelectedSessionID != "abc-123" {
		t.Fatalf("selected session id = %q, want abc-123", launcher.lastRequest.SelectedSessionID)
	}
	if launcher.lastRequest.ClientRequestID != "req-123" {
		t.Fatalf("client request id = %q, want req-123", launcher.lastRequest.ClientRequestID)
	}
	if launcher.runtime.prompt != "hello world" {
		t.Fatalf("submitted prompt = %q, want hello world", launcher.runtime.prompt)
	}
	if launcher.runtime.historyRequestID != "req-123" || launcher.runtime.historyPrompt != "hello world" {
		t.Fatalf("recorded history request=%q prompt=%q, want trimmed request and prompt", launcher.runtime.historyRequestID, launcher.runtime.historyPrompt)
	}
	if !launcher.runtime.closed {
		t.Fatal("expected prepared runtime to be closed")
	}
	if result.SessionID != "session-1" || result.SessionName != "session one" || result.Result != "done" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != "warning one" {
		t.Fatalf("unexpected warnings: %+v", result.Warnings)
	}
	if len(progresses) != 1 || progresses[0].Kind != serverapi.RunPromptProgressKindStatus {
		t.Fatalf("unexpected progress events: %+v", progresses)
	}
	if launcher.runtime.logs[len(launcher.runtime.logs)-1] != "app.run_prompt.exit ok" {
		t.Fatalf("unexpected logs: %+v", launcher.runtime.logs)
	}
}

func TestPromptServiceReturnsPartialResultOnRunError(t *testing.T) {
	runErr := errors.New("boom")
	launcher := &stubHeadlessPromptLauncher{
		runtime: &stubPromptSessionRuntime{
			assistant: PromptAssistantMessage{SessionID: "session-2", SessionName: "session two", Content: "partial", DroppedEvents: 3},
			err:       runErr,
		},
	}
	service := NewPromptService(launcher)

	result, err := service.RunPrompt(context.Background(), serverapi.RunPromptRequest{ClientRequestID: "req-1", Prompt: "hello"}, nil)
	if !errors.Is(err, runErr) {
		t.Fatalf("RunPrompt error = %v, want %v", err, runErr)
	}
	if result.Result != "partial" || result.SessionID != "session-2" {
		t.Fatalf("unexpected partial result: %+v", result)
	}
	if !launcher.runtime.closed {
		t.Fatal("expected prepared runtime to be closed")
	}
	if !launcher.runtime.failureClosed {
		t.Fatal("expected failed run prompt to use failure close")
	}
	if got := strings.Join(launcher.runtime.logs, "\n"); !strings.Contains(got, "runtime.event.drop.total=3") || !strings.Contains(got, `app.run_prompt.exit err="boom"`) {
		t.Fatalf("unexpected logs: %q", got)
	}
}

func TestPromptServiceAppliesTimeoutToSubmittedRun(t *testing.T) {
	launcher := &stubHeadlessPromptLauncher{
		runtime: &stubPromptSessionRuntime{
			assistant: PromptAssistantMessage{SessionID: "session-timeout", SessionName: "timeout"},
			onSubmit: func(ctx context.Context) {
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("expected timeout deadline on run context")
				}
				if time.Until(deadline) <= 0 {
					t.Fatal("expected future deadline")
				}
			},
		},
	}
	service := NewPromptService(launcher)

	_, err := service.RunPrompt(context.Background(), serverapi.RunPromptRequest{ClientRequestID: "req-1", Prompt: "hello", Timeout: 5 * time.Second}, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
}

type stubHeadlessPromptLauncher struct {
	runtime     *stubPromptSessionRuntime
	lastRequest serverapi.RunPromptRequest
}

func (s *stubHeadlessPromptLauncher) PrepareHeadlessPrompt(_ context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (PromptSessionRuntime, error) {
	s.lastRequest = req
	if progress != nil {
		progress.PublishRunPromptProgress(serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindStatus, Message: "Prepared run context"})
	}
	if s.runtime == nil {
		s.runtime = &stubPromptSessionRuntime{}
	}
	return s.runtime, nil
}

type stubPromptSessionRuntime struct {
	assistant        PromptAssistantMessage
	err              error
	prompt           string
	closed           bool
	failureClosed    bool
	logs             []string
	onSubmit         func(context.Context)
	historyRequestID string
	historyPrompt    string
}

func (s *stubPromptSessionRuntime) RecordPromptHistory(_ context.Context, clientRequestID string, prompt string) error {
	s.historyRequestID = clientRequestID
	s.historyPrompt = prompt
	return nil
}

func (s *stubPromptSessionRuntime) SubmitUserMessage(ctx context.Context, prompt string) (PromptAssistantMessage, error) {
	s.prompt = prompt
	if s.onSubmit != nil {
		s.onSubmit(ctx)
	}
	return s.assistant, s.err
}

func (s *stubPromptSessionRuntime) Logf(format string, args ...any) {
	s.logs = append(s.logs, fmt.Sprintf(format, args...))
}

func (s *stubPromptSessionRuntime) Close() error {
	s.closed = true
	return nil
}

func (s *stubPromptSessionRuntime) CloseWithFailure() error {
	s.failureClosed = true
	return s.Close()
}
