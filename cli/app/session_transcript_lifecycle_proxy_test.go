package app

import (
	"encoding/json"
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

	proxy.AcceptTranscript(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessagePromptPending,
		Payload: clientui.TranscriptPayload{
			PromptPending: &prompt,
		},
	})

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
	hydration.Payload.Hydration.PendingPrompts = []clientui.TranscriptPrompt{question, approval}

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
