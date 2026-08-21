package app

import (
	"testing"

	"core/shared/clientui"
)

func TestRuntimeActivityOwnsCompactionStatus(t *testing.T) {
	for _, kind := range []clientui.RuntimeActivityActiveKind{
		clientui.RuntimeActivityActiveKindCompaction,
		clientui.RuntimeActivityActiveKindPreSubmitCompaction,
	} {
		t.Run(string(kind), func(t *testing.T) {
			model := newProjectedStaticUIModel()

			if err := model.applyRuntimeActivityProjection(clientui.RuntimeActivity{
				State: clientui.RuntimeActivityRunning,
				ActiveStep: &clientui.RuntimeActiveStep{
					RunID:      ongoingTestRunID(),
					StepID:     ongoingTestStepID(),
					ActiveKind: kind,
				},
			}); err != nil {
				t.Fatalf("apply running compaction activity: %v", err)
			}

			if !model.isCompacting() ||
				model.statusLineLabel() == "" ||
				!model.statusLineSpinning() {
				t.Fatalf(
					"running compaction status = compacting %t label %q spinning %t",
					model.isCompacting(),
					model.statusLineLabel(),
					model.statusLineSpinning(),
				)
			}

			if err := model.applyRuntimeActivityProjection(clientui.RuntimeActivity{
				State:          clientui.RuntimeActivityRegisteredIdle,
				QueueAccepting: true,
			}); err != nil {
				t.Fatalf("apply idle activity: %v", err)
			}
			if model.isCompacting() || model.statusLineLabel() != "" || model.statusLineSpinning() {
				t.Fatalf(
					"idle status = compacting %t label %q spinning %t",
					model.isCompacting(),
					model.statusLineLabel(),
					model.statusLineSpinning(),
				)
			}
		})
	}
}

func TestTranscriptCompactionEventDoesNotMakeIdleRuntimeActive(t *testing.T) {
	model := newProjectedStaticUIModel()

	model.applyAdmittedTranscriptMessageState(clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(clientui.TranscriptCompactionStatus{
		StepID: ongoingTestStepID(),
		State:  clientui.CompactionStarted,
		Mode:   "auto",
		Count:  1,
	})), runtimeTupleMergeResult{})

	if model.isCompacting() || model.statusLineSpinning() {
		t.Fatalf(
			"idle runtime became active from transcript event: compacting %t spinning %t",
			model.isCompacting(),
			model.statusLineSpinning(),
		)
	}
}
