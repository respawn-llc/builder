package serverapi

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"core/shared/clientui"
)

func TestRuntimeGoalShowResponseEncodesAbsentGoalAsNull(t *testing.T) {
	wire, err := json.Marshal(RuntimeGoalShowResponse{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	goal, exists := fields["goal"]
	if !exists || !bytes.Equal(goal, []byte("null")) {
		t.Fatalf("goal field = %s, exists=%t; want null", goal, exists)
	}
}

func TestRuntimeGoalShowResponseRejectsUnknownGoalStatus(t *testing.T) {
	now := time.Now().UTC()
	err := (RuntimeGoalShowResponse{Goal: &clientui.Goal{
		ID:        "goal-1",
		Objective: "ship",
		Status:    clientui.RuntimeGoalStatus("unknown"),
		CreatedAt: now,
		UpdatedAt: now,
	}}).Validate()
	if err == nil {
		t.Fatal("RuntimeGoalShowResponse accepted an unknown Goal status")
	}
}

func TestRuntimeSubmitUserShellCommandRejectsBlankCommand(t *testing.T) {
	err := (RuntimeSubmitUserShellCommandRequest{
		SessionID: "session-1",
		Command:   " \t\n",
	}).Validate()
	if err == nil {
		t.Fatal("RuntimeSubmitUserShellCommandRequest accepted a blank command")
	}
}
