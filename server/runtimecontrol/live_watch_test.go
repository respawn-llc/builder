package runtimecontrol

import (
	"errors"
	"testing"

	"core/server/runtime"
	"core/shared/runtimeids"
)

func TestLiveWatchResultClassifiesTypedTerminalStates(t *testing.T) {
	id := runtimeids.NewSessionID()
	cases := []struct {
		name   string
		result runtime.LiveRunResult
		err    error
		kind   string
	}{
		{"no final", runtime.LiveRunResult{NoFinalReason: runtime.LiveRunNoFinalAnswerReasonGoalLoop}, runtime.ErrLiveRunNoFinalAnswer, "no_final_result"},
		{"interrupted", runtime.LiveRunResult{Status: runtime.RunStatusInterrupted, Error: errors.New("stop detail")}, errors.New("terminal"), "interrupted"},
		{"error", runtime.LiveRunResult{Status: runtime.RunStatusFailed, Error: errors.New("failure detail")}, errors.New("terminal"), "execution_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, err := liveWatchResult(id, "session", tc.result, tc.err)
			if err != nil || string(response.Outcome.Kind) != tc.kind {
				t.Fatalf("result = %+v, err = %v", response, err)
			}
		})
	}
}
