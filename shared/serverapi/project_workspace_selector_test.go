package serverapi

import "testing"

func TestProjectWorkspaceSelectorAcceptsExactlyOneIdentity(t *testing.T) {
	for _, construct := range []func() (ProjectWorkspaceSelector, error){
		func() (ProjectWorkspaceSelector, error) {
			return NewProjectWorkspaceSelectorForID("workspace-1")
		},
		func() (ProjectWorkspaceSelector, error) {
			return NewProjectWorkspaceSelectorForRoot("/workspace")
		},
	} {
		selector, err := construct()
		if err != nil {
			t.Fatal(err)
		}
		if err := selector.Validate(); err != nil {
			t.Fatalf("valid selector rejected: %v", err)
		}
	}
}

func TestProjectWorkspaceSelectorRejectsMissingMultipleAndBlankIdentities(t *testing.T) {
	blank := " "
	id := "workspace-1"
	root := "/workspace"
	for _, selector := range []ProjectWorkspaceSelector{
		{},
		{WorkspaceID: &blank},
		{WorkspaceRoot: &blank},
		{WorkspaceID: &id, WorkspaceRoot: &root},
	} {
		if err := selector.Validate(); err == nil {
			t.Fatalf("invalid selector accepted: %+v", selector)
		}
	}
}
