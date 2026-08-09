package app

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIDraftRecoveryOwnership(t *testing.T) {
	submittedQueueItemID := runtimeids.NewQueueItemID()
	otherQueueItemID := runtimeids.NewQueueItemID()
	submittedText := "submitted text"
	newerText := "newer draft"
	stopped := clientui.QueuedUserMessageFailureStopped

	tests := []struct {
		name        string
		arrange     func(*uiModel)
		act         func(*uiModel)
		wantInput   string
		wantBuffers []serverapi.SessionDraftRecoveryBuffer
	}{
		{
			name: "active submit remains recoverable after durable persistence",
			arrange: func(model *uiModel) {
				model.beginSubmitAttempt(submittedText, "", activeSubmitOriginDirect)
			},
			wantBuffers: []serverapi.SessionDraftRecoveryBuffer{{
				Kind: serverapi.SessionDraftRecoveryBufferActiveSubmit,
				Text: submittedText,
			}},
		},
		{
			name: "direct send success clears active submit recovery",
			arrange: func(model *uiModel) {
				model.beginSubmitAttempt(submittedText, "", activeSubmitOriginDirect)
			},
			act: func(model *uiModel) {
				_, _ = model.inputController().handleSubmitDone(submitDoneMsg{
					token:         model.activeSubmit.token,
					submittedText: submittedText,
				})
			},
		},
		{
			name: "lost direct send result preserves possible duplicate recovery",
			arrange: func(model *uiModel) {
				model.beginSubmitAttempt(submittedText, "", activeSubmitOriginDirect)
				model.mainEditor.Replace(newerText)
			},
			act: func(model *uiModel) {
				_, _ = model.inputController().handleSubmitDone(submitDoneMsg{
					token:         model.activeSubmit.token,
					submittedText: submittedText,
					err:           io.EOF,
				})
			},
			wantInput: newerText + "\n\n" + submittedText,
			wantBuffers: []serverapi.SessionDraftRecoveryBuffer{{
				Kind: serverapi.SessionDraftRecoveryBufferActiveSubmit,
				Text: submittedText,
			}},
		},
		{
			name: "direct send error preserves possible duplicate recovery",
			arrange: func(model *uiModel) {
				model.beginSubmitAttempt(submittedText, "", activeSubmitOriginDirect)
				model.mainEditor.Replace(newerText)
			},
			act: func(model *uiModel) {
				_, _ = model.inputController().handleSubmitDone(submitDoneMsg{
					token:         model.activeSubmit.token,
					submittedText: submittedText,
					err:           errors.New("result delivery failed"),
				})
			},
			wantInput: newerText + "\n\n" + submittedText,
			wantBuffers: []serverapi.SessionDraftRecoveryBuffer{{
				Kind: serverapi.SessionDraftRecoveryBufferActiveSubmit,
				Text: submittedText,
			}},
		},
		{
			name: "typed not applied restores without overwriting newer text",
			arrange: func(model *uiModel) {
				model.beginSubmitAttempt(submittedText, "", activeSubmitOriginDirect)
				model.mainEditor.Replace(newerText)
			},
			act: func(model *uiModel) {
				_, _ = model.inputController().handleSubmitDone(submitDoneMsg{
					token:         model.activeSubmit.token,
					submittedText: submittedText,
					err:           serverapi.ErrRuntimeOperationCanceled,
				})
			},
			wantInput: newerText + "\n\n" + submittedText,
		},
		{
			name: "steer acceptance transfers recovery to volatile Queue Item ownership",
			arrange: func(model *uiModel) {
				model.beginSubmitAttempt(submittedText, "", activeSubmitOriginDirect)
			},
			act: func(model *uiModel) {
				_, _ = model.inputController().handleSubmitDone(submitDoneMsg{
					token:         model.activeSubmit.token,
					submittedText: submittedText,
					queued: clientui.QueuedUserMessage{
						ID:   submittedQueueItemID.String(),
						Text: submittedText,
					},
				})
			},
			wantBuffers: []serverapi.SessionDraftRecoveryBuffer{{
				Kind: serverapi.SessionDraftRecoveryBufferPendingInjectedInput,
				Text: submittedText,
			}},
		},
		{
			name: "post turn Queue retains identity free recovery",
			arrange: func(model *uiModel) {
				model.queued = []queuedInputItem{{ID: "volatile-local-id", Text: submittedText}}
			},
			wantBuffers: []serverapi.SessionDraftRecoveryBuffer{{
				Kind: serverapi.SessionDraftRecoveryBufferQueuedInput,
				Text: submittedText,
			}},
		},
		{
			name: "authoritative Queue Item delivery clears initiating local recovery",
			arrange: func(model *uiModel) {
				model.registerSteeredQueuedUserMessage(clientui.QueuedUserMessage{
					ID:   submittedQueueItemID.String(),
					Text: submittedText,
				})
			},
			act: func(model *uiModel) {
				_ = model.applyTranscriptQueuedMessageState(clientui.TranscriptQueuedMessageState{
					QueueItemID: submittedQueueItemID,
					Status:      clientui.QueuedUserMessageSubmitted,
				})
			},
		},
		{
			name: "Queue Item not applied restores without overwriting newer text",
			arrange: func(model *uiModel) {
				model.registerSteeredQueuedUserMessage(clientui.QueuedUserMessage{
					ID:   submittedQueueItemID.String(),
					Text: submittedText,
				})
				model.mainEditor.Replace(newerText)
			},
			act: func(model *uiModel) {
				_ = model.applyTranscriptQueuedMessageState(clientui.TranscriptQueuedMessageState{
					QueueItemID:   submittedQueueItemID,
					Status:        clientui.QueuedUserMessageFailed,
					FailureReason: &stopped,
					Text:          &submittedText,
				})
			},
			wantInput: newerText + "\n\n" + submittedText,
		},
		{
			name: "another client transcript snapshot cannot clear or adopt recovery",
			arrange: func(model *uiModel) {
				model.registerSteeredQueuedUserMessage(clientui.QueuedUserMessage{
					ID:   submittedQueueItemID.String(),
					Text: submittedText,
				})
			},
			act: func(model *uiModel) {
				model.reconcileTranscriptQueuedMessages([]clientui.TranscriptQueuedMessageState{{
					QueueItemID: otherQueueItemID,
					Status:      clientui.QueuedUserMessageAccepted,
					Text:        &newerText,
				}})
			},
			wantBuffers: []serverapi.SessionDraftRecoveryBuffer{{
				Kind: serverapi.SessionDraftRecoveryBufferPendingInjectedInput,
				Text: submittedText,
			}},
		},
		{
			name: "transcript flush cannot clear ambiguous active recovery",
			arrange: func(model *uiModel) {
				model.beginSubmitAttempt(submittedText, "", activeSubmitOriginDirect)
			},
			act: func(model *uiModel) {
				stepID, err := runtimeids.ParseStepID("33333333-3333-4333-8333-333333333333")
				if err != nil {
					panic(err)
				}
				_ = model.applyTranscriptUserMessageFlushed(clientui.TranscriptUserMessageFlushed{
					StepID: stepID,
				})
			},
			wantBuffers: []serverapi.SessionDraftRecoveryBuffer{{
				Kind: serverapi.SessionDraftRecoveryBufferActiveSubmit,
				Text: submittedText,
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := NewProjectedUIModel(&draftRecoveryRuntimeClient{}).(*uiModel)
			model.startupCmds = nil
			if test.arrange != nil {
				test.arrange(model)
			}
			if test.act != nil {
				test.act(model)
			}
			if got := model.mainEditor.Text(); got != test.wantInput {
				t.Fatalf("editable input = %q, want %q", got, test.wantInput)
			}
			if got := model.sessionDraftRecoveryBuffers(); !slices.Equal(got, test.wantBuffers) {
				t.Fatalf("Draft Recovery = %+v, want %+v", got, test.wantBuffers)
			}
		})
	}
}

func TestTUISubmitPersistsActiveRecoveryBeforeNetworkSend(t *testing.T) {
	persistErr := errors.New("draft persistence failed")
	tests := []struct {
		name            string
		persistErr      error
		wantSubmitCalls int
	}{
		{name: "persistence succeeds before send", wantSubmitCalls: 1},
		{name: "persistence failure prevents send", persistErr: persistErr},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &draftRecoveryLifecycleClient{persistErr: test.persistErr}
			runtimeClient := &draftRecoveryRuntimeClient{beforeSubmit: func() error {
				if lifecycle.persistCalls == 0 {
					return errors.New("network send started before Draft Recovery persistence")
				}
				return nil
			}}
			model := NewProjectedUIModel(
				runtimeClient,
				WithUISessionID("session-1"),
				WithUISessionDraftPersistence(lifecycle),
			).(*uiModel)
			model.startupCmds = nil
			model.mainEditor.Replace("send me")

			next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = next.(*uiModel)
			runDraftRecoveryCommands(t, model, cmd)

			if lifecycle.persistCalls != 1 {
				t.Fatalf("Draft Recovery persistence calls = %d, want 1", lifecycle.persistCalls)
			}
			if runtimeClient.submitCalls != test.wantSubmitCalls {
				t.Fatalf("runtime submit calls = %d, want %d", runtimeClient.submitCalls, test.wantSubmitCalls)
			}
			if test.persistErr == nil {
				want := []serverapi.SessionDraftRecoveryBuffer{{
					Kind: serverapi.SessionDraftRecoveryBufferActiveSubmit,
					Text: "send me",
				}}
				if !slices.Equal(lifecycle.lastRequest.RecoveryBuffers, want) {
					t.Fatalf("persisted Draft Recovery = %+v, want %+v", lifecycle.lastRequest.RecoveryBuffers, want)
				}
			}
		})
	}
}

