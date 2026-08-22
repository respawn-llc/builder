package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestForkedSessionRecoversCompletedTriggerHandoff(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewHandoffTestEngine(t, store, &fakeClient{}, Config{})
	if err := steerTestActiveStep(engine, "seed", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("first")}})); err != nil {
		t.Fatalf("persist first user message: %v", err)
	}
	handoffCall := persistSuccessfulTriggerHandoff(t, engine, "fork-handoff")
	if err := steerTestActiveStep(engine, "anchor", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("fork anchor")}})); err != nil {
		t.Fatalf("persist fork anchor: %v", err)
	}

	forkedStore, _, err := session.ForkAtUserMessage(
		mustMaterializeTestEventLog(t, store),
		userMessageSeqAt(t, store, 2),
		"fork",
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("fork handoff session: %v", err)
	}
	forked := mustNewHandoffTestEngine(t, forkedStore, &fakeClient{}, Config{})
	if pending := forked.handoffRuntimeState().RequestSnapshot(); pending == nil {
		t.Fatal("fork lost completed trigger-handoff recovery")
	}
	assertBoundedTriggerHandoffCompletion(t, forkedStore, handoffCall.ID, false)
}

func TestTriggerHandoffRejectsUnavailableAdmissionWithoutQueueing(t *testing.T) {
	engine := mustNewHandoffTestEngine(t, mustCreateTestSession(t), &fakeClient{}, Config{})
	tests := []struct {
		name  string
		setup func(*Engine)
		want  error
	}{
		{
			name:  "before reminder",
			setup: func(*Engine) {},
			want:  errHandoffTooEarly,
		},
		{
			name: "auto compaction disabled",
			setup: func(engine *Engine) {
				engine.compactionRuntimeState().SetSoonReminderIssued(true)
				engine.SetAutoCompactionEnabled(false)
			},
			want: errHandoffDisabledByUser,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.setup(engine)
			_, _, err := engine.TriggerHandoff(
				context.Background(),
				"step",
				llm.ToolCall{ID: "handoff", Name: string(toolspec.ToolTriggerHandoff)},
				"summary",
				"future",
			)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("trigger handoff error = %v, want %v", err, testCase.want)
			}
			if pending := engine.handoffRuntimeState().RequestSnapshot(); pending != nil {
				t.Fatalf("rejected trigger-handoff queued request: %+v", pending)
			}
		})
	}
}

func assertBoundedTriggerHandoffCompletion(t *testing.T, store *session.Store, callID string, wantError bool) {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded handoff records: %v", err)
	}
	for _, record := range window.Records {
		completion, ok := mustSessionEventPayload(record).(session.ToolCompletionRecord)
		if ok && completion.CallID == callID && completion.Name == string(toolspec.ToolTriggerHandoff) && completion.IsError == wantError {
			return
		}
	}
	t.Fatalf("bounded records contain no typed trigger-handoff completion: %+v", window.Records)
}
