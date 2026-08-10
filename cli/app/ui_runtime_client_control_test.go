package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type runtimeControlStatusPatchClient struct {
	reconnectRetryRuntimeControlClient
	fastModeResp       serverapi.RuntimeSetFastModeEnabledResponse
	reviewerResp       serverapi.RuntimeSetReviewerEnabledResponse
	autoCompactionResp serverapi.RuntimeSetAutoCompactionEnabledResponse
}

func (c *runtimeControlStatusPatchClient) SetFastModeEnabled(context.Context, serverapi.RuntimeSetFastModeEnabledRequest) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
	return c.fastModeResp, nil
}

func (c *runtimeControlStatusPatchClient) SetReviewerEnabled(context.Context, serverapi.RuntimeSetReviewerEnabledRequest) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
	return c.reviewerResp, nil
}

func (c *runtimeControlStatusPatchClient) SetAutoCompactionEnabled(context.Context, serverapi.RuntimeSetAutoCompactionEnabledRequest) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
	return c.autoCompactionResp, nil
}

func (c *runtimeControlStatusPatchClient) SetQuestionsEnabled(context.Context, serverapi.RuntimeSetQuestionsEnabledRequest) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
	return serverapi.RuntimeSetQuestionsEnabledResponse{}, nil
}

func TestRuntimeClientControlMutationsPatchCachedSessionStatus(t *testing.T) {
	controls := &runtimeControlStatusPatchClient{
		fastModeResp:       serverapi.RuntimeSetFastModeEnabledResponse{Changed: true},
		reviewerResp:       serverapi.RuntimeSetReviewerEnabledResponse{Changed: true, Mode: "edits"},
		autoCompactionResp: serverapi.RuntimeSetAutoCompactionEnabledResponse{Changed: true, Enabled: true},
	}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls).(*sessionRuntimeClient)
	runtimeClient.storeMainView(clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}})

	if err := runtimeClient.SetSessionName("renamed"); err != nil {
		t.Fatalf("SetSessionName: %v", err)
	}
	if err := runtimeClient.SetThinkingLevel("high"); err != nil {
		t.Fatalf("SetThinkingLevel: %v", err)
	}
	if changed, err := runtimeClient.SetFastModeEnabled(true); err != nil || !changed {
		t.Fatalf("SetFastModeEnabled changed=%v err=%v, want changed", changed, err)
	}
	if changed, mode, err := runtimeClient.SetReviewerEnabled(true); err != nil || !changed || mode != "edits" {
		t.Fatalf("SetReviewerEnabled changed=%v mode=%q err=%v, want edits", changed, mode, err)
	}
	if changed, enabled, err := runtimeClient.SetAutoCompactionEnabled(true); err != nil || !changed || !enabled {
		t.Fatalf("SetAutoCompactionEnabled changed=%v enabled=%v err=%v, want enabled", changed, enabled, err)
	}

	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if view.Session.SessionName != "renamed" ||
		view.Status.ThinkingLevel != "high" ||
		!view.Status.FastModeEnabled ||
		!view.Status.ReviewerEnabled ||
		view.Status.ReviewerFrequency != "edits" ||
		!view.Status.AutoCompactionEnabled {
		t.Fatalf("cached session status was not patched: %+v", view)
	}
}

func TestRuntimeClientInputRequestUsesCallerOperationIdentity(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls).(*sessionRuntimeClient)
	ref := clientui.RuntimeOperationRef{
		Kind:            clientui.RuntimeOperationKindSubmit,
		ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
	}

	if _, err := runtimeClient.SubmitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
		OperationRef:                    ref,
		PreSubmitCompactionOperationRef: newRuntimeOperationRef(clientui.RuntimeOperationKindPreSubmitCompact),
		Input:                           runtimeinput.Text("hello"),
	}); err != nil {
		t.Fatalf("SubmitRuntimeInput: %v", err)
	}
	if got := controls.submitRequestIDs(); len(got) != 1 || got[0] != ref.ClientRequestID.String() {
		t.Fatalf("request ids = %+v, want %q", got, ref.ClientRequestID.String())
	}
	if len(controls.submitRefs) != 1 || controls.submitRefs[0] != ref {
		t.Fatalf("operation refs = %+v, want %+v", controls.submitRefs, ref)
	}
}

