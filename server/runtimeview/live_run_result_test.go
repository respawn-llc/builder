package runtimeview

import (
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/textutil"
)

func TestTranscriptProjectsLiveRunResultWithoutRuntimeIDs(t *testing.T) {
	startedAt := time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Second)
	tests := []struct {
		name   string
		result runtime.LiveRunResult
		check  func(*testing.T, clientui.TranscriptLiveRunResult)
	}{
		{
			name: "final answer",
			result: runtime.LiveRunResult{
				Status:           runtime.RunStatusCompleted,
				ResultKind:       runtime.LiveRunResultAssistantFinalAnswer,
				WorkPerformed:    true,
				AssistantMessage: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
				StartedAt:        startedAt,
				FinishedAt:       finishedAt,
			},
			check: func(t *testing.T, result clientui.TranscriptLiveRunResult) {
				if result.FinalAnswer == nil || *result.FinalAnswer != "done" || !result.WorkPerformed {
					t.Fatalf("result = %+v", result)
				}
			},
		},
		{
			name: "failure",
			result: runtime.LiveRunResult{
				Status:     runtime.RunStatusFailed,
				ResultKind: runtime.LiveRunResultNoFinalAnswer,
				Error:      errors.New("provider failed"),
				StartedAt:  startedAt,
				FinishedAt: finishedAt,
			},
			check: func(t *testing.T, result clientui.TranscriptLiveRunResult) {
				if result.Failure == nil || *result.Failure != "provider failed" {
					t.Fatalf("result = %+v", result)
				}
			},
		},
		{
			name: "failure after final answer",
			result: runtime.LiveRunResult{
				Status:           runtime.RunStatusFailed,
				ResultKind:       runtime.LiveRunResultAssistantFinalAnswer,
				AssistantMessage: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("partial final")},
				Error:            errors.New("follow-up failed"),
				StartedAt:        startedAt,
				FinishedAt:       finishedAt,
			},
			check: func(t *testing.T, result clientui.TranscriptLiveRunResult) {
				if result.FinalAnswer == nil || *result.FinalAnswer != "partial final" {
					t.Fatalf("final answer = %+v", result.FinalAnswer)
				}
				if result.Failure == nil || *result.Failure != "follow-up failed" {
					t.Fatalf("failure = %+v", result.Failure)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
				Kind:          runtime.EventLiveRunFinished,
				LiveRunResult: &test.result,
			})
			if len(messages) != 1 || messages[0].Kind() != clientui.TranscriptMessageLiveRunFinished {
				t.Fatalf("messages = %+v", messages)
			}
			projected := transcriptPayload[clientui.TranscriptLiveRunResult](t, messages[0])
			if err := projected.Validate(); err != nil {
				t.Fatalf("validate: %v", err)
			}
			test.check(t, projected)
		})
	}
}

func TestTranscriptProjectsMissingAssistantFinalTextAsNoFinalResult(t *testing.T) {
	startedAt := time.Date(2026, time.July, 22, 21, 25, 14, 0, time.UTC)
	finishedAt := startedAt.Add(time.Second)

	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind: runtime.EventLiveRunFinished,
		LiveRunResult: &runtime.LiveRunResult{
			Status:     runtime.RunStatusCompleted,
			ResultKind: runtime.LiveRunResultAssistantFinalAnswer,
			StartedAt:  startedAt,
			FinishedAt: finishedAt,
		},
	})

	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want one live-run completion", messages)
	}
	projected := transcriptPayload[clientui.TranscriptLiveRunResult](t, messages[0])
	if projected.ResultKind != clientui.LiveRunResultNoFinalAnswer || projected.FinalAnswer != nil {
		t.Fatalf("projected live run = %+v, want no-final result without fabricated answer", projected)
	}
}
