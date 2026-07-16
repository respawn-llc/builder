package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	patchformat "core/shared/transcript/patchformat"
)

type DeveloperDiagnosticKind string

const (
	DeveloperDiagnosticDeletionFactMismatch DeveloperDiagnosticKind = "deletion_fact_mismatch"
)

type DeveloperDiagnostic struct {
	DeletionFactMismatch *DeletionFactMismatchDeveloperDiagnostic `json:"deletion_fact_mismatch,omitempty"`
}

type DeletionFactMismatchDeveloperDiagnostic struct {
	CallID       string                                        `json:"call_id"`
	OperationID  patchformat.WholeFileDeletionOperationID      `json:"operation_id"`
	MismatchKind patchformat.WholeFileDeletionFactMismatchKind `json:"mismatch_kind"`
}

func (d *DeletionFactMismatchDeveloperDiagnostic) UnmarshalJSON(data []byte) error {
	var wire struct {
		CallID       string                                        `json:"call_id"`
		OperationID  *patchformat.WholeFileDeletionOperationID     `json:"operation_id"`
		MismatchKind patchformat.WholeFileDeletionFactMismatchKind `json:"mismatch_kind"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.OperationID == nil {
		return errors.New("deletion fact mismatch diagnostic hunk ordinal is required")
	}
	*d = DeletionFactMismatchDeveloperDiagnostic{
		CallID:       wire.CallID,
		OperationID:  *wire.OperationID,
		MismatchKind: wire.MismatchKind,
	}
	return nil
}

func NewDeletionFactMismatchDeveloperDiagnostic(
	callID string,
	mismatch patchformat.WholeFileDeletionFactMismatchError,
) DeveloperDiagnostic {
	return DeveloperDiagnostic{
		DeletionFactMismatch: &DeletionFactMismatchDeveloperDiagnostic{
			CallID:       strings.TrimSpace(callID),
			OperationID:  mismatch.ID,
			MismatchKind: mismatch.Kind,
		},
	}
}

func (d DeveloperDiagnostic) Kind() (DeveloperDiagnosticKind, bool) {
	if d.DeletionFactMismatch != nil {
		return DeveloperDiagnosticDeletionFactMismatch, true
	}
	return "", false
}

func (d DeveloperDiagnostic) Validate() error {
	context := d.DeletionFactMismatch
	if context == nil {
		return errors.New("developer diagnostic variant is required")
	}
	if strings.TrimSpace(context.CallID) == "" {
		return errors.New("deletion fact mismatch diagnostic call id is required")
	}
	if context.OperationID.HunkOrdinal < 0 {
		return errors.New("deletion fact mismatch diagnostic hunk ordinal must be non-negative")
	}
	switch context.MismatchKind {
	case patchformat.WholeFileDeletionFactMismatchDuplicate,
		patchformat.WholeFileDeletionFactMismatchUnmatched,
		patchformat.WholeFileDeletionFactMismatchMissing,
		patchformat.WholeFileDeletionFactMismatchInvalidCount:
		return nil
	default:
		return errors.New("deletion fact mismatch diagnostic kind is invalid")
	}
}

func DeveloperDiagnosticText(d DeveloperDiagnostic) string {
	context := d.DeletionFactMismatch
	if context == nil {
		return "developer diagnostic"
	}
	return fmt.Sprintf(
		"whole-file deletion count presentation mismatch: %s (call %s, hunk %d)",
		context.MismatchKind,
		context.CallID,
		context.OperationID.HunkOrdinal,
	)
}

func DeveloperDiagnosticEqual(left, right *DeveloperDiagnostic) bool {
	if left == nil || right == nil {
		return left == right
	}
	leftKind, leftPresent := left.Kind()
	rightKind, rightPresent := right.Kind()
	if leftPresent != rightPresent || leftKind != rightKind {
		return false
	}
	if left.DeletionFactMismatch == nil || right.DeletionFactMismatch == nil {
		return left.DeletionFactMismatch == right.DeletionFactMismatch
	}
	return *left.DeletionFactMismatch == *right.DeletionFactMismatch
}

func CloneDeveloperDiagnostic(in *DeveloperDiagnostic) *DeveloperDiagnostic {
	if in == nil {
		return nil
	}
	out := *in
	if in.DeletionFactMismatch != nil {
		context := *in.DeletionFactMismatch
		out.DeletionFactMismatch = &context
	}
	return &out
}
