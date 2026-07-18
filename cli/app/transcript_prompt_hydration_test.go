package app

import (
	"context"
	"io"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func runningPromptTestActivity() clientui.RuntimeActivity {
	return clientui.RuntimeActivity{
		State:          clientui.RuntimeActivityRunning,
		QueueAccepting: true,
		ActiveStep: &clientui.RuntimeActiveStep{
			ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
			RunID:      ongoingTestRunID(),
			StepID:     ongoingTestStepID(),
		},
	}
}

func newDeferredApprovalTestModel(runtimeClient clientui.RuntimeClient, control *recordingPromptControl) *uiModel {
	model := sizedTestUIModel(newProjectedTestUIModel(runtimeClient), 80, 24)
	model.promptAnswers = newTranscriptPromptAnswerer(context.Background(), control)
	model.ongoingTranscript = newPromptTestOngoingTranscriptController(model, &ongoingSurfaceSpy{})
	return model
}

func newPromptTestOngoingTranscriptController(
	model *uiModel,
	surface ongoingTranscriptSurface,
	loggers ...uiLogger,
) *ongoingTranscriptController {
	return newOngoingTranscriptController(
		surface,
		model.ongoingFrameInput,
		promptTestRuntimeAdmission,
		model.applyAdmittedTranscriptMessageState,
		model,
		loggers...,
	)
}

func promptTestRuntimeAdmission(message clientui.TranscriptMessage) (runtimeTupleMergeResult, error) {
	var update clientui.RuntimeReadModelUpdate
	switch message.Kind {
	case clientui.TranscriptMessageHydration:
		update = message.Payload.Hydration.RuntimeReadModelUpdate
	case clientui.TranscriptMessageRuntimeReadModelUpdate:
		update = *message.Payload.RuntimeReadModelUpdate
	default:
		return runtimeTupleMergeResult{}, nil
	}
	return runtimeTupleMergeResult{
		decision: runtimeTupleApply,
		view: clientui.RuntimeMainView{
			Version:             update.Version,
			Activity:            update.Activity,
			InputReconciliation: update.InputReconciliation,
		},
		project: true,
	}, nil
}

func deliverPromptHydration(t *testing.T, model *uiModel, prompt clientui.TranscriptPrompt) *uiModel {
	t.Helper()
	hydration := ongoingHydrationMessage(1)
	hydration.Payload.Hydration.RuntimeReadModelUpdate.Activity = runningPromptTestActivity()
	hydration.Payload.Hydration.PendingPrompts = []clientui.TranscriptPrompt{prompt}
	return updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message:         hydration,
	})
}

func updatePromptUIModel(t *testing.T, model *uiModel, message tea.Msg) *uiModel {
	t.Helper()
	next, command := model.Update(message)
	updated, ok := next.(*uiModel)
	if !ok {
		t.Fatalf("unexpected model type %T", next)
	}
	if command == nil {
		return updated
	}
	return runPromptTestCommand(t, updated, command)
}

func runPromptTestCommand(t *testing.T, model *uiModel, command tea.Cmd) *uiModel {
	t.Helper()
	if command == nil {
		return model
	}
	harness := &promptTestCommandModel{model: model, command: command}
	program := tea.NewProgram(
		harness,
		tea.WithoutRenderer(),
		tea.WithoutSignals(),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
	)
	final, err := program.Run()
	if err != nil {
		t.Fatalf("run prompt test command: %v", err)
	}
	return final.(*promptTestCommandModel).model
}

type promptTestCommandModel struct {
	model   *uiModel
	command tea.Cmd
}

func (m *promptTestCommandModel) Init() tea.Cmd {
	return tea.Sequence(m.command, tea.Quit)
}

func (m *promptTestCommandModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	next, command := m.model.Update(message)
	m.model = next.(*uiModel)
	return m, command
}

func (m *promptTestCommandModel) View() string {
	return ""
}

func (c *recordingPromptControl) nextApproval(t *testing.T) serverapi.ApprovalAnswerRequest {
	t.Helper()
	select {
	case request := <-c.approvalRequests:
		return request
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for approval answer")
		return serverapi.ApprovalAnswerRequest{}
	}
}

func (c *recordingPromptControl) assertNoApproval(t *testing.T) {
	t.Helper()
	select {
	case request := <-c.approvalRequests:
		t.Fatalf("unexpected approval answer: %+v", request)
	case <-time.After(100 * time.Millisecond):
	}
}

func (c *recordingPromptControl) nextAsk(t *testing.T) serverapi.AskAnswerRequest {
	t.Helper()
	select {
	case request := <-c.askRequests:
		return request
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ask answer")
		return serverapi.AskAnswerRequest{}
	}
}

func boolName(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
