package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"

	tea "github.com/charmbracelet/bubbletea"
)

type promptInvariantLogger struct {
	calls int
	args  []any
}

func (l *promptInvariantLogger) Logf(_ string, args ...any) {
	l.calls++
	l.args = append([]any(nil), args...)
}

func captureTestPanic(run func()) (value any) {
	defer func() {
		value = recover()
	}()
	run()
	return nil
}

func mustTestSessionID(raw string) runtimeids.SessionID {
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		panic(err)
	}
	return id
}

func mustTestStepID(raw string) runtimeids.StepID {
	id, err := runtimeids.ParseStepID(raw)
	if err != nil {
		panic(err)
	}
	return id
}

func TestOngoingTranscriptForeignSessionPayloadPanics(t *testing.T) {
	foreignSessionID := mustTestSessionID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	testCases := []struct {
		name     string
		baseline bool
		scratch  bool
		setup    func(*testing.T, *uiModel) *uiModel
		message  func() clientui.TranscriptMessage
	}{
		{
			name: "initial_hydration",
			message: func() clientui.TranscriptMessage {
				message := ongoingHydrationMessage(1)
				message.Payload.Hydration.SessionIdentity.SessionID = foreignSessionID
				return message
			},
		},
		{
			name: "initial_hydration_prompt",
			message: func() clientui.TranscriptMessage {
				message := ongoingHydrationMessage(1)
				prompt := testQuestionPrompt("foreign-hydrated", "Choose", "one")
				prompt.SessionID = foreignSessionID
				message.Payload.Hydration.RuntimeReadModelUpdate.Activity = runningPromptTestActivity()
				message.Payload.Hydration.PendingPrompts = []clientui.TranscriptPrompt{prompt}
				return message
			},
		},
		{
			name:     "scratch_hydration",
			baseline: true,
			scratch:  true,
			message: func() clientui.TranscriptMessage {
				message := ongoingHydrationMessage(1)
				message.Payload.Hydration.SessionIdentity.SessionID = foreignSessionID
				return message
			},
		},
		{
			name:     "live_pending",
			baseline: true,
			message: func() clientui.TranscriptMessage {
				prompt := testQuestionPrompt("foreign-pending", "Choose", "one")
				prompt.SessionID = foreignSessionID
				return clientui.TranscriptMessage{
					Sequence: 2,
					Kind:     clientui.TranscriptMessagePromptPending,
					Payload:  clientui.TranscriptPayload{PromptPending: &prompt},
				}
			},
		},
		{
			name:     "live_resolved",
			baseline: true,
			message: func() clientui.TranscriptMessage {
				prompt := testQuestionPrompt("foreign-resolved", "Choose", "one")
				prompt.SessionID = foreignSessionID
				prompt.State = clientui.TranscriptPromptStateResolved
				return clientui.TranscriptMessage{
					Sequence: 2,
					Kind:     clientui.TranscriptMessagePromptResolved,
					Payload:  clientui.TranscriptPayload{PromptResolved: &prompt},
				}
			},
		},
		{
			name:     "live_identity",
			baseline: true,
			message: func() clientui.TranscriptMessage {
				message := ongoingTranscriptMessage(2, clientui.TranscriptMessageSessionIdentity)
				message.Payload.SessionIdentity.SessionID = foreignSessionID
				return message
			},
		},
		{
			name:     "pending_drain",
			baseline: true,
			scratch:  true,
			setup: func(t *testing.T, model *uiModel) *uiModel {
				running := ongoingTranscriptMessage(2, clientui.TranscriptMessageRuntimeReadModelUpdate)
				running.Payload.RuntimeReadModelUpdate.Activity = clientui.RuntimeActivity{
					State:          clientui.RuntimeActivityRunning,
					QueueAccepting: true,
					ActiveStep: &clientui.RuntimeActiveStep{
						ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
						RunID:      ongoingTestRunID(),
						StepID:     ongoingTestStepID(),
					},
				}
				model = updateUIModel(t, model, ongoingTranscriptEvent{
					Kind:            ongoingTranscriptEventMessage,
					SourceSessionID: ongoingTestSessionID(),
					Message:         running,
				})
				model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("queued")})
				model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})

				approval := testApprovalPrompt(
					"approval-pending-drain",
					"Allow?",
					clientui.ApprovalDecisionAllowOnce,
					clientui.ApprovalDecisionDeny,
				)
				model = updateUIModel(t, model, ongoingTranscriptEvent{
					Kind:            ongoingTranscriptEventMessage,
					SourceSessionID: ongoingTestSessionID(),
					Message: clientui.TranscriptMessage{
						Sequence: 3,
						Kind:     clientui.TranscriptMessagePromptPending,
						Payload:  clientui.TranscriptPayload{PromptPending: &approval},
					},
				})
				model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
				model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("commentary")})
				return updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
			},
			message: func() clientui.TranscriptMessage {
				message := ongoingHydrationMessage(1)
				message.Payload.Hydration.SessionIdentity.SessionID = foreignSessionID
				return message
			},
		},
	}

	for _, debugMode := range []bool{false, true} {
		for _, testCase := range testCases {
			t.Run(testCase.name+"/debug="+boolName(debugMode), func(t *testing.T) {
				logger := &promptInvariantLogger{}
				ringer := &countRinger{}
				hooks := newUnfocusedBellHooks(ringer)
				surface := &ongoingSurfaceSpy{}
				control := newBlockingPromptControl()
				reopens := 0
				runtimeClient := &runtimeControlFakeClient{}
				model := sizedTestUIModel(newProjectedTestUIModel(runtimeClient,
					WithUIDebug(debugMode),
					WithUILogger(logger),
					WithUITurnQueueHook(hooks),
					WithUIOngoingTranscriptReopen(func() { reopens++ }),
				), 80, 24)
				model.promptAnswers = newTranscriptPromptAnswerer(context.Background(), control)
				model.promptAttention = hooks
				model.ongoingTranscript = newPromptTestOngoingTranscriptController(model, surface, logger)
				if testCase.baseline {
					model = updateUIModel(t, model, ongoingTranscriptEvent{
						Kind:            ongoingTranscriptEventMessage,
						SourceSessionID: ongoingTestSessionID(),
						Message:         ongoingHydrationMessage(1),
					})
				}
				if testCase.setup != nil {
					model = testCase.setup(t, model)
				}
				if testCase.scratch {
					model = updateUIModel(t, model, ongoingTranscriptEvent{
						Kind:            ongoingTranscriptEventLoss,
						SourceSessionID: ongoingTestSessionID(),
						Err:             errors.New("scratch"),
					})
				}
				surfaceCalls := len(surface.calls)
				notifications := ringer.total()
				beforeTransition := model.Transition()
				beforeReopens := reopens

				panicValue := captureTestPanic(func() {
					_, _ = model.Update(ongoingTranscriptEvent{
						Kind:            ongoingTranscriptEventMessage,
						SourceSessionID: ongoingTestSessionID(),
						Message:         testCase.message(),
					})
				})
				diagnostic, ok := panicValue.(*ongoingTranscriptDeveloperError)
				if !ok {
					t.Fatalf("panic = %T, want *ongoingTranscriptDeveloperError", panicValue)
				}
				if diagnostic.SourceSessionID != ongoingTestSessionID() || diagnostic.PayloadSessionID != foreignSessionID {
					t.Fatalf("diagnostic session IDs = source:%q payload:%q", diagnostic.SourceSessionID.String(), diagnostic.PayloadSessionID.String())
				}
				if diagnostic.Geometry.Width != 80 || diagnostic.Geometry.Height != 24 || diagnostic.Stack == "" {
					t.Fatalf("diagnostic structure incomplete: %+v", diagnostic)
				}
				if len(surface.calls) != surfaceCalls || ringer.total() != notifications || reopens != beforeReopens {
					t.Fatalf("rejected payload produced follow-up: surface=%d/%d notifications=%d/%d reopens=%d/%d",
						len(surface.calls), surfaceCalls, ringer.total(), notifications, reopens, beforeReopens)
				}
				if got := model.Transition(); !reflect.DeepEqual(got, beforeTransition) {
					t.Fatalf("transition changed after panic: got=%+v want=%+v", got, beforeTransition)
				}
				control.assertNoRequest(t)
				if runtimeClient.queueUserMessageCalls != 0 ||
					runtimeClient.hasQueuedUserWorkCalls != 0 ||
					runtimeClient.submitQueuedCalls != 0 {
					t.Fatalf(
						"rejected payload dispatched queued work: create=%d check=%d submit=%d",
						runtimeClient.queueUserMessageCalls,
						runtimeClient.hasQueuedUserWorkCalls,
						runtimeClient.submitQueuedCalls,
					)
				}
				if logger.calls != 1 || len(logger.args) == 0 {
					t.Fatalf("diagnostic logger calls=%d args=%d, want one structured call", logger.calls, len(logger.args))
				}
			})
		}
	}
}

