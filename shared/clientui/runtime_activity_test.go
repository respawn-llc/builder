package clientui

import "testing"

func TestRuntimeActivityValidationMatrixForFirstProof(t *testing.T) {
	version := mustReadModelVersion(t, "epoch-1", 1, 1)

	tests := []struct {
		name     string
		activity RuntimeActivity
		active   bool
	}{
		{name: "unavailable", activity: MustRuntimeActivity(RuntimeActivityUnavailable, RuntimeActivityOptions{})},
		{name: "registered idle accepts queue policy", activity: MustRuntimeActivity(RuntimeActivityRegisteredIdle, RuntimeActivityOptions{QueueAccepting: true})},
		{name: "running goal loop", activity: MustRuntimeActivity(RuntimeActivityRunning, RuntimeActivityOptions{ActiveKind: RuntimeActivityActiveKindGoalLoop, RunID: "run-1", StepID: "step-1", QueueAccepting: true}), active: true},
		{name: "running user turn", activity: MustRuntimeActivity(RuntimeActivityRunning, RuntimeActivityOptions{ActiveKind: RuntimeActivityActiveKindUserTurn, RunID: "run-2", StepID: "step-2"}), active: true},
		{name: "diagnostic recovery idle", activity: MustRuntimeActivity(RuntimeActivityRegisteredIdle, RuntimeActivityOptions{DiagnosticRecovery: true})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.activity.Validate(); err != nil {
				t.Fatalf("valid activity rejected: %v", err)
			}
			if got := tt.activity.ActiveForControl(); got != tt.active {
				t.Fatalf("ActiveForControl = %t, want %t", got, tt.active)
			}
			snapshot := NewEmptyRuntimeInputReconciliationSnapshot(version)
			if snapshot.Version != version {
				t.Fatalf("snapshot version = %+v, want %+v", snapshot.Version, version)
			}
			if len(snapshot.Operations) != 0 {
				t.Fatalf("empty reconciliation operations = %+v, want none", snapshot.Operations)
			}
		})
	}
}

func TestRuntimeActivityRejectsInvalidCombinations(t *testing.T) {
	tests := []struct {
		name     string
		activity RuntimeActivity
	}{
		{name: "unavailable queue accepting", activity: RuntimeActivity{State: RuntimeActivityUnavailable, QueueAccepting: true}},
		{name: "idle with active kind", activity: RuntimeActivity{State: RuntimeActivityRegisteredIdle, ActiveKind: RuntimeActivityActiveKindGoalLoop}},
		{name: "running without kind", activity: RuntimeActivity{State: RuntimeActivityRunning, RunID: "run-1", StepID: "step-1"}},
		{name: "running without run", activity: RuntimeActivity{State: RuntimeActivityRunning, ActiveKind: RuntimeActivityActiveKindUserTurn, StepID: "step-1"}},
		{name: "running invalid kind", activity: RuntimeActivity{State: RuntimeActivityRunning, ActiveKind: RuntimeActivityActiveKind("review"), RunID: "run-1", StepID: "step-1"}},
		{name: "unknown state", activity: RuntimeActivity{State: RuntimeActivityState("busy")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.activity.Validate(); err == nil {
				t.Fatal("expected invalid runtime activity to be rejected")
			}
		})
	}
}

func TestReadModelVersionOrdersWithinGeneration(t *testing.T) {
	oldVersion := mustReadModelVersion(t, "epoch-1", 3, 10)
	newVersion := mustReadModelVersion(t, "epoch-1", 3, 11)
	differentGeneration := mustReadModelVersion(t, "epoch-1", 4, 1)
	differentEpoch := mustReadModelVersion(t, "epoch-2", 1, 1)

	if !newVersion.NewerThan(oldVersion) {
		t.Fatal("expected higher same-generation sequence to be newer")
	}
	if oldVersion.NewerThan(newVersion) {
		t.Fatal("expected lower same-generation sequence not to be newer")
	}
	if differentGeneration.NewerThan(newVersion) {
		t.Fatal("different generation must require hydration/reset, not stale-apply ordering")
	}
	if differentEpoch.NewerThan(newVersion) {
		t.Fatal("different epoch must require hydration/reset, not stale-apply ordering")
	}
}

func TestRuntimeInputReconciliationUnknownEntriesShareSnapshotVersion(t *testing.T) {
	version := mustReadModelVersion(t, "epoch-1", 1, 2)
	ref := RuntimeOperationRef{Kind: RuntimeOperationKindSubmit, ClientRequestID: "client-1"}

	snapshot := NewUnknownRuntimeInputReconciliationSnapshot(version, []RuntimeOperationRef{ref})
	if snapshot.Version != version {
		t.Fatalf("snapshot version = %+v, want %+v", snapshot.Version, version)
	}
	if len(snapshot.Operations) != 1 {
		t.Fatalf("operations = %+v, want one", snapshot.Operations)
	}
	operation := snapshot.Operations[0]
	if operation.Version != version {
		t.Fatalf("operation version = %+v, want %+v", operation.Version, version)
	}
	if operation.OperationRef != ref {
		t.Fatalf("operation ref = %+v, want %+v", operation.OperationRef, ref)
	}
	if operation.State != RuntimeInputReconciliationUnknown {
		t.Fatalf("operation state = %q, want %q", operation.State, RuntimeInputReconciliationUnknown)
	}
}

