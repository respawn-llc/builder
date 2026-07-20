package session

import (
	"context"
	"errors"
	"syscall"
	"testing"

	"core/shared/protocol"
)

func TestMapEventLogMaterializationErrorMapsUnsupportedVersionFacts(t *testing.T) {
	native := wrapEventLogPreparationError(false, UnsupportedEventLogVersionError{
		FoundVersion:     2,
		SupportedVersion: EventLogVersionV1,
	})
	mapped := MapEventLogMaterializationError(native)
	var wire *protocol.SessionEventLogMaterializationError
	if !errors.As(mapped, &wire) {
		t.Fatalf(
			"mapped error = %T %v, want SessionEventLogMaterializationError",
			mapped,
			mapped,
		)
	}
	if wire.Reason != protocol.SessionEventLogMaterializationUnsupportedVersion ||
		wire.Stage != protocol.SessionEventLogMaterializationPreparation ||
		wire.Committed ||
		wire.PendingRepair ||
		wire.FoundVersion == nil ||
		*wire.FoundVersion != 2 ||
		wire.SupportedVersion == nil ||
		*wire.SupportedVersion != EventLogVersionV1 {
		t.Fatalf("mapped unsupported-version facts = %+v", wire)
	}
	var preserved *EventLogMaterializationError
	if !errors.As(mapped, &preserved) {
		t.Fatalf("mapped error lost native materialization cause: %T %v", mapped, mapped)
	}
}

func TestMapEventLogMaterializationErrorMapsClosedFailureReasons(t *testing.T) {
	tests := []struct {
		name          string
		native        error
		wantReason    protocol.SessionEventLogMaterializationReason
		wantStage     protocol.SessionEventLogMaterializationStage
		wantCommitted bool
		wantPending   bool
	}{
		{
			name: "structural contract failure",
			native: wrapEventLogPreparationError(
				false,
				eventLogContractError{Err: errors.New("required field is missing")},
			),
			wantReason: protocol.SessionEventLogMaterializationStructuralFailure,
			wantStage:  protocol.SessionEventLogMaterializationPreparation,
		},
		{
			name:       "insufficient space",
			native:     wrapEventLogPreparationError(false, syscall.ENOSPC),
			wantReason: protocol.SessionEventLogMaterializationInsufficientSpace,
			wantStage:  protocol.SessionEventLogMaterializationPreparation,
		},
		{
			name:       "generic pre-commit failure",
			native:     wrapEventLogPreparationError(false, context.DeadlineExceeded),
			wantReason: protocol.SessionEventLogMaterializationFailure,
			wantStage:  protocol.SessionEventLogMaterializationPreparation,
		},
		{
			name: "reconciliation pending after rename",
			native: wrapEventLogMaterializationError(
				EventLogMaterializationStageReconciliation,
				true,
				true,
				context.DeadlineExceeded,
			),
			wantReason:    protocol.SessionEventLogMaterializationReconciliationPending,
			wantStage:     protocol.SessionEventLogMaterializationReconciliation,
			wantCommitted: true,
			wantPending:   true,
		},
		{
			name:          "post-rename preparation failure remains pending",
			native:        wrapEventLogPreparationError(true, context.DeadlineExceeded),
			wantReason:    protocol.SessionEventLogMaterializationReconciliationPending,
			wantStage:     protocol.SessionEventLogMaterializationPreparation,
			wantCommitted: true,
			wantPending:   true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped := MapEventLogMaterializationError(test.native)
			var wire *protocol.SessionEventLogMaterializationError
			if !errors.As(mapped, &wire) {
				t.Fatalf("mapped error = %T %v", mapped, mapped)
			}
			if wire.Reason != test.wantReason ||
				wire.Stage != test.wantStage ||
				wire.Committed != test.wantCommitted ||
				wire.PendingRepair != test.wantPending {
				t.Fatalf(
					"mapped facts = %+v, want reason=%q stage=%q committed=%t pending=%t",
					wire,
					test.wantReason,
					test.wantStage,
					test.wantCommitted,
					test.wantPending,
				)
			}
			if err := wire.Validate(); err != nil {
				t.Fatalf("mapped wire contract is invalid: %+v: %v", wire, err)
			}
		})
	}
}

func TestMapEventLogMaterializationErrorLeavesUnrelatedErrorsNative(t *testing.T) {
	native := errors.New("metadata mutation failed")
	if mapped := MapEventLogMaterializationError(native); mapped != native {
		t.Fatalf("unrelated error mapped to %T %v", mapped, mapped)
	}
}

func TestMapEventLogMaterializationErrorUsesActualCommitBoundaryFacts(t *testing.T) {
	t.Run("malformed legacy is structural and pre-commit", func(t *testing.T) {
		sessionDir := newEventLogPreparationSessionDir(t)
		writeEventLogPreparationSource(
			t,
			sessionDir+"/events.jsonl",
			[]byte(
				`{"seq":1,"timestamp":"2026-07-20T00:00:00Z","kind":"message","payload":not-json}`+"\n",
			),
		)
		store := newEventLogReconciliationStore(
			t,
			sessionDir,
			reconciliationTestMeta(),
			&eventLogReconciliationTestObserver{},
			nil,
		)
		_, native := store.MaterializeEventLog()
		mapped := MapEventLogMaterializationError(native)
		var wire *protocol.SessionEventLogMaterializationError
		if !errors.As(mapped, &wire) {
			t.Fatalf("mapped error = %T %v", mapped, mapped)
		}
		if wire.Reason != protocol.SessionEventLogMaterializationStructuralFailure ||
			wire.Committed ||
			wire.PendingRepair {
			t.Fatalf("pre-commit structural facts = %+v", wire)
		}
	})

	t.Run("observer failure is reconciliation pending after rename", func(t *testing.T) {
		sessionDir := newEventLogPreparationSessionDir(t)
		store := newEventLogReconciliationStore(
			t,
			sessionDir,
			reconciliationTestMeta(),
			&eventLogReconciliationTestObserver{err: context.DeadlineExceeded},
			nil,
		)
		_, native := store.MaterializeEventLog()
		mapped := MapEventLogMaterializationError(native)
		var wire *protocol.SessionEventLogMaterializationError
		if !errors.As(mapped, &wire) {
			t.Fatalf("mapped error = %T %v", mapped, mapped)
		}
		if wire.Reason != protocol.SessionEventLogMaterializationReconciliationPending ||
			!wire.Committed ||
			!wire.PendingRepair {
			t.Fatalf("post-rename pending-repair facts = %+v", wire)
		}
	})
}
