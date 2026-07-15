package serverapi

import (
	"encoding/json"
	"testing"
)

func TestSessionExecutionWorkspaceRootResponseRejectsMissingNullBlankAndUnknownData(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"workspace_root":null}`,
		`{"workspace_root":""}`,
		`{"workspace_root":" "}`,
		`{"workspace_root":"/workspace","unknown":true}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var response SessionExecutionWorkspaceRootResponse
			if err := json.Unmarshal([]byte(raw), &response); err == nil {
				t.Fatalf("json.Unmarshal(%s) error = nil", raw)
			}
		})
	}
}

func TestSessionExecutionWorkspaceRootContractsUseStrictNormalizedJSON(t *testing.T) {
	var request SessionExecutionWorkspaceRootRequest
	if err := json.Unmarshal(
		[]byte(`{"session_id":"session-1","unknown":true}`),
		&request,
	); err == nil {
		t.Fatal("request with unknown field decoded")
	}

	var response SessionExecutionWorkspaceRootResponse
	if err := json.Unmarshal(
		[]byte(`{"workspace_root":" /workspace "}`),
		&response,
	); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}
	if response.WorkspaceRoot != "/workspace" {
		t.Fatalf("normalized workspace root = %q", response.WorkspaceRoot)
	}
}