func TestRuntimeOperationRefCoversInputBearingOperations(t *testing.T) {
	for _, kind := range []RuntimeOperationKind{
		RuntimeOperationKindSubmit,
		RuntimeOperationKindUserShell,
		RuntimeOperationKindCompact,
		RuntimeOperationKindPreSubmitCompact,
		RuntimeOperationKindSubmitQueued,
	} {
		t.Run(string(kind), func(t *testing.T) {
			ref := RuntimeOperationRef{Kind: kind, ClientRequestID: "client-1"}
			if err := ref.Validate(); err != nil {
				t.Fatalf("valid ref rejected: %v", err)
			}
			if ref.Key() == "" {
				t.Fatal("operation ref key must be stable and non-empty")
			}
		})
	}
	t.Run(string(RuntimeOperationKindQueuedMessage), func(t *testing.T) {
		for _, ref := range []RuntimeOperationRef{
			{Kind: RuntimeOperationKindQueuedMessage, ClientRequestID: "client-queue-1"},
			{Kind: RuntimeOperationKindQueuedMessage, QueueItemID: "queue-1"},
		} {
			if err := ref.Validate(); err != nil {
				t.Fatalf("valid queued-message ref rejected: %v", err)
			}
			if ref.Key() == "" {
				t.Fatal("operation ref key must be stable and non-empty")
			}
		}
	})
}

func TestRuntimeOperationRefRejectsMissingIdentity(t *testing.T) {
	for _, ref := range []RuntimeOperationRef{
		{Kind: RuntimeOperationKindSubmit},
		{Kind: RuntimeOperationKindQueuedMessage},
		{Kind: RuntimeOperationKindQueuedMessage, QueueItemID: "queue-1", ClientRequestID: "client-1"},
		{Kind: RuntimeOperationKindSubmit, ClientRequestID: "submit-1", QueueItemID: "queue-1"},
		{ClientRequestID: "client-1"},
		{Kind: RuntimeOperationKind("goal"), ClientRequestID: "client-1"},
	} {
		if err := ref.Validate(); err == nil {
			t.Fatalf("expected ref %+v to be rejected", ref)
		}
	}
}

func TestRuntimeInputReconciliationStatesValidateConservativeOutcomes(t *testing.T) {
	version := mustReadModelVersion(t, "epoch-1", 1, 3)
	ref := RuntimeOperationRef{Kind: RuntimeOperationKindUserShell, ClientRequestID: "shell-1"}
	for _, state := range []RuntimeInputReconciliationState{
		RuntimeInputReconciliationCommitted,
		RuntimeInputReconciliationAccepted,
		RuntimeInputReconciliationSubmitted,
		RuntimeInputReconciliationCanceledNotCommitted,
		RuntimeInputReconciliationFailedWithRestore,
		RuntimeInputReconciliationUnknown,
		RuntimeInputReconciliationEvicted,
	} {
		record := RuntimeInputReconciliation{Version: version, OperationRef: ref, State: state}
		if err := record.Validate(); err != nil {
			t.Fatalf("state %q rejected: %v", state, err)
		}
		if record.RestoreRecommended() != (state == RuntimeInputReconciliationCanceledNotCommitted || state == RuntimeInputReconciliationFailedWithRestore) {
			t.Fatalf("RestoreRecommended for %q = %t", state, record.RestoreRecommended())
		}
		if record.Ambiguous() != (state == RuntimeInputReconciliationUnknown || state == RuntimeInputReconciliationEvicted) {
			t.Fatalf("Ambiguous for %q = %t", state, record.Ambiguous())
		}
	}
}

func TestRuntimeInputRequestDTOsCarryCallerOperationRefs(t *testing.T) {
	submitRef := RuntimeOperationRef{Kind: RuntimeOperationKindSubmit, ClientRequestID: "submit-1"}
	preSubmitRef := RuntimeOperationRef{Kind: RuntimeOperationKindPreSubmitCompact, ClientRequestID: "pre-compact-1"}
	shellRef := RuntimeOperationRef{Kind: RuntimeOperationKindUserShell, ClientRequestID: "shell-1"}
	compactRef := RuntimeOperationRef{Kind: RuntimeOperationKindCompact, ClientRequestID: "compact-1"}
	queuedRef := RuntimeOperationRef{Kind: RuntimeOperationKindSubmitQueued, ClientRequestID: "queued-1"}

	for name, err := range map[string]error{
		"submit":      (RuntimeSubmitRequest{OperationRef: submitRef, PreSubmitCompactionOperationRef: preSubmitRef, Text: "hello"}).Validate(),
		"shell":       (RuntimeShellRequest{OperationRef: shellRef, Command: "pwd"}).Validate(),
		"compact":     (RuntimeCompactRequest{OperationRef: compactRef, Args: "notes"}).Validate(),
		"pre-compact": (RuntimePreSubmitCompactRequest{OperationRef: preSubmitRef}).Validate(),
		"queued":      (RuntimeSubmitQueuedRequest{OperationRef: queuedRef}).Validate(),
	} {
		if err != nil {
			t.Fatalf("%s request rejected: %v", name, err)
		}
	}
}

func mustReadModelVersion(t *testing.T, epoch string, generation uint64, sequence uint64) ReadModelVersion {
	t.Helper()
	version, err := NewReadModelVersion(epoch, generation, sequence)
	if err != nil {
		t.Fatalf("NewReadModelVersion: %v", err)
	}
	return version
}
