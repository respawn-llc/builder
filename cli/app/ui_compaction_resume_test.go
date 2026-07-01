package app

import (
	"strings"
	"testing"
	"time"

	"core/cli/app/internal/runtimeattach"
	"core/server/llm"
	"core/server/runtime"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCompactDoneResumesQueuedSteeringAsNewTurn(t *testing.T) {
	client := &requestCaptureFakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "resumed"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	projectedEvents := make(chan clientui.Event, 32)
	_, eng := newAppRuntimeEngine(t, client, runtime.Config{
		Model: "gpt-5",
		OnEvent: func(evt runtime.Event) {
			projectedEvents <- projectRuntimeEvent(evt)
		},
	})

	m := NewProjectedUIModel(newUIRuntimeClient(eng), projectedEvents, make(chan askEvent)).(*uiModel)
	m.setBusy(true)
	m.setCompacting(true)
	m.activity = uiActivityRunning
	m.input = "steered message"

	next, createCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	updated = applyFirstInjectedQueueCreateDoneForTest(t, updated, createCmd)
	if len(updated.pendingInjected) != 1 || updated.pendingInjected[0].Text != "steered message" {
		t.Fatalf("expected pending injected steering before compaction completes, got %+v", updated.pendingInjected)
	}

	next, cmd := updated.Update(compactDoneMsg{})
	updated = next.(*uiModel)
	updated, cmd = applyQueuedRuntimeWorkCheckForTest(t, updated, cmd)
	if cmd == nil {
		t.Fatal("expected compaction completion to resume queued steering")
	}
	if !updated.isBusy() {
		t.Fatal("expected resumed steering submission to set busy=true")
	}
	if updated.isCompacting() {
		t.Fatal("expected compaction flag cleared before resumed steering turn")
	}

	msgs := collectCmdMessages(t, cmd)
	var submitDone submitDoneMsg
	foundSubmitDone := false
	for _, msg := range msgs {
		if typed, ok := msg.(submitDoneMsg); ok {
			submitDone = typed
			foundSubmitDone = true
		}
	}
	if !foundSubmitDone {
		t.Fatalf("expected resumed steering submission to yield submitDoneMsg, got %+v", msgs)
	}
	if submitDone.err != nil {
		t.Fatalf("submit done err = %v", submitDone.err)
	}

	requests := client.Requests()
	if len(requests) != 1 {
		t.Fatalf("expected one resumed steering model request, got %d", len(requests))
	}
	hasSteeredUser := false
	for _, message := range llm.MessagesFromItems(requests[0].Items) {
		if message.Role == llm.RoleUser && message.Content == "steered message" {
			hasSteeredUser = true
			break
		}
	}
	if !hasSteeredUser {
		t.Fatalf("expected resumed request to include steered message, got %+v", llm.MessagesFromItems(requests[0].Items))
	}

	deadline := time.Now().Add(2 * time.Second)
	submitApplied := false
	for time.Now().Before(deadline) {
		select {
		case evt := <-projectedEvents:
			next, _ = updated.Update(runtimeEventMsg{event: evt})
			updated = next.(*uiModel)
		default:
			if !submitApplied {
				next, _ = updated.Update(submitDone)
				updated = next.(*uiModel)
				submitApplied = true
				if updated.isBusy() {
					t.Fatal("expected resumed steering turn to finish idle")
				}
				if len(updated.pendingInjected) == 0 {
					return
				}
				continue
			}
			if len(updated.pendingInjected) != 0 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			return
		}
	}
	t.Fatal("timed out waiting for resumed steering flush")
}

func TestInterruptedResumedQueuedSteeringRestoresInput(t *testing.T) {
	_, eng := newAppRuntimeEngine(t, &requestCaptureFakeClient{}, runtime.Config{})

	m := newProjectedEngineUIModel(eng)
	m.setBusy(true)
	m.setCompacting(true)
	m.activity = uiActivityRunning
	m.input = "steered message"

	next, createCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	updated = applyFirstInjectedQueueCreateDoneForTest(t, updated, createCmd)
	next, cmd := updated.Update(compactDoneMsg{})
	updated = next.(*uiModel)
	updated, cmd = applyQueuedRuntimeWorkCheckForTest(t, updated, cmd)
	if cmd == nil {
		t.Fatal("expected compaction completion to resume queued steering")
	}
	if !updated.isBusy() {
		t.Fatal("expected resumed steering submission to set busy=true")
	}

	next, interruptCmd := updated.Update(submitDoneMsg{err: runtimeattach.ErrSubmissionInterrupted})
	updated = next.(*uiModel)
	if interruptCmd == nil {
		t.Fatal("expected queued runtime cleanup command after interrupted resumed steering")
	}
	_ = collectCmdMessages(t, interruptCmd)
	if updated.isBusy() {
		t.Fatal("expected busy=false after interrupted resumed steering")
	}
	if updated.activity != uiActivityInterrupted {
		t.Fatalf("expected interrupted activity, got %v", updated.activity)
	}
	if updated.input != "steered message" {
		t.Fatalf("expected interrupted resumed steering restored into input, got %q", updated.input)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected pending injected cleared after restore, got %+v", updated.pendingInjected)
	}
	hasWork, err := updated.runtimeClient().HasQueuedUserWork()
	if err != nil {
		t.Fatalf("check queued user work: %v", err)
	}
	if hasWork {
		t.Fatal("expected interrupted resumed steering cleanup to discard runtime queued work")
	}
	plain := stripANSIAndTrimRight(updated.View())
	if count := strings.Count(strings.ToLower(plain), "interrupted"); count != 1 {
		t.Fatalf("expected interruption rendered only as status state, got count=%d view=%q", count, plain)
	}
}
