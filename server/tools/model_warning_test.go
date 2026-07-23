package tools

import (
	"encoding/json"
	"testing"

	"core/shared/toolspec"
)

func TestMaterializeModelWarningsAppendsWarningToSuccessfulResult(t *testing.T) {
	result := MaterializeModelWarnings(Result{
		CallID: "call",
		Name:   toolspec.ToolPatch,
		Output: json.RawMessage(`{"ok":true}`),
		ModelWarnings: []ModelWarning{
			ForeignManagedWorktreeEditWarning(),
		},
	})
	var output map[string]json.RawMessage
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("decode materialized result: %v", err)
	}
	if _, ok := output["ok"]; !ok {
		t.Fatalf("original successful result was not preserved: %s", result.Output)
	}
	if _, ok := output["warning"]; !ok {
		t.Fatalf("model warning was not materialized: %s", result.Output)
	}
	if len(result.ModelWarnings) != 0 {
		t.Fatalf("transient warnings remain after materialization: %+v", result.ModelWarnings)
	}
}
