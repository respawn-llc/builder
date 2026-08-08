package runtime

import (
	"testing"
	"time"

	"core/server/tools"
)

func TestModelAffectingControlMutationUsesRuntimeEventAdmission(t *testing.T) {
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5", ThinkingLevel: "high"},
	)
	release := blockRuntimeEventAdmission(t, engine.runtimeEvents)
	blocked := true
	defer func() {
		if blocked {
			release()
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- engine.SetThinkingLevel("low")
	}()
	select {
	case err := <-done:
		t.Fatalf("thinking mutation bypassed Runtime Event admission: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if got := engine.ThinkingLevel(); got != "high" {
		t.Fatalf("thinking level before Runtime Event admission = %q, want high", got)
	}

	release()
	blocked = false
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("thinking mutation after Runtime Event admission: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("thinking mutation did not complete after Runtime Event admission")
	}
	if got := engine.ThinkingLevel(); got != "low" {
		t.Fatalf("thinking level after Runtime Event admission = %q, want low", got)
	}
}

func TestCommittedControlFeedbackAndStateUseOneRuntimeEvent(t *testing.T) {
	events := make(chan Event, 4)
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model: "gpt-5",
			OnEvent: func(event Event) {
				events <- event
			},
		},
	)
	release := blockRuntimeEventAdmission(t, engine.runtimeEvents)
	blocked := true
	defer func() {
		if blocked {
			release()
		}
	}()

	type result struct {
		changed bool
		enabled bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		changed, enabled, _, err := engine.SetQuestionsEnabledWithCommittedFeedback(
			false,
			func(bool, bool) string { return "questions disabled" },
		)
		done <- result{changed: changed, enabled: enabled, err: err}
	}()
	select {
	case got := <-done:
		t.Fatalf("committed control mutation bypassed Runtime Event admission: %+v", got)
	case event := <-events:
		t.Fatalf("committed feedback published before Runtime Event admission: %+v", event)
	case <-time.After(100 * time.Millisecond):
	}
	if !engine.QuestionsEnabled() {
		t.Fatal("questions setting changed before committed feedback admission")
	}

	release()
	blocked = false
	select {
	case got := <-done:
		if got.err != nil || !got.changed || got.enabled {
			t.Fatalf("committed control mutation result = %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("committed control mutation did not complete after Runtime Event admission")
	}
	if engine.QuestionsEnabled() {
		t.Fatal("questions setting did not change after committed feedback")
	}
}
