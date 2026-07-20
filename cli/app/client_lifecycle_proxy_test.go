package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/lifecyclecontract"
	"core/shared/runtimeids"
)

func TestClientLifecycleProxyFiresFiveEventsWithoutOrderingGuarantee(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "events.jsonl")
	t.Setenv("KENT_TEST_LIFECYCLE_HELPER_MODE", "record")
	t.Setenv("KENT_TEST_LIFECYCLE_HELPER_FILE", recordPath)
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("parse session ID: %v", err)
	}
	title := "Proxy test"
	proxy := newClientLifecycleProxy(
		t.Context(),
		lifecycleTestCommand(),
		lifecyclecontract.Context{SessionID: &sessionID, SessionTitle: &title},
		func() bool { return true },
		false,
	)
	t.Cleanup(proxy.Close)

	proxy.AcceptSessionStart(lifecyclecontract.OpeningKindResumed)
	finishedAt := time.Now().UTC()
	finalAnswer := "done"
	proxy.AcceptTranscript(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageLiveRunFinished,
		Payload: clientui.TranscriptPayload{LiveRunFinished: &clientui.TranscriptLiveRunResult{
			Status:        clientui.LiveRunStatusCompleted,
			ResultKind:    clientui.LiveRunResultAssistantFinalAnswer,
			WorkPerformed: true,
			FinalAnswer:   &finalAnswer,
			StartedAt:     finishedAt.Add(-time.Second),
			FinishedAt:    finishedAt,
		}},
	})
	proxy.AcceptAttention(clientui.AttentionNotificationEvent{
		Type: clientui.AttentionNotificationEventPending,
		Pending: &clientui.AttentionNotification{
			Kind:       clientui.AttentionNotificationKindQuestion,
			OccurredAt: finishedAt,
			Question:   &clientui.AttentionNotificationQuestionState{Preview: "ready?"},
		},
	})
	stepID, err := runtimeids.ParseStepID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("parse step ID: %v", err)
	}
	proxy.AcceptTranscript(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageCompactionStatus,
		Payload: clientui.TranscriptPayload{CompactionStatus: &clientui.TranscriptCompactionStatus{
			StepID: stepID,
			State:  clientui.CompactionStarted,
			Mode:   "manual",
		}},
	})
	failure := "provider failed"
	proxy.AcceptTranscript(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageLiveRunFinished,
		Payload: clientui.TranscriptPayload{LiveRunFinished: &clientui.TranscriptLiveRunResult{
			Status:     clientui.LiveRunStatusFailed,
			ResultKind: clientui.LiveRunResultNoFinalAnswer,
			Failure:    &failure,
			StartedAt:  finishedAt,
			FinishedAt: finishedAt.Add(time.Second),
		}},
	})

	events := waitForLifecycleRecords(t, recordPath, 5)
	categories := make(map[lifecyclecontract.Category]lifecyclecontract.Event, len(events))
	for _, event := range events {
		categories[event.Category] = event
	}
	for _, category := range []lifecyclecontract.Category{
		lifecyclecontract.CategorySessionStart,
		lifecyclecontract.CategoryTaskComplete,
		lifecyclecontract.CategoryTaskError,
		lifecyclecontract.CategoryInputRequired,
		lifecyclecontract.CategoryResourceLimit,
	} {
		if _, ok := categories[category]; !ok {
			t.Fatalf("missing category %q in %+v", category, categories)
		}
	}
	complete := categories[lifecyclecontract.CategoryTaskComplete]
	details, ok := complete.Details.(map[string]any)
	if !ok || details["work_performed"] != true {
		t.Fatalf("task complete details = %#v", complete.Details)
	}
}

func TestClientLifecycleProxyReportsFailureAndCloseDoesNotWait(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
		t.Setenv("KENT_TEST_LIFECYCLE_HELPER_MODE", "fail")
		proxy := newClientLifecycleProxy(t.Context(), lifecycleTestCommand(), lifecyclecontract.Context{}, nil, true)
		t.Cleanup(proxy.Close)
		proxy.AcceptSessionStart(lifecyclecontract.OpeningKindNew)
		select {
		case issue := <-proxy.Issues():
			if issue.err == nil {
				t.Fatalf("issue = %+v", issue)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for lifecycle hook issue")
		}
	})

	t.Run("close", func(t *testing.T) {
		readyPath := filepath.Join(t.TempDir(), "ready")
		t.Setenv("KENT_TEST_LIFECYCLE_HELPER_MODE", "hang")
		t.Setenv("KENT_TEST_LIFECYCLE_HELPER_FILE", readyPath)
		proxy := newClientLifecycleProxy(t.Context(), lifecycleTestCommand(), lifecyclecontract.Context{}, nil, true)
		proxy.AcceptSessionStart(lifecyclecontract.OpeningKindNew)
		waitForFile(t, readyPath)
		started := time.Now()
		proxy.Close()
		if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
			t.Fatalf("close waited %s", elapsed)
		}
	})
}

func TestLifecycleHookIssueProducesLogAndNoticeCommand(t *testing.T) {
	logger := &testUILogger{}
	model := newProjectedStaticUIModel(WithUILogger(logger))
	result := model.reduceDispatchedEvent(uiDispatchedEventMsg{issue: &lifecycleHookIssue{
		category: lifecyclecontract.CategoryTaskComplete,
		err:      errors.New("launch failed"),
	}})
	if !result.handled || result.cmd == nil {
		t.Fatalf("result = %+v, want handled notice command", result)
	}
	if len(logger.lines) != 1 {
		t.Fatalf("log lines = %d, want 1", len(logger.lines))
	}
}

func TestLifecycleHookProcessHelper(t *testing.T) {
	mode := os.Getenv("KENT_TEST_LIFECYCLE_HELPER_MODE")
	if mode == "" {
		return
	}
	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	path := os.Getenv("KENT_TEST_LIFECYCLE_HELPER_FILE")
	switch mode {
	case "record":
		file, openErr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if openErr != nil {
			os.Exit(3)
		}
		_, writeErr := file.Write(append(payload, '\n'))
		_ = file.Close()
		if writeErr != nil {
			os.Exit(4)
		}
	case "fail":
		os.Exit(7)
	case "hang":
		if writeErr := os.WriteFile(path, []byte("ready"), 0o600); writeErr != nil {
			os.Exit(5)
		}
		select {}
	default:
		os.Exit(6)
	}
}

func lifecycleTestCommand() []string {
	return []string{os.Args[0], "-test.run=^TestLifecycleHookProcessHelper$"}
}

func waitForLifecycleRecords(t *testing.T, path string, count int) []lifecyclecontract.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		file, err := os.Open(path)
		if err == nil {
			var events []lifecyclecontract.Event
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				var event lifecyclecontract.Event
				if decodeErr := json.Unmarshal(scanner.Bytes(), &event); decodeErr != nil {
					_ = file.Close()
					t.Fatalf("decode lifecycle event: %v", decodeErr)
				}
				events = append(events, event)
			}
			_ = file.Close()
			if len(events) >= count {
				return events
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d lifecycle records", count)
	return nil
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
