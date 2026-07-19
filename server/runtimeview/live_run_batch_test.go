package runtimeview

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"core/server/llm"
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/lifecyclecontract"
	"core/shared/transcript"
)

func TestLiveRunBatchFinishedProjectsEveryDisposition(t *testing.T) {
	finishedAt := time.Date(2026, time.July, 19, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		name           string
		result         runtime.LiveRunResult
		disposition    clientui.LiveRunBatchDisposition
		exclusion      *clientui.LiveRunBatchExclusionReason
		wantPreview    bool
		wantDiagnostic bool
	}{
		{
			name: "final answer",
			result: runtime.LiveRunResult{
				ResultKind:       runtime.LiveRunResultAssistantFinalAnswer,
				FinishedAt:       finishedAt,
				WorkPerformed:    true,
				AssistantMessage: llm.Message{Role: llm.RoleAssistant, Content: "final answer"},
			},
			disposition: clientui.LiveRunBatchDispositionFinalAnswer,
			wantPreview: true,
		},
		{
			name: "runtime failure",
			result: runtime.LiveRunResult{
				ResultKind: runtime.LiveRunResultRuntimeFailure,
				FinishedAt: finishedAt,
				FailureDiagnostic: &runtime.LiveRunFailureDiagnostic{
					Code:   runtime.LiveRunFailureCodeRuntime,
					Detail: "provider failed",
				},
			},
			disposition:    clientui.LiveRunBatchDispositionRuntimeFailure,
			wantDiagnostic: true,
		},
		{
			name: "completed without final answer",
			result: runtime.LiveRunResult{
				ResultKind: runtime.LiveRunResultCompletedNoFinal,
				FinishedAt: finishedAt,
			},
			disposition: clientui.LiveRunBatchDispositionNoFinalAnswer,
		},
		{
			name: "interrupted",
			result: runtime.LiveRunResult{
				ResultKind: runtime.LiveRunResultInterrupted,
				FinishedAt: finishedAt,
			},
			disposition: clientui.LiveRunBatchDispositionInterrupted,
		},
		{
			name: "workflow completed exclusion",
			result: runtime.LiveRunResult{
				ResultKind:    runtime.LiveRunResultWorkflowCompleted,
				FinishedAt:    finishedAt,
				NoFinalReason: runtime.LiveRunNoFinalAnswerReasonWorkflow,
			},
			disposition: clientui.LiveRunBatchDispositionExcluded,
			exclusion:   liveRunBatchExclusionReasonPtr(clientui.LiveRunBatchExclusionWorkflowCompleted),
		},
		{
			name: "non task exclusion",
			result: runtime.LiveRunResult{
				ResultKind: runtime.LiveRunResultNonTaskActivity,
				FinishedAt: finishedAt,
			},
			disposition: clientui.LiveRunBatchDispositionExcluded,
			exclusion:   liveRunBatchExclusionReasonPtr(clientui.LiveRunBatchExclusionNonTaskActivity),
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			result := testCase.result
			messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
				Kind:          runtime.EventLiveRunBatchFinished,
				LiveRunResult: &result,
			})
			if len(messages) != 1 {
				t.Fatalf("projected messages = %+v, want one", messages)
			}
			message := messages[0]
			if message.Kind != clientui.TranscriptMessageLiveRunBatchFinished || message.Payload.LiveRunBatchFinished == nil {
				t.Fatalf("projected message = %+v", message)
			}
			fact := message.Payload.LiveRunBatchFinished
			if fact.Disposition != testCase.disposition || fact.WorkPerformed != testCase.result.WorkPerformed || !fact.FinishedAt.Equal(finishedAt) {
				t.Fatalf("projected fact = %+v", fact)
			}
			if !reflect.DeepEqual(fact.ExclusionReason, testCase.exclusion) {
				t.Fatalf("exclusion reason = %+v, want %+v", fact.ExclusionReason, testCase.exclusion)
			}
			if (fact.FinalAnswerPreview != nil) != testCase.wantPreview || (fact.FailureDiagnostic != nil) != testCase.wantDiagnostic {
				t.Fatalf("projected fact payload = %+v", fact)
			}
			if testCase.wantPreview {
				if fact.FinalAnswerPreview.Markdown != testCase.result.AssistantMessage.Content || fact.FinalAnswerPreview.Truncation != nil {
					t.Fatalf("final-answer preview = %+v, want original untruncated content", fact.FinalAnswerPreview)
				}
			}
			if testCase.wantDiagnostic {
				if fact.FailureDiagnostic.Code != clientui.TranscriptDiagnosticCode(testCase.result.FailureDiagnostic.Code) ||
					fact.FailureDiagnostic.Detail != testCase.result.FailureDiagnostic.Detail {
					t.Fatalf("failure diagnostic = %+v, want %+v", fact.FailureDiagnostic, testCase.result.FailureDiagnostic)
				}
			}
		})
	}
}

