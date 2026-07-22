package workflowrunner

import (
	"context"

	"core/server/workflow"
	"core/server/workflowattention"
)

type recordingInterruptedRunFinalizer struct {
	transitions     []workflowattention.TransitionResult
	interruptedRuns []workflow.RunID
}

func (f *recordingInterruptedRunFinalizer) FinalizeTransition(_ context.Context, result workflowattention.TransitionResult) {
	f.transitions = append(f.transitions, result)
}

func (f *recordingInterruptedRunFinalizer) PublishPendingInterruptedRun(_ context.Context, runID workflow.RunID) {
	f.interruptedRuns = append(f.interruptedRuns, runID)
}
