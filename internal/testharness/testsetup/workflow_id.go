package testsetup

import (
	"crypto/sha256"
	"testing"

	"core/shared/runtimeids"

	"github.com/google/uuid"
)

// WorkflowID deterministically derives a canonical Workflow ID from a test label.
func WorkflowID(t testing.TB, label string) runtimeids.WorkflowID {
	t.Helper()
	sum := sha256.Sum256([]byte(label))
	bytes := sum[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	id, err := runtimeids.ParseWorkflowID(uuid.Must(uuid.FromBytes(bytes)).String())
	if err != nil {
		t.Fatalf("parse deterministic workflow ID: %v", err)
	}
	return id
}