func TestLiveRunBatchFinishedProjectsUTF8BoundedFinalAnswerPreview(t *testing.T) {
	content := strings.Repeat("界", 1366)
	result := runtime.LiveRunResult{
		ResultKind:       runtime.LiveRunResultAssistantFinalAnswer,
		FinishedAt:       time.Date(2026, time.July, 19, 20, 0, 0, 0, time.UTC),
		AssistantMessage: llm.Message{Role: llm.RoleAssistant, Content: content},
	}
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:          runtime.EventLiveRunBatchFinished,
		LiveRunResult: &result,
	})
	if len(messages) != 1 || messages[0].Payload.LiveRunBatchFinished == nil || messages[0].Payload.LiveRunBatchFinished.FinalAnswerPreview == nil {
		t.Fatalf("projected messages = %+v", messages)
	}
	preview := messages[0].Payload.LiveRunBatchFinished.FinalAnswerPreview
	want, truncated := lifecyclecontract.LimitMarkdownSummary(content)
	if !truncated || preview.Markdown != want || !utf8.ValidString(preview.Markdown) {
		t.Fatalf("preview = %+v, want UTF-8 bounded %q", preview, want)
	}
	if preview.Truncation == nil || *preview.Truncation != clientui.TranscriptFinalAnswerPreviewTruncationByteLimit {
		t.Fatalf("preview truncation = %+v, want byte-limit fact", preview.Truncation)
	}
}

func TestLiveRunBatchFinishedRejectsMalformedRuntimeVariants(t *testing.T) {
	tests := []runtime.Event{
		{Kind: runtime.EventLiveRunBatchFinished},
		{
			Kind: runtime.EventLiveRunBatchFinished,
			LiveRunResult: &runtime.LiveRunResult{
				ResultKind: runtime.LiveRunResultRuntimeFailure,
				FinishedAt: time.Date(2026, time.July, 19, 20, 0, 0, 0, time.UTC),
			},
		},
	}
	for _, event := range tests {
		t.Run("invalid", func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("malformed live-run batch event did not fail fast")
				}
			}()
			_ = TranscriptMessagesFromRuntimeEvent(event)
		})
	}
}

func TestTranscriptHydrationDoesNotReconstructLiveRunBatchFinished(t *testing.T) {
	hydration := TranscriptHydrationFromSnapshot(runtime.TranscriptHydrationSnapshot{
		CommittedRows: []runtime.TranscriptCommittedRowFact{{
			StepID:     transcriptProjectionStepID,
			Visibility: transcript.EntryVisibilityOngoing,
			Integrity:  transcript.RowIntegrityValid,
			Kind:       runtime.TranscriptCommittedRowFactAssistant,
			Assistant: &runtime.TranscriptAssistantRowFact{
				Text: "persisted final answer",
			},
		}},
	})
	if _, found := reflect.TypeOf(hydration).FieldByName("LiveRunBatchFinished"); found {
		t.Fatal("hydration carries a completed live-run batch")
	}
	if len(hydration.CommittedRows) != 1 {
		t.Fatalf("hydration committed rows = %+v, want persisted row only", hydration.CommittedRows)
	}
}

func liveRunBatchExclusionReasonPtr(reason clientui.LiveRunBatchExclusionReason) *clientui.LiveRunBatchExclusionReason {
	return &reason
}
