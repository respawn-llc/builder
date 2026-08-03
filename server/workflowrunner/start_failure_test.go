package workflowrunner

import (
	"errors"
	"testing"

	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowscript"
	"core/server/worktree"
	"core/shared/serverapi"
)

func TestCurrentNodeStartFailureProjectsTypedCauses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		cause      error
		wantReason workflow.CurrentNodeInterruptionReason
		wantCode   string
		wantFields map[string]string
	}{
		{
			name: "script validation",
			cause: workflowscript.ValidationError{Diagnostic: workflowscript.Diagnostic{
				Code:         workflowscript.CodePathNotFound,
				RawPath:      "run.sh",
				ResolvedPath: "/workspace/run.sh",
				Message:      "script missing",
			}},
			wantReason: workflow.CurrentNodeInterruptionReason("workflow_script_validation_failed"),
			wantCode:   workflowscript.ReasonValidationFailed,
			wantFields: map[string]string{
				"code":          workflowscript.CodePathNotFound,
				"raw_path":      "run.sh",
				"resolved_path": "/workspace/run.sh",
			},
		},
		{
			name: "target resolution",
			cause: &worktree.GitRevisionResolutionError{
				Kind:         worktree.GitRevisionResolutionErrorInvalidRevision,
				RequestedRef: "feature/missing",
			},
			wantReason: workflow.CurrentNodeInterruptionReason("workflow_runtime_start_failed"),
			wantCode:   "workflow_execution_target_resolution_failed",
			wantFields: map[string]string{
				"code":          string(worktree.GitRevisionResolutionErrorInvalidRevision),
				"requested_ref": "feature/missing",
			},
		},
		{
			name: "default branch missing",
			cause: &worktree.GitDefaultBranchResolutionError{
				Kind: worktree.GitDefaultBranchResolutionErrorMissing,
			},
			wantReason: workflow.CurrentNodeInterruptionReason("workflow_runtime_start_failed"),
			wantCode:   "workflow_execution_target_resolution_failed",
			wantFields: map[string]string{
				"code":           string(serverapi.WorkflowExecutionTargetUnavailableCauseDefaultBranchMissing),
				"selection_mode": string(workflow.ExecutionTargetModeDefaultBranch),
			},
		},
		{
			name: "default branch ambiguous",
			cause: &worktree.GitDefaultBranchResolutionError{
				Kind: worktree.GitDefaultBranchResolutionErrorAmbiguous,
			},
			wantReason: workflow.CurrentNodeInterruptionReason("workflow_runtime_start_failed"),
			wantCode:   "workflow_execution_target_resolution_failed",
			wantFields: map[string]string{
				"code":           string(serverapi.WorkflowExecutionTargetUnavailableCauseDefaultBranchAmbiguous),
				"selection_mode": string(workflow.ExecutionTargetModeDefaultBranch),
			},
		},
		{
			name: "locked worktree",
			cause: &worktree.LockedTaskWorktreeError{
				Cause: worktree.LockedTaskWorktreeCauseRootInaccessible,
			},
			wantReason: workflow.CurrentNodeInterruptionReason("workflow_runtime_start_failed"),
			wantCode:   "workflow_locked_execution_target_unavailable",
			wantFields: map[string]string{
				"cause": string(worktree.LockedTaskWorktreeCauseRootInaccessible),
			},
		},
		{
			name: "unlocked target preparation",
			cause: &workflowexecution.ExecutionTargetPreparationFailure{
				Cause: errors.New("source workspace lookup failed"),
			},
			wantReason: workflow.CurrentNodeInterruptionReason("workflow_runtime_start_failed"),
			wantCode:   "workflow_execution_target_preparation_failed",
			wantFields: map[string]string{
				"error": "source workspace lookup failed",
			},
		},
		{
			name:       "unknown",
			cause:      errors.New("provider unavailable"),
			wantReason: workflow.CurrentNodeInterruptionReason("workflow_runtime_start_failed"),
			wantCode:   "workflow_runtime_start_failed",
			wantFields: map[string]string{
				"error": "provider unavailable",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := currentNodeStartFailure(test.cause)
			var projected *workflowexecution.CurrentNodeStartFailure
			if !errors.As(failure, &projected) {
				t.Fatalf("failure = %T %v, want CurrentNodeStartFailure", failure, failure)
			}
			if projected.Reason != test.wantReason {
				t.Fatalf("reason = %q, want %q", projected.Reason, test.wantReason)
			}
			if projected.Detail.Code != test.wantCode {
				t.Fatalf("detail code = %q, want %q", projected.Detail.Code, test.wantCode)
			}
			for key, want := range test.wantFields {
				if got := projected.Detail.Fields[key]; got != want {
					t.Fatalf("detail field %q = %q, want %q", key, got, want)
				}
			}
			if !errors.Is(failure, test.cause) {
				t.Fatalf("failure %v does not unwrap to %v", failure, test.cause)
			}
		})
	}
}

func TestCurrentNodeStartFailurePreservesExplicitTargetSelectionSource(t *testing.T) {
	requestedRef := "missing/ref"
	failure := currentNodeStartFailure(&workflowexecution.ExecutionTargetPreparationFailure{
		Cause: &worktree.GitRevisionResolutionError{
			Kind:         worktree.GitRevisionResolutionErrorInvalidRevision,
			RequestedRef: requestedRef,
		},
		Selection: workflow.ExecutionTargetSelection{
			Mode:      workflow.ExecutionTargetModeCustomRef,
			CustomRef: &requestedRef,
		},
		SelectionSource: workflowexecution.ExecutionTargetSelectionSourceExplicit,
	})
	var projected *workflowexecution.CurrentNodeStartFailure
	if !errors.As(failure, &projected) {
		t.Fatalf("failure = %T %v, want CurrentNodeStartFailure", failure, failure)
	}
	metadata, err := workflowexecution.ExecutionTargetResolutionFailureMetadataFromFields(projected.Detail.Fields)
	if err != nil {
		t.Fatalf("resolution failure metadata: %v", err)
	}
	if metadata.SelectionSource != workflowexecution.ExecutionTargetSelectionSourceExplicit ||
		metadata.SelectionMode != workflow.ExecutionTargetModeCustomRef ||
		metadata.RequestedRef == nil ||
		*metadata.RequestedRef != requestedRef {
		t.Fatalf("resolution failure metadata = %+v, want explicit custom ref", metadata)
	}
}
