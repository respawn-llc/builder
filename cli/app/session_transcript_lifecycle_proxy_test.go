package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/internal/testharness/pty/appfixture"
	"core/shared/clientui"
	"core/shared/lifecyclecontract"
)

func TestClientLifecycleProxyEmitsInputRequiredWhenPendingPromptIsObserved(t *testing.T) {
	proxy, recordPath := newRecordingClientLifecycleProxy(t, true)
	prompt := testQuestionPrompt(
		"ask-observed",
		"**Choose the next action before returning to the ongoing surface.**",
		"continue",
	)

	proxy.AcceptTranscript(clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(prompt)))

	event := appfixture.DecodeLifecycleHookEvents(
		t,
		appfixture.WaitForLifecycleHookRecords(t, recordPath, 1),
	)[0]
	var details lifecyclecontract.InputRequiredDetails
	if err := json.Unmarshal(event.Details, &details); err != nil {
		t.Fatalf("decode lifecycle input details: %v", err)
	}
	if event.Category != lifecyclecontract.CategoryInputRequired ||
		!event.OccurredAt.Equal(prompt.CreatedAt) ||
		!event.Focused ||
		details.Kind != lifecyclecontract.InputKindQuestion ||
		details.Summary != prompt.Question {
		t.Fatalf("observed pending prompt lifecycle event = %+v details=%+v", event, details)
	}
}

func TestClientLifecycleProxyEmitsInputRequiredForEachHydratedPendingPrompt(t *testing.T) {
	proxy, recordPath := newRecordingClientLifecycleProxy(t, false)
	question := testQuestionPrompt("ask-hydrated", "Choose a recovery path.", "retry")
	approval := testApprovalPrompt(
		"approval-hydrated",
		"Allow the requested operation?",
		clientui.ApprovalDecisionAllowOnce,
		clientui.ApprovalDecisionDeny,
	)
	hydration := ongoingHydrationMessage(1)
	hydrationPayload := hydration.Payload().(clientui.TranscriptHydration)
	hydrationPayload.PendingPrompts = []clientui.TranscriptPrompt{question, approval}
	hydration = clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(hydrationPayload))

	proxy.AcceptTranscript(hydration)

	events := appfixture.DecodeLifecycleHookEvents(
		t,
		appfixture.WaitForLifecycleHookRecords(t, recordPath, 2),
	)
	summaries := make(map[lifecyclecontract.InputKind]string, len(events))
	for index, event := range events {
		if event.Category != lifecyclecontract.CategoryInputRequired {
			t.Fatalf("hydrated lifecycle event %d category = %q", index, event.Category)
		}
		var details lifecyclecontract.InputRequiredDetails
		if err := json.Unmarshal(event.Details, &details); err != nil {
			t.Fatalf("decode hydrated lifecycle input details %d: %v", index, err)
		}
		summaries[details.Kind] = details.Summary
	}
	if summaries[lifecyclecontract.InputKindQuestion] != question.Question ||
		summaries[lifecyclecontract.InputKindApproval] != approval.Question {
		t.Fatalf("hydrated input-required summaries = %+v", summaries)
	}
}

func TestTurnQueueHooksEmitFocusedTaskCompletionWithoutNotificationEligibility(t *testing.T) {
	proxy, recordPath := newRecordingClientLifecycleProxy(t, true)
	ringer := &countRinger{}
	hooks := newTurnQueueHooks(
		newBellHooks(ringer, nil, func() bool { return true }),
		proxy,
	)
	message := testAssistantFinalLiveRunMessage(2, "completed without notification eligibility", true)

	hooks.OnTranscriptMessage(message)

	if _, err := os.Stat(recordPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completion hook record before queue drain: %v", err)
	}
	hooks.OnTurnQueueDrained()

	event := appfixture.DecodeLifecycleHookEvents(
		t,
		appfixture.WaitForLifecycleHookRecords(t, recordPath, 1),
	)[0]
	var details lifecyclecontract.TaskCompleteDetails
	if err := json.Unmarshal(event.Details, &details); err != nil {
		t.Fatalf("decode lifecycle completion details: %v", err)
	}
	result := message.Payload().(clientui.TranscriptLiveRunResult)
	if event.Category != lifecyclecontract.CategoryTaskComplete ||
		!event.OccurredAt.Equal(result.FinishedAt) ||
		!event.Focused ||
		details.FinalAnswer != *result.FinalAnswer ||
		details.WorkPerformed != result.WorkPerformed {
		t.Fatalf("turn-queue completion lifecycle event = %+v details=%+v", event, details)
	}
	if ringer.total() != 0 {
		t.Fatalf("focused zero-tool completion emitted %d terminal notifications", ringer.total())
	}
}

func newRecordingClientLifecycleProxy(
	t testing.TB,
	focused bool,
) (*clientLifecycleProxy, string) {
	t.Helper()
	recordPath := filepath.Join(t.TempDir(), "lifecycle.jsonl")
	command, err := lifecycleHookProductRecorderCommand(
		recordPath,
		appfixture.LifecycleHookBehaviorSuccess,
		nil,
	)
	if err != nil {
		t.Fatalf("lifecycle recorder command: %v", err)
	}
	sessionID := ongoingTestSessionID()
	proxy := newClientLifecycleProxy(
		t.Context(),
		command,
		lifecyclecontract.Context{SessionID: &sessionID},
		func() bool { return focused },
	)
	t.Cleanup(proxy.Close)
	return proxy, recordPath
}
