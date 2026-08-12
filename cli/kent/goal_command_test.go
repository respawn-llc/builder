package main

import (
	"bytes"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestWriteGoalMutationTextUsesOnlyAppliedOrPreviewState(t *testing.T) {
	for _, test := range []struct {
		name string
		resp serverapi.RuntimeGoalMutationResponse
		want string
	}{
		{
			name: "authoritative Goal",
			resp: serverapi.RuntimeGoalMutationResponse{Goal: &serverapi.RuntimeGoal{Objective: "applied", Status: clientui.RuntimeGoalStatusPaused}},
			want: "Goal: applied\nStatus: paused\n",
		},
		{
			name: "queued preview",
			resp: serverapi.RuntimeGoalMutationResponse{Pending: &clientui.GoalPreview{Objective: "queued", Status: clientui.RuntimeGoalStatusActive}},
			want: "Goal: queued\nStatus: active\n",
		},
		{name: "acceptance only"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			writeGoalMutationText(&stdout, test.resp)
			if got := stdout.String(); got != test.want {
				t.Fatalf("output = %q, want %q", got, test.want)
			}
			if strings.Contains(stdout.String(), "No goal") {
				t.Fatalf("mutation output claimed authoritative absence: %q", stdout.String())
			}
		})
	}
}