func TestPersistedIdentityFreeDraftRecoveryReopensAsEditableTextWithoutReplay(t *testing.T) {
	model := NewProjectedUIModel(
		&draftRecoveryRuntimeClient{},
		WithUIInitialInput("newer draft"),
		WithUIInitialRecoveryBuffers([]serverapi.SessionDraftRecoveryBuffer{
			{Kind: serverapi.SessionDraftRecoveryBufferActiveSubmit, Text: "possibly submitted"},
			{Kind: serverapi.SessionDraftRecoveryBufferPendingInjectedInput, Text: "pending steer"},
			{Kind: serverapi.SessionDraftRecoveryBufferQueuedInput, Text: "queued later"},
		}),
	).(*uiModel)

	if got, want := model.mainEditor.Text(), "newer draft\n\npossibly submitted\n\npending steer\n\nqueued later"; got != want {
		t.Fatalf("reopened input = %q, want %q", got, want)
	}
	if model.startupSubmit != "" || model.activeSubmit.token != 0 || len(model.pendingInjected) != 0 || len(model.injectedQueue) != 0 || len(model.queued) != 0 {
		t.Fatalf(
			"recovery replayed operational work: startup=%q active=%+v pending=%+v injected=%+v queued=%+v",
			model.startupSubmit,
			model.activeSubmit,
			model.pendingInjected,
			model.injectedQueue,
			model.queued,
		)
	}
}

