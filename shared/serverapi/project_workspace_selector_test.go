package serverapi

import (
	"encoding/json"
	"testing"
)

func TestProjectWorkspaceSelectorRoundTripsLegacyWorkspaceIDPayload(t *testing.T) {
	var request ProjectWorkspaceUnlinkRequest
	if err := json.Unmarshal([]byte(`{"project_id":"project-1","workspace_id":"workspace-1"}`), &request); err != nil {
		t.Fatalf("unmarshal legacy request: %v", err)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("validate legacy request: %v", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal legacy request: %v", err)
	}
	if string(encoded) != `{"project_id":"project-1","workspace_id":"workspace-1"}` {
		t.Fatalf("encoded legacy request = %s", encoded)
	}
}

func TestProjectWorkspaceSelectorAcceptsWorkspaceRoot(t *testing.T) {
	selector, err := NewProjectWorkspaceSelectorForRoot("/workspace")
	if err != nil {
		t.Fatalf("construct path request: %v", err)
	}
	request := ProjectWorkspaceUnlinkRequest{
		ProjectID:                "project-1",
		ProjectWorkspaceSelector: selector,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("validate path request: %v", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal path request: %v", err)
	}
	if string(encoded) != `{"project_id":"project-1","workspace_root":"/workspace"}` {
		t.Fatalf("encoded path request = %s", encoded)
	}
}

func TestProjectWorkspaceSelectorRejectsMultipleOrBlankSelectors(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing", body: `{"project_id":"project-1"}`},
		{name: "blank id", body: `{"project_id":"project-1","workspace_id":" "}`},
		{name: "blank root", body: `{"project_id":"project-1","workspace_root":" "}`},
		{name: "both", body: `{"project_id":"project-1","workspace_id":"workspace-1","workspace_root":"/workspace"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var request ProjectWorkspaceUnlinkRequest
			if err := json.Unmarshal([]byte(test.body), &request); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			if err := request.Validate(); err == nil {
				t.Fatal("validate request succeeded")
			}
		})
	}
}
