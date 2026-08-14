package protocol

import (
	"encoding/json"
	"testing"
)

func TestHandshakeRequestUsesVersionOnlyWireContract(t *testing.T) {
	request := HandshakeRequest{ProtocolVersion: Version}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("decode handshake fields: %v", err)
	}
	if len(fields) != 1 {
		t.Fatalf("handshake fields = %v, want protocol_version only", fields)
	}
	var version string
	if err := json.Unmarshal(fields["protocol_version"], &version); err != nil {
		t.Fatalf("decode protocol version: %v", err)
	}
	if version != Version {
		t.Fatalf("protocol version = %q, want %q", version, Version)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestServerIdentityWireContractHasNoCapabilityProjection(t *testing.T) {
	identity := ServerIdentity{
		ProtocolVersion:   Version,
		ServerID:          "server-1",
		PID:               42,
		PersistenceRootID: "root-1",
	}
	data, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("decode identity fields: %v", err)
	}
	if _, ok := fields["capabilities"]; ok {
		t.Fatalf("server identity contains capability projection: %s", data)
	}
	for _, field := range []string{"protocol_version", "server_id", "pid", "persistence_root_id"} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("server identity is missing %q: %s", field, data)
		}
	}
}

func TestAttachProjectRequestStrictWorkspaceSelectionRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		new  func() (AttachProjectRequest, error)
		want string
	}{
		{
			name: "default workspace",
			new:  func() (AttachProjectRequest, error) { return AttachProjectRequestForDefaultWorkspace("project-1") },
			want: `{"project_id":"project-1","workspace":null}`,
		},
		{
			name: "workspace id",
			new: func() (AttachProjectRequest, error) {
				return AttachProjectRequestForWorkspaceID("project-1", "workspace-1")
			},
			want: `{"project_id":"project-1","workspace":{"kind":"workspace_id","workspace_id":"workspace-1"}}`,
		},
		{
			name: "workspace root",
			new: func() (AttachProjectRequest, error) {
				return AttachProjectRequestForWorkspaceRoot("project-1", "/workspace")
			},
			want: `{"project_id":"project-1","workspace":{"kind":"workspace_root","workspace_root":"/workspace"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := test.new()
			if err != nil {
				t.Fatalf("construct request: %v", err)
			}
			data, err := json.Marshal(request)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if got := string(data); got != test.want {
				t.Fatalf("JSON = %s, want %s", got, test.want)
			}
			var decoded AttachProjectRequest
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !decoded.Equal(request) {
				t.Fatalf("decoded = %+v, want %+v", decoded, request)
			}
		})
	}
}

func TestAttachProjectRequestRejectsMalformedWorkspaceSelection(t *testing.T) {
	tests := []string{
		`{}`,
		`{"project_id":"project-1"}`,
		`{"project_id":" ","workspace":null}`,
		`{"project_id":" project-1 ","workspace":null}`,
		`{"project_id":"project-1","workspace_id":"workspace-1"}`,
		`{"project_id":"project-1","workspace_root":"/workspace"}`,
		`{"project_id":"project-1","workspace":{"kind":""}}`,
		`{"project_id":"project-1","workspace":{"kind":"unknown","workspace_id":"workspace-1"}}`,
		`{"project_id":"project-1","workspace":{"kind":"workspace_id"}}`,
		`{"project_id":"project-1","workspace":{"kind":"workspace_id","workspace_id":" "}}`,
		`{"project_id":"project-1","workspace":{"kind":"workspace_id","workspace_id":"workspace-1","workspace_root":"/workspace"}}`,
		`{"project_id":"project-1","workspace":{"kind":"workspace_root"}}`,
		`{"project_id":"project-1","workspace":{"kind":"workspace_root","workspace_root":" "}}`,
		`{"project_id":"project-1","workspace":{"kind":"workspace_root","workspace_root":"/workspace","workspace_id":"workspace-1"}}`,
		`{"project_id":"project-1","workspace":null,"extra":true}`,
	}
	for _, data := range tests {
		var request AttachProjectRequest
		if err := json.Unmarshal([]byte(data), &request); err == nil {
			t.Fatalf("Unmarshal(%s) error = nil", data)
		}
	}
}

func TestAttachResponseStrictTypedRoundTrip(t *testing.T) {
	rootRequest, err := AttachProjectRequestForWorkspaceRoot("project-1", "/workspace-alias")
	if err != nil {
		t.Fatalf("root request: %v", err)
	}
	rootResponse, err := ProjectAttachResponseForRequest(rootRequest, "workspace-1", "/workspace")
	if err != nil {
		t.Fatalf("root response: %v", err)
	}
	tests := []struct {
		name  string
		value AttachResponse
	}{
		{
			name:  "project",
			value: mustProjectAttachResponse(t, "project-1", "workspace-1", "/workspace"),
		},
		{
			name:  "project selected by root",
			value: rootResponse,
		},
		{
			name:  "session",
			value: mustSessionAttachResponse(t, "project-1", "workspace-1", "/workspace", "session-1"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(test.value)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var decoded AttachResponse
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !decoded.Equal(test.value) {
				t.Fatalf("decoded = %+v, want %+v", decoded, test.value)
			}
		})
	}
}

func TestAttachResponseRejectsMalformedVariants(t *testing.T) {
	tests := []string{
		`{}`,
		`{"kind":""}`,
		`{"kind":"unknown","project_id":"project-1","workspace_id":"workspace-1","workspace_root":"/workspace"}`,
		`{"kind":"project","project_id":"project-1","workspace_id":"workspace-1"}`,
		`{"kind":"project","project_id":"project-1","workspace_id":"workspace-1","workspace_root":"/workspace"}`,
		`{"kind":"project","project_id":"project-1","workspace_id":"workspace-1","workspace_root":"/workspace","session_id":"session-1"}`,
		`{"kind":"session","project_id":"project-1","workspace_id":"workspace-1","workspace_root":"/workspace"}`,
		`{"kind":"session","project_id":"project-1","workspace_id":"workspace-1","workspace_root":"/workspace","session_id":" "}`,
		`{"kind":"project","project_id":" project-1 ","workspace_id":"workspace-1","workspace_root":"/workspace"}`,
		`{"kind":"project","project_id":"project-1","workspace_id":"workspace-1","workspace_root":"/workspace","extra":true}`,
		`{"kind":"project","project_id":"project-1","workspace_id":"workspace-1","workspace_root":"/workspace","workspace_selection":{"kind":"workspace_root","requested_root":"/other","canonical_root":"/other"}}`,
		`{"kind":"project","project_id":"project-1","workspace_id":"workspace-1","workspace_root":"/workspace","workspace_selection":{"kind":"workspace_id","workspace_id":"other-workspace"}}`,
	}
	for _, data := range tests {
		var response AttachResponse
		if err := json.Unmarshal([]byte(data), &response); err == nil {
			t.Fatalf("Unmarshal(%s) error = nil", data)
		}
	}
}

func mustProjectAttachResponse(t testing.TB, projectID string, workspaceID string, workspaceRoot string) AttachResponse {
	t.Helper()
	response, err := ProjectAttachResponse(projectID, workspaceID, workspaceRoot)
	if err != nil {
		t.Fatalf("ProjectAttachResponse: %v", err)
	}
	return response
}

func mustSessionAttachResponse(t testing.TB, projectID string, workspaceID string, workspaceRoot string, sessionID string) AttachResponse {
	t.Helper()
	response, err := SessionAttachResponse(projectID, workspaceID, workspaceRoot, sessionID)
	if err != nil {
		t.Fatalf("SessionAttachResponse: %v", err)
	}
	return response
}