type draftRecoveryRuntimeClient struct {
	clientui.RuntimeClient
	beforeSubmit func() error
	submitCalls  int
}

func (*draftRecoveryRuntimeClient) MainView() clientui.RuntimeMainView {
	return clientui.RuntimeMainView{}
}

func (c *draftRecoveryRuntimeClient) SubmitRuntimeInput(_ context.Context, request clientui.RuntimeSubmitRequest) (clientui.UserTurnSubmission, error) {
	c.submitCalls++
	if c.beforeSubmit != nil {
		if err := c.beforeSubmit(); err != nil {
			return clientui.UserTurnSubmission{}, err
		}
	}
	text, err := request.Input.CanonicalHistoryText()
	if err != nil {
		return clientui.UserTurnSubmission{}, err
	}
	return clientui.UserTurnSubmission{Message: text}, nil
}

type draftRecoveryLifecycleClient struct {
	apicontract.SessionLifecycleService
	persistCalls int
	lastRequest  serverapi.SessionPersistInputDraftRequest
	persistErr   error
}

func (c *draftRecoveryLifecycleClient) PersistInputDraft(
	_ context.Context,
	request serverapi.SessionPersistInputDraftRequest,
) (serverapi.SessionPersistInputDraftResponse, error) {
	c.persistCalls++
	c.lastRequest = request
	return serverapi.SessionPersistInputDraftResponse{}, c.persistErr
}

func runDraftRecoveryCommands(t *testing.T, model *uiModel, command tea.Cmd) {
	t.Helper()
	pending := []tea.Cmd{command}
	for len(pending) > 0 {
		next := pending[0]
		pending = pending[1:]
		if next == nil {
			continue
		}
		message := next()
		if batch, ok := message.(tea.BatchMsg); ok {
			pending = append(batch, pending...)
			continue
		}
		updated, followup := model.Update(message)
		var ok bool
		model, ok = updated.(*uiModel)
		if !ok {
			t.Fatalf("updated model = %T, want *uiModel", updated)
		}
		if followup != nil {
			pending = append(pending, followup)
		}
	}
}
