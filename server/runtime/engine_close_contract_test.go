package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

func TestCloseRejectsUserTurnsAndSteeringWithoutNewWork(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	if err := engine.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}
	if _, err := engine.SubmitUserMessage(context.Background(), "after-close"); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("submit after close error = %v, want ErrEngineClosed", err)
	}
	if err := engine.steerRuntime(steerMessagesWithPersistenceIntent(steeringMessageEventDefault,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("after-close")}},
	)); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("steer after close error = %v, want ErrEngineClosed", err)
	}
	if calls := len(client.calls); calls != 0 {
		t.Fatalf("provider dispatches after close = %d, want zero", calls)
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(8)
	if err != nil {
		t.Fatalf("read bounded records after close: %v", err)
	}
	if len(window.Records) != 0 {
		t.Fatalf("records persisted after close = %+v, want none", window.Records)
	}
}
