package workflowstore

import (
	"context"
	"testing"

	"core/server/workflow"
)

func TestCurrentNodeTaskEventPublicationSurfacesReadFailure(t *testing.T) {
	store := &Store{}
	_, err := store.InterruptCurrentNodes(
		context.Background(),
		[]workflow.CurrentNodeReference{{}},
		workflow.CurrentNodeInterruptionReason(""),
		workflow.CurrentNodeInterruptionDetail{},
	)
	if err == nil {
		t.Fatal("invalid aggregate interruption was accepted")
	}
}
