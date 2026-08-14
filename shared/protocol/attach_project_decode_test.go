package protocol

import (
	"encoding/json"
	"testing"
)

func TestAttachProjectRequestUnmarshalIsRepresentationOnly(t *testing.T) {
	var request AttachProjectRequest
	if err := json.Unmarshal([]byte(`{"project_id":"","workspace":null}`), &request); err != nil {
		t.Fatalf("representation decode failed: %v", err)
	}
	if err := request.Validate(); err == nil {
		t.Fatal("semantically invalid request validated")
	}
}

func TestAttachProjectRequestUnmarshalRetainsStrictPrivateSelectorConstruction(t *testing.T) {
	var request AttachProjectRequest
	if err := json.Unmarshal([]byte(`{"project_id":"project","workspace":{"kind":"workspace_id","workspace_root":"/tmp"}}`), &request); err == nil {
		t.Fatal("invalid private selector state decoded successfully")
	}
}
