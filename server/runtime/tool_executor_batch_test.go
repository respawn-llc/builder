package runtime

import (
	"encoding/json"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestPrepareExecutorToolCallsAssignsAskQuestionBatchMetadata(t *testing.T) {
	engine := &Engine{registry: tools.NewRegistry(tools.HandlerRegistration{
		ID:      toolspec.ToolAskQuestion,
		Handler: tools.NewAskQuestionTool(tools.NewAskQuestionBroker(), func() bool { return true }),
	})}
	calls := []llm.ToolCall{
		{ID: "ask-1", Name: string(toolspec.ToolAskQuestion), Input: askQuestionInput(t, "one?")},
		{ID: "shell-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"true"}`)},
		{ID: "ask-2", Name: string(toolspec.ToolAskQuestion), Input: askQuestionInput(t, "two?")},
	}

	prepared, err := prepareExecutorToolCalls(engine, "step-1", "run-1", true, calls)
	if err != nil {
		t.Fatalf("prepare executor tool calls: %v", err)
	}

	first := prepared[0].askQuestionBatch
	second := prepared[2].askQuestionBatch
	if first == nil || second == nil {
		t.Fatalf("ask batch metadata missing: first=%+v second=%+v", first, second)
	}
	if first.BatchID == "" || first.BatchID != second.BatchID {
		t.Fatalf("batch ids = %q, %q", first.BatchID, second.BatchID)
	}
	if first.Origin != tools.AskQuestionOriginModelTool || first.RunID != "run-1" || first.StepID != "step-1" {
		t.Fatalf("first metadata = %+v", first)
	}
	if got := first.BatchPromptIDs; len(got) != 2 || got[0] != "ask-1" || got[1] != "ask-2" {
		t.Fatalf("batch prompt ids = %+v", got)
	}
	if first.CandidateOrdinal != 0 || second.CandidateOrdinal != 1 || second.PreparedPromptCount != 2 {
		t.Fatalf("ordinals/count = %+v / %+v", first, second)
	}
	if prepared[1].askQuestionBatch != nil {
		t.Fatalf("non-ask tool received batch metadata: %+v", prepared[1].askQuestionBatch)
	}
}

func TestPrepareExecutorToolCallsExcludesInvalidAndDisabledAsks(t *testing.T) {
	enabledEngine := &Engine{registry: tools.NewRegistry(tools.HandlerRegistration{
		ID:      toolspec.ToolAskQuestion,
		Handler: tools.NewAskQuestionTool(tools.NewAskQuestionBroker(), func() bool { return true }),
	})}
	calls := []llm.ToolCall{
		{ID: "invalid-first", Name: string(toolspec.ToolAskQuestion), Input: json.RawMessage(`{"suggestions":["A"]}`)},
		{ID: "valid-middle", Name: string(toolspec.ToolAskQuestion), Input: askQuestionInput(t, "middle?")},
		{ID: "invalid-later", Name: string(toolspec.ToolAskQuestion), Input: json.RawMessage(`{"question":"bad","approval":true}`)},
		{ID: "valid-later", Name: string(toolspec.ToolAskQuestion), Input: askQuestionInput(t, "later?")},
	}

	prepared, err := prepareExecutorToolCalls(enabledEngine, "step-1", "run-1", true, calls)
	if err != nil {
		t.Fatalf("prepare enabled executor tool calls: %v", err)
	}
	if prepared[0].askQuestionBatch != nil || prepared[2].askQuestionBatch != nil {
		t.Fatalf("invalid asks received metadata: first=%+v later=%+v", prepared[0].askQuestionBatch, prepared[2].askQuestionBatch)
	}
	if got := prepared[1].askQuestionBatch.BatchPromptIDs; len(got) != 2 || got[0] != "valid-middle" || got[1] != "valid-later" {
		t.Fatalf("valid batch prompt ids = %+v", got)
	}

	disabledEngine := &Engine{registry: tools.NewRegistry(tools.HandlerRegistration{
		ID:      toolspec.ToolAskQuestion,
		Handler: tools.NewAskQuestionTool(tools.NewAskQuestionBroker(), func() bool { return false }),
	})}
	disabled, err := prepareExecutorToolCalls(disabledEngine, "step-1", "run-1", true, calls)
	if err != nil {
		t.Fatalf("prepare disabled executor tool calls: %v", err)
	}
	for index, call := range disabled {
		if call.askQuestionBatch != nil {
			t.Fatalf("disabled ask %d received metadata: %+v", index, call.askQuestionBatch)
		}
	}
}

func TestPrepareExecutorToolCallsRejectsMissingProviderCallID(t *testing.T) {
	prepared, err := prepareExecutorToolCalls(&Engine{}, "step-1", "run-1", true, []llm.ToolCall{{
		Name: string(toolspec.ToolExecCommand),
	}})
	if err == nil {
		t.Fatal("prepare executor tool calls accepted missing provider call id")
	}
	if !errors.Is(err, ErrMissingProviderToolCallID) {
		t.Fatalf("error = %v, want ErrMissingProviderToolCallID", err)
	}
	if len(prepared) != 0 {
		t.Fatalf("prepared calls = %+v, want none", prepared)
	}
}

func TestToolResultWithTranscriptPresentationKeepsTypedInput(t *testing.T) {
	nonZeroExitCode := 7
	tests := []struct {
		name                  string
		call                  llm.ToolCall
		delta                 *transcript.ToolResultPresentationDelta
		wantCommand           string
		wantPatch             bool
		wantRaw               bool
		wantTruncated         bool
		wantMovedToBackground bool
		wantShellExitCode     *int
	}{
		{
			name:        "shell command",
			call:        llm.ToolCall{ID: "0f63b1c2-6b29-4dc0-9b0f-405a92a23901", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			wantCommand: "pwd",
		},
		{
			name:        "raw shell command",
			call:        llm.ToolCall{ID: "0f63b1c2-6b29-4dc0-9b0f-405a92a23902", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"printf raw","raw":true}`)},
			delta:       &transcript.ToolResultPresentationDelta{RawOutputRequested: true},
			wantCommand: "printf raw",
			wantRaw:     true,
		},
		{
			name:          "truncated shell command",
			call:          llm.ToolCall{ID: "0f63b1c2-6b29-4dc0-9b0f-405a92a23903", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"cat large.log"}`)},
			delta:         &transcript.ToolResultPresentationDelta{OutputTruncated: true},
			wantCommand:   "cat large.log",
			wantTruncated: true,
		},
		{
			name:                  "backgrounded shell command",
			call:                  llm.ToolCall{ID: "0f63b1c2-6b29-4dc0-9b0f-405a92a23907", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"sleep 20"}`)},
			delta:                 &transcript.ToolResultPresentationDelta{MovedToBackground: true},
			wantCommand:           "sleep 20",
			wantMovedToBackground: true,
		},
		{
			name:              "failed shell command",
			call:              llm.ToolCall{ID: "0f63b1c2-6b29-4dc0-9b0f-405a92a23908", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"exit 7"}`)},
			delta:             &transcript.ToolResultPresentationDelta{ShellExitCode: &nonZeroExitCode},
			wantCommand:       "exit 7",
			wantShellExitCode: &nonZeroExitCode,
		},
		{
			name: "patch input",
			call: llm.ToolCall{
				ID:          "0f63b1c2-6b29-4dc0-9b0f-405a92a23904",
				Name:        string(toolspec.ToolPatch),
				Custom:      true,
				CustomInput: "*** Begin Patch\n*** Add File: a.txt\n+hello\n*** End Patch",
			},
			wantPatch: true,
		},
		{
			name: "edit input",
			call: llm.ToolCall{
				ID:    "0f63b1c2-6b29-4dc0-9b0f-405a92a23906",
				Name:  string(toolspec.ToolEdit),
				Input: json.RawMessage(`{"path":"a.txt","old_string":"hello","new_string":"goodbye"}`),
			},
			wantPatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toolResultWithTranscriptPresentation(tools.Result{
				CallID:            tt.call.ID,
				Name:              toolspec.ID(tt.call.Name),
				IsError:           true,
				Summary:           "failed",
				PresentationDelta: tt.delta,
			}, tt.call, t.TempDir())

			if result.Presentation == nil {
				t.Fatal("tool result presentation is nil")
			}
			if result.PresentationDelta != nil {
				t.Fatalf("tool result presentation delta was not consumed: %+v", result.PresentationDelta)
			}
			if tt.wantCommand != "" && result.Presentation.Command != tt.wantCommand {
				t.Fatalf("presentation command = %q, want %q", result.Presentation.Command, tt.wantCommand)
			}
			if tt.wantPatch && result.Presentation.PatchRender == nil {
				t.Fatal("patch result presentation has no structured patch")
			}
			if result.Presentation.RawOutputRequested != tt.wantRaw {
				t.Fatalf("raw output requested = %t, want %t", result.Presentation.RawOutputRequested, tt.wantRaw)
			}
			if result.Presentation.OutputTruncated != tt.wantTruncated {
				t.Fatalf("output truncated = %t, want %t", result.Presentation.OutputTruncated, tt.wantTruncated)
			}
			if result.Presentation.MovedToBackground != tt.wantMovedToBackground {
				t.Fatalf("backgrounded = %t, want %t", result.Presentation.MovedToBackground, tt.wantMovedToBackground)
			}
			if !optionalIntEqual(result.Presentation.ShellExitCode, tt.wantShellExitCode) {
				t.Fatalf("shell exit code = %v, want %v", result.Presentation.ShellExitCode, tt.wantShellExitCode)
			}
		})
	}
}

func optionalIntEqual(left, right *int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func TestLiveToolCompletionBoundaryRejectsHandlerFinalizedPresentation(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected handler-owned finalized presentation to violate the finalization invariant")
		}
	}()

	call := llm.ToolCall{
		ID:    "0f63b1c2-6b29-4dc0-9b0f-405a92a23905",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"command":"pwd"}`),
	}
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	if err := engine.steer("step", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{call},
	}})); err != nil {
		t.Fatalf("persist assistant tool call: %v", err)
	}
	engine.finalizeLiveToolCompletion(tools.Result{
		CallID:       call.ID,
		Name:         toolspec.ToolExecCommand,
		Presentation: &transcript.ToolCallMeta{Command: "handler override"},
	})
}

func askQuestionInput(t *testing.T, question string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(map[string]string{"question": question})
	if err != nil {
		t.Fatalf("marshal ask question input: %v", err)
	}
	return encoded
}
