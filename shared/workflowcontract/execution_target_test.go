package workflowcontract

import (
	"encoding/json"
	"testing"
)

func TestExecutionTargetSelectionValidation(t *testing.T) {
	customRef := "release/v1"
	otherRef := "release/v2"
	blankRef := " "

	tests := []struct {
		name      string
		selection ExecutionTargetSelection
		wantErr   bool
	}{
		{name: "none", selection: ExecutionTargetSelection{Mode: ExecutionTargetModeNone}},
		{name: "head", selection: ExecutionTargetSelection{Mode: ExecutionTargetModeHead}},
		{name: "default branch", selection: ExecutionTargetSelection{Mode: ExecutionTargetModeDefaultBranch}},
		{name: "custom ref", selection: ExecutionTargetSelection{Mode: ExecutionTargetModeCustomRef, CustomRef: &customRef}},
		{name: "ask on first execution cannot be selected", selection: ExecutionTargetSelection{Mode: ExecutionTargetModeAskOnFirstExecution}, wantErr: true},
		{name: "custom selection requires ref", selection: ExecutionTargetSelection{Mode: ExecutionTargetModeCustomRef}, wantErr: true},
		{name: "custom selection rejects blank ref", selection: ExecutionTargetSelection{Mode: ExecutionTargetModeCustomRef, CustomRef: &blankRef}, wantErr: true},
		{name: "concrete non custom selection rejects ref", selection: ExecutionTargetSelection{Mode: ExecutionTargetModeHead, CustomRef: &customRef}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.selection.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}

	if !(ExecutionTargetSelection{Mode: ExecutionTargetModeCustomRef, CustomRef: &customRef}).Equal(
		ExecutionTargetSelection{Mode: ExecutionTargetModeCustomRef, CustomRef: &customRef},
	) {
		t.Fatal("equal selections differ")
	}
	if (ExecutionTargetSelection{Mode: ExecutionTargetModeCustomRef, CustomRef: &customRef}).Equal(
		ExecutionTargetSelection{Mode: ExecutionTargetModeCustomRef, CustomRef: &otherRef},
	) {
		t.Fatal("different selections compare equal")
	}
}

func TestExecutionTargetSelectionJSONEncoding(t *testing.T) {
	customRef := "refs/heads/release"
	for _, test := range []struct {
		name      string
		selection ExecutionTargetSelection
		want      string
	}{
		{name: "without custom ref", selection: ExecutionTargetSelection{Mode: ExecutionTargetModeHead}, want: `{"mode":"head"}`},
		{name: "with custom ref", selection: ExecutionTargetSelection{Mode: ExecutionTargetModeCustomRef, CustomRef: &customRef}, want: `{"mode":"custom_ref","custom_ref":"refs/heads/release"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.selection)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(encoded) != test.want {
				t.Fatalf("JSON = %s, want %s", encoded, test.want)
			}

			var decoded ExecutionTargetSelection
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !decoded.Equal(test.selection) {
				t.Fatalf("decoded = %+v, want %+v", decoded, test.selection)
			}
		})
	}
}
