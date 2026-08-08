package sqlitegen

import (
	"testing"

	"core/shared/runtimeids"
)

func TestWorkflowIdentityQueryTypesRemainNullableAndTyped(t *testing.T) {
	defaultIdentity := ProjectDefaultWorkflowIdentity{}
	page := ListWorkflowRecordsPageParams{}

	var defaultWorkflowID *runtimeids.WorkflowID = defaultIdentity.WorkflowID
	var pageWorkflowID *runtimeids.WorkflowID = page.WorkflowID
	if defaultWorkflowID != nil || pageWorkflowID != nil {
		t.Fatal("zero-value nullable workflow identities must be absent")
	}
}