func TestRuntimeCtrlCUsesActualClientReconciliationForRetainedPreActiveSubmission(t *testing.T) {
	tests := []struct {
		name           string
		state          clientui.RuntimeInputReconciliationState
		wantInput      string
		wantExit       UIAction
		wantSubmitLive bool
	}{
		{
			name:           "durably canceled creator restores editable input",
			state:          clientui.RuntimeInputReconciliationCanceledNotCommitted,
			wantInput:      "retained input",
			wantExit:       UIActionNone,
			wantSubmitLive: false,
		},
		{
			name:           "stale retained hint detaches without canceling submission",
			state:          clientui.RuntimeInputReconciliationAccepted,
			wantExit:       UIActionExit,
			wantSubmitLive: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := clientui.RuntimeOperationRef{
				Kind:            clientui.RuntimeOperationKindSubmit,
				ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
			}
			controls := &reconnectRetryRuntimeControlClient{
				interruptResp: serverapi.RuntimeInterruptResponse{
					Version:  clientui.ReadModelVersion{Epoch: "actual-retained-client", Generation: 1, Sequence: 2},
					Activity: clientui.RuntimeActivity{State: clientui.RuntimeActivityRegisteredIdle},
					InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{
						Operations: []clientui.RuntimeInputReconciliation{{
							Operation: target,
							State:     test.state,
						}},
					},
				},
			}
			runtimeClient := newUIRuntimeClientWithReads(
				"session-1",
				&countingSessionViewClient{},
				controls,
			).(*sessionRuntimeClient)
			runtimeClient.storeMainView(clientui.RuntimeMainView{
				Version: clientui.ReadModelVersion{Epoch: "actual-retained-client", Generation: 1, Sequence: 1},
				Status: clientui.RuntimeStatus{
					WorkflowSession: &clientui.WorkflowSessionStatus{TaskID: "task-1"},
				},
				Session:  clientui.RuntimeSessionView{SessionID: "session-1"},
				Activity: clientui.RuntimeActivity{State: clientui.RuntimeActivityRegisteredIdle},
			})
			model := newProjectedTestUIModel(runtimeClient)
			model.beginSubmitAttempt("retained input", "", target, activeSubmitOriginDirect)

			next, command := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			updated := next.(*uiModel)
			var done runtimeControlDoneMsg
			for _, message := range collectCmdMessages(t, command) {
				if typed, ok := message.(runtimeControlDoneMsg); ok {
					done = typed
				}
			}
			next, _ = updated.Update(done)
			updated = next.(*uiModel)

			if controls.interruptReq.TargetOperationRef == nil ||
				*controls.interruptReq.TargetOperationRef != target {
				t.Fatalf("actual client target = %+v, want %+v", controls.interruptReq.TargetOperationRef, target)
			}
			if got := testMainInput(updated); got != test.wantInput {
				t.Fatalf("input = %q, want %q", got, test.wantInput)
			}
			if updated.exitAction != test.wantExit {
				t.Fatalf("exit action = %q, want %q", updated.exitAction, test.wantExit)
			}
			if (updated.activeSubmit.token != 0) != test.wantSubmitLive {
				t.Fatalf("active submission live = %t, want %t", updated.activeSubmit.token != 0, test.wantSubmitLive)
			}
		})
	}
}

func TestRuntimeClientInterruptTimeoutCancelsOnlyItsWaiter(t *testing.T) {
	serverContinued := make(chan struct{})
	controls := &reconnectRetryRuntimeControlClient{
		interruptFn: func(ctx context.Context, _ serverapi.RuntimeInterruptRequest) (serverapi.RuntimeInterruptResponse, error) {
			<-ctx.Done()
			close(serverContinued)
			return serverapi.RuntimeInterruptResponse{}, ctx.Err()
		},
	}
	runtimeClient := newUIRuntimeClientWithReads(
		"session-1",
		&countingSessionViewClient{},
		controls,
	).(*sessionRuntimeClient)

	started := time.Now()
	if _, err := runtimeClient.interruptRuntimeCandidate(nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("interrupt error = %v, want deadline exceeded", err)
	}
	elapsed := time.Since(started)
	if elapsed < uiRuntimeControlTimeout || elapsed > uiRuntimeControlTimeout+time.Second {
		t.Fatalf("interrupt waiter elapsed = %v, want approximately %v", elapsed, uiRuntimeControlTimeout)
	}
	select {
	case <-serverContinued:
	default:
		t.Fatal("interrupt waiter timeout did not cancel its RPC context")
	}
}
