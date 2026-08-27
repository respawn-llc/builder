package app

import (
	"bytes"
	"errors"
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestNativeProgressEligibilityUsesOnlyApprovedSources(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*uiModel)
		want  bool
	}{
		{
			name: "compaction",
			setup: func(m *uiModel) {
				m.runtimeActivityProjection = clientui.RuntimeActivity{
					State: clientui.RuntimeActivityRunning,
					ActiveStep: &clientui.RuntimeActiveStep{
						ActiveKind: clientui.RuntimeActivityActiveKindCompaction,
					},
					Reviewer: clientui.ReviewerActivityInactive,
				}
			},
			want: true,
		},
		{
			name: "Reviewer invocation",
			setup: func(m *uiModel) {
				m.runtimeActivityProjection.Reviewer = clientui.ReviewerActivityInvoking
			},
			want: true,
		},
		{
			name: "detail transcript page",
			setup: func(m *uiModel) {
				m.pendingDetailTranscript = &uiPendingDetailTranscriptRequest{}
			},
			want: true,
		},
		{
			name: "worktree create",
			setup: func(m *uiModel) {
				m.worktrees.create.submitting = true
			},
			want: true,
		},
		{
			name: "worktree delete",
			setup: func(m *uiModel) {
				m.worktrees.deleteConfirm.submitting = true
			},
			want: true,
		},
		{
			name: "main Agent Turn",
			setup: func(m *uiModel) {
				m.runtimeActivityProjection = clientui.RuntimeActivity{
					State: clientui.RuntimeActivityRunning,
					ActiveStep: &clientui.RuntimeActiveStep{
						ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
					},
					Reviewer: clientui.ReviewerActivityInactive,
				}
			},
		},
		{
			name: "Reviewer addressing feedback",
			setup: func(m *uiModel) {
				m.runtimeActivityProjection.Reviewer = clientui.ReviewerActivityAddressingFeedback
			},
		},
		{
			name: "worktree list",
			setup: func(m *uiModel) {
				m.worktrees.listPending = true
			},
		},
		{
			name: "worktree target lookup",
			setup: func(m *uiModel) {
				m.worktrees.deleteTargetResolutionPending = true
			},
		},
		{
			name: "worktree switch scheduling",
			setup: func(m *uiModel) {
				m.worktrees.create.resolving = true
			},
		},
		{
			name: "background process loading",
			setup: func(m *uiModel) {
				m.processList.loading = true
			},
		},
		{
			name: "final answer lookup",
			setup: func(m *uiModel) {
				m.finalAnswerOperation = &uiFinalAnswerOperation{}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			model, _ := nativeProgressTestModel(t, true)
			testCase.setup(model)
			if got := model.nativeProgressEligible(); got != testCase.want {
				t.Fatalf("native progress eligibility = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestReviewerAddressingFeedbackRemainsActiveInTUIStatus(t *testing.T) {
	model, _ := nativeProgressTestModel(t, true)
	model.runtimeActivityProjection.Reviewer = clientui.ReviewerActivityAddressingFeedback
	if !model.isReviewerActive() || model.statusLineLabel() != "review" || !model.statusLineSpinning() {
		t.Fatalf("addressing-feedback status = active=%t label=%q spinning=%t", model.isReviewerActive(), model.statusLineLabel(), model.statusLineSpinning())
	}
	if model.nativeProgressEligible() {
		t.Fatal("addressing-feedback Reviewer phase activated native progress")
	}
}

func TestNativeProgressUsesOneDelayedAggregateInterval(t *testing.T) {
	model, output := nativeProgressTestModel(t, true)
	model.runtimeActivityProjection.Reviewer = clientui.ReviewerActivityInvoking

	showCmd := model.reconcileNativeProgress()
	if showCmd == nil || model.nativeProgress.phase != uiNativeProgressWaiting {
		t.Fatalf("initial progress state = %+v, want waiting with delay command", model.nativeProgress)
	}
	generation := model.nativeProgress.generation

	model.pendingDetailTranscript = &uiPendingDetailTranscriptRequest{}
	if cmd := model.reconcileNativeProgress(); cmd != nil {
		t.Fatal("overlapping eligible source restarted the delay")
	}
	if model.nativeProgress.generation != generation {
		t.Fatalf("overlap changed delay generation from %d to %d", generation, model.nativeProgress.generation)
	}

	_, cmd := model.Update(nativeProgressDelayMsg{generation: generation})
	if cmd == nil {
		t.Fatal("delay completion did not request native progress")
	}
	if len(output.Bytes()) != 0 {
		t.Fatalf("Update performed terminal I/O: %q", output.Bytes())
	}
	done, ok := cmd().(nativeProgressWriteDoneMsg)
	if !ok || done.write == nil || done.write.kind != uiNativeProgressShow {
		t.Fatalf("show command result = %#v, want typed show completion", cmd())
	}
	_, _ = model.Update(done)
	if model.nativeProgress.phase != uiNativeProgressVisible {
		t.Fatalf("successful show state = %+v, want visible", model.nativeProgress)
	}
	if got, want := output.String(), xansi.SetIndeterminateProgressBar; got != want {
		t.Fatalf("native progress output = %q, want %q", got, want)
	}
}

func TestNativeProgressEndingDuringDelayInvalidatesStaleTimerAndStartsFreshInterval(t *testing.T) {
	model, _ := nativeProgressTestModel(t, true)
	model.worktrees.create.submitting = true
	delayCmd := model.reconcileNativeProgress()
	generation := model.nativeProgress.generation

	model.worktrees.create.submitting = false
	if cmd := model.reconcileNativeProgress(); cmd != nil {
		t.Fatal("ending activity during delay scheduled a command")
	}
	if model.nativeProgress.phase != uiNativeProgressHidden {
		t.Fatalf("ended delayed activity state = %+v, want hidden", model.nativeProgress)
	}
	if _, cmd := model.Update(nativeProgressDelayMsg{generation: generation}); cmd != nil {
		t.Fatal("stale delay completion scheduled native progress")
	}
	if delayCmd == nil {
		t.Fatal("initial eligible activity did not schedule a delay")
	}

	model.worktrees.deleteConfirm.submitting = true
	if cmd := model.reconcileNativeProgress(); cmd == nil {
		t.Fatal("later eligible interval did not schedule a fresh delay")
	}
	if model.nativeProgress.generation == generation {
		t.Fatal("later interval reused the ended delay generation")
	}
}

func TestNativeProgressStaleShowCommandIsCanceledBeforeOutput(t *testing.T) {
	model, output := nativeProgressTestModel(t, true)
	model.worktrees.create.submitting = true
	_ = model.reconcileNativeProgress()
	generation := model.nativeProgress.generation
	_, showCmd := model.Update(nativeProgressDelayMsg{generation: generation})
	if showCmd == nil {
		t.Fatal("eligible operation did not schedule native progress")
	}

	model.worktrees.create.submitting = false
	if cmd := model.reconcileNativeProgress(); cmd != nil {
		t.Fatal("ended operation scheduled a stale native progress command")
	}
	if model.nativeProgress.pending != nil || model.nativeProgress.phase != uiNativeProgressHidden {
		t.Fatalf("ended operation state = %+v, want no pending write and hidden progress", model.nativeProgress)
	}

	done, ok := showCmd().(nativeProgressWriteDoneMsg)
	if !ok {
		t.Fatalf("stale show command result = %T, want native progress completion", showCmd())
	}
	if !done.canceled {
		t.Fatal("stale show command performed terminal I/O")
	}
	if output.Len() != 0 {
		t.Fatalf("stale show output = %q, want empty", output.String())
	}
}

func TestNativeProgressClearsAfterLastEligibleSource(t *testing.T) {
	model, output := nativeProgressTestModel(t, true)
	model.worktrees.create.submitting = true
	_ = model.reconcileNativeProgress()
	generation := model.nativeProgress.generation
	_, showCmd := model.Update(nativeProgressDelayMsg{generation: generation})
	showDone, ok := showCmd().(nativeProgressWriteDoneMsg)
	if !ok {
		t.Fatalf("show command result = %T, want native progress completion", showCmd())
	}
	_, _ = model.Update(showDone)

	model.worktrees.create.submitting = false
	resetCmd := model.reconcileNativeProgress()
	if resetCmd == nil {
		t.Fatal("ending last eligible source did not request reset")
	}
	resetDone, ok := resetCmd().(nativeProgressWriteDoneMsg)
	if !ok || resetDone.write == nil || resetDone.write.kind != uiNativeProgressReset {
		t.Fatalf("reset command result = %#v, want typed reset completion", resetCmd())
	}
	_, _ = model.Update(resetDone)
	if model.nativeProgress.phase != uiNativeProgressHidden {
		t.Fatalf("successful reset state = %+v, want hidden", model.nativeProgress)
	}
	if got, want := output.String(), xansi.SetIndeterminateProgressBar+xansi.ResetProgressBar; got != want {
		t.Fatalf("native progress output = %q, want %q", got, want)
	}
}

func TestNativeProgressDisabledSuppressesAllOutput(t *testing.T) {
	model, output := nativeProgressTestModel(t, false)
	model.worktrees.create.submitting = true
	if cmd := model.reconcileNativeProgress(); cmd != nil {
		t.Fatal("disabled native progress scheduled a command")
	}
	if model.nativeProgress.phase != uiNativeProgressHidden || output.Len() != 0 {
		t.Fatalf("disabled native progress state = %+v output=%q", model.nativeProgress, output.String())
	}
}

func TestNativeProgressWriteFailureUsesFatalPolicyWithoutRetry(t *testing.T) {
	model, _ := nativeProgressTestModel(t, true)
	model.worktrees.create.submitting = true
	model.nativeProgress.phase = uiNativeProgressWaiting
	model.nativeProgress.delayElapsed = true
	pending := newNativeProgressWrite(uiNativeProgressShow)
	model.nativeProgress.pending = pending
	_, cmd := model.Update(nativeProgressWriteDoneMsg{
		write: pending,
		err:   errors.New("terminal unavailable"),
	})
	if cmd == nil {
		t.Fatal("failed native progress write did not request quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("failed native progress command = %T, want tea.QuitMsg", cmd())
	}
	if !model.Transition().Exit {
		t.Fatal("failed native progress write did not enter fatal UI transition")
	}
	if retry := model.reconcileNativeProgress(); retry != nil {
		t.Fatal("fatal native progress write scheduled a retry")
	}
}

func TestNativeProgressResetFailureUsesFatalPolicyWithoutRetry(t *testing.T) {
	model, _ := nativeProgressTestModel(t, true)
	model.nativeProgress.phase = uiNativeProgressVisible
	pending := newNativeProgressWrite(uiNativeProgressReset)
	model.nativeProgress.pending = pending
	_, cmd := model.Update(nativeProgressWriteDoneMsg{
		write: pending,
		err:   errors.New("terminal unavailable"),
	})
	if cmd == nil {
		t.Fatal("failed native progress reset did not request quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("failed native progress reset command = %T, want tea.QuitMsg", cmd())
	}
	if !model.Transition().Exit {
		t.Fatal("failed native progress reset did not enter fatal UI transition")
	}
	if retry := model.reconcileNativeProgress(); retry != nil {
		t.Fatal("fatal native progress reset scheduled a retry")
	}
}

func TestNativeProgressTransitionClearsDelayAndStopsScheduling(t *testing.T) {
	model, _ := nativeProgressTestModel(t, true)
	model.worktrees.create.submitting = true
	if cmd := model.reconcileNativeProgress(); cmd == nil {
		t.Fatal("eligible operation did not start delay")
	}
	model.exitAction = UIActionExit
	if cmd := model.reconcileNativeProgress(); cmd != nil {
		t.Fatal("session transition scheduled native progress")
	}
	if model.nativeProgress.phase != uiNativeProgressHidden {
		t.Fatalf("native progress after session transition = %+v, want hidden", model.nativeProgress)
	}
}

func nativeProgressTestModel(t *testing.T, enabled bool) (*uiModel, *bytes.Buffer) {
	t.Helper()
	output := &bytes.Buffer{}
	model := newProjectedTestUIModel(nil,
		WithUINativeProgressBar(enabled),
		WithUITerminalOutput(newUITerminalOutput(output)),
	)
	return model, output
}