func TestOngoingTranscriptPromptLiveContractMismatchPanics(t *testing.T) {
	runPromptContractMismatchCases(t, clientui.TranscriptMessagePromptPending)
}

func TestOngoingTranscriptPromptHydrationContractMismatchPanics(t *testing.T) {
	runPromptContractMismatchCases(t, clientui.TranscriptMessageHydration)
}

func TestOngoingTranscriptPromptResolvedContractMismatchPanics(t *testing.T) {
	runPromptContractMismatchCases(t, clientui.TranscriptMessagePromptResolved)
}

func runPromptContractMismatchCases(t *testing.T, messageKind clientui.TranscriptMessageKind) {
	t.Helper()
	foreignSessionID := mustTestSessionID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	foreignStepID := mustTestStepID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	mutations := map[string]func(*clientui.TranscriptPrompt){
		"session": func(prompt *clientui.TranscriptPrompt) {
			prompt.SessionID = foreignSessionID
		},
		"kind": func(prompt *clientui.TranscriptPrompt) {
			prompt.Kind = clientui.TranscriptPromptKindApproval
			prompt.Suggestions = nil
			prompt.RecommendedOptionIndex = nil
			prompt.ApprovalOptions = []clientui.ApprovalDecision{clientui.ApprovalDecisionAllowOnce}
		},
		"step": func(prompt *clientui.TranscriptPrompt) {
			prompt.StepID = foreignStepID
		},
		"created_at": func(prompt *clientui.TranscriptPrompt) {
			prompt.CreatedAt = prompt.CreatedAt.Add(time.Second)
		},
		"tool": func(prompt *clientui.TranscriptPrompt) {
			prompt.Tool = &clientui.ToolProvenance{ToolCallID: "tool-mismatch", ToolName: "ask_question"}
		},
	}
	for _, debugMode := range []bool{false, true} {
		for _, queued := range []bool{false, true} {
			for mutationName, mutate := range mutations {
				t.Run(string(messageKind)+"/"+mutationName+"/queued="+boolName(queued)+"/debug="+boolName(debugMode), func(t *testing.T) {
					logger := &promptInvariantLogger{}
					ringer := &countRinger{}
					hooks := newUnfocusedBellHooks(ringer)
					surface := &ongoingSurfaceSpy{}
					model := sizedTestUIModel(newProjectedStaticUIModel(
						WithUIDebug(debugMode),
						WithUILogger(logger),
						WithUITurnQueueHook(hooks),
					), 80, 24)
					model.promptAttention = hooks
					model.ongoingTranscript = newPromptTestOngoingTranscriptController(model, surface, logger)
					active := testQuestionPrompt("contract-active", "Active", "one")
					target := active
					if queued {
						target = testQuestionPrompt("contract-queued", "Queued", "one")
						target.CreatedAt = active.CreatedAt.Add(time.Second)
					}
					hydration := ongoingHydrationMessage(1)
					hydration.Payload.Hydration.RuntimeReadModelUpdate.Activity = runningPromptTestActivity()
					hydration.Payload.Hydration.PendingPrompts = []clientui.TranscriptPrompt{active}
					if queued {
						hydration.Payload.Hydration.PendingPrompts = append(hydration.Payload.Hydration.PendingPrompts, target)
					}
					model = updateUIModel(t, model, ongoingTranscriptEvent{
						Kind:            ongoingTranscriptEventMessage,
						SourceSessionID: ongoingTestSessionID(),
						Message:         hydration,
					})

					incoming := cloneTranscriptPromptForAsk(target)
					mutate(&incoming)
					var message clientui.TranscriptMessage
					switch messageKind {
					case clientui.TranscriptMessagePromptPending:
						message = clientui.TranscriptMessage{
							Sequence: 2,
							Kind:     messageKind,
							Payload:  clientui.TranscriptPayload{PromptPending: &incoming},
						}
					case clientui.TranscriptMessagePromptResolved:
						incoming.State = clientui.TranscriptPromptStateResolved
						message = clientui.TranscriptMessage{
							Sequence: 2,
							Kind:     messageKind,
							Payload:  clientui.TranscriptPayload{PromptResolved: &incoming},
						}
					case clientui.TranscriptMessageHydration:
						model = updateUIModel(t, model, ongoingTranscriptEvent{
							Kind:            ongoingTranscriptEventLoss,
							SourceSessionID: ongoingTestSessionID(),
							Err:             errors.New("scratch"),
						})
						message = hydration
						if queued {
							message.Payload.Hydration.PendingPrompts[1] = incoming
						} else {
							message.Payload.Hydration.PendingPrompts[0] = incoming
						}
					default:
						t.Fatalf("unsupported mismatch message kind %q", messageKind)
					}
					surfaceCalls := len(surface.calls)
					notifications := ringer.total()
					panicValue := captureTestPanic(func() {
						_, _ = model.Update(ongoingTranscriptEvent{
							Kind:            ongoingTranscriptEventMessage,
							SourceSessionID: ongoingTestSessionID(),
							Message:         message,
						})
					})
					diagnostic, ok := panicValue.(*ongoingTranscriptDeveloperError)
					if !ok {
						t.Fatalf("panic = %T, want *ongoingTranscriptDeveloperError", panicValue)
					}
					if diagnostic.PromptID == nil || *diagnostic.PromptID != target.PromptID ||
						diagnostic.OldPromptContract == nil ||
						diagnostic.NewPromptContract == nil {
						t.Fatalf("contract diagnostic incomplete: %+v", diagnostic)
					}
					if len(surface.calls) != surfaceCalls || ringer.total() != notifications {
						t.Fatalf("contract mismatch partially committed: surface=%d/%d notifications=%d/%d",
							len(surface.calls), surfaceCalls, ringer.total(), notifications)
					}
					if logger.calls != 1 || len(logger.args) == 0 {
						t.Fatalf("diagnostic logger calls=%d args=%d", logger.calls, len(logger.args))
					}
				})
			}
		}
	}
}
