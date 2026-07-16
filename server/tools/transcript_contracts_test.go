package tools

import (
	"encoding/json"
	"reflect"
	"testing"

	"core/shared/toolspec"
	patchformat "core/shared/transcript/patchformat"
)

func TestFormatAskQuestionToolOutputRejectsPresentZeroSelection(t *testing.T) {
	if formatted, ok := formatAskQuestionToolOutput([]byte(`{"selected_option_number":0,"freeform_answer":"typed"}`)); ok || formatted != "" {
		t.Fatalf("present zero selection formatted as %q, %t; want malformed payload rejection", formatted, ok)
	}
}

func TestFormatAskQuestionToolOutputAcceptsLegacyOmittedSelection(t *testing.T) {
	formatted, ok := formatAskQuestionToolOutput([]byte(`{"freeform_answer":"typed"}`))
	if !ok || formatted == "" {
		t.Fatalf("legacy omitted selection formatted as %q, %t; want freeform answer", formatted, ok)
	}
}

func TestPatchToolCallMetaUsesOneRawFallbackForSupportedInvocationShapes(t *testing.T) {
	patch, ok := DefinitionFor(toolspec.ToolPatch)
	if !ok {
		t.Fatalf("expected %s definition", toolspec.ToolPatch)
	}
	input := "not a structured patch\nsecond raw line"
	oracle := patchformat.Raw(input)
	stringShape, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal string shape: %v", err)
	}
	objectShape, err := json.Marshal(struct {
		Patch string `json:"patch"`
	}{Patch: input})
	if err != nil {
		t.Fatalf("marshal object shape: %v", err)
	}

	var metas []struct {
		detail  string
		compact string
		render  *patchformat.RenderedPatch
	}
	for _, raw := range []json.RawMessage{stringShape, objectShape} {
		meta := patch.BuildToolCallMeta(ToolCallContext{WorkingDir: "/workspace"}, raw)
		if meta.PatchRender == nil {
			t.Fatalf("supported malformed patch shape used default metadata: %+v", meta)
		}
		if len(meta.PatchRender.Files) != 0 {
			t.Fatalf("raw fallback exposed structured files: %+v", meta.PatchRender.Files)
		}
		if !reflect.DeepEqual(*meta.PatchRender, oracle) {
			t.Fatalf("raw fallback = %+v, want oracle %+v", *meta.PatchRender, oracle)
		}
		if meta.Command != meta.PatchDetail || meta.CompactText != meta.PatchSummary {
			t.Fatalf("raw fallback aliases are inconsistent: %+v", meta)
		}
		metas = append(metas, struct {
			detail  string
			compact string
			render  *patchformat.RenderedPatch
		}{
			detail:  meta.PatchDetail,
			compact: meta.PatchSummary,
			render:  meta.PatchRender,
		})
	}
	if !reflect.DeepEqual(metas[0], metas[1]) {
		t.Fatalf("supported invocation shapes produced different raw metadata: left=%+v right=%+v", metas[0], metas[1])
	}
}

func TestPatchToolCallMetaReservesDefaultFallbackForMalformedOrBlankInvocation(t *testing.T) {
	patch, ok := DefinitionFor(toolspec.ToolPatch)
	if !ok {
		t.Fatalf("expected %s definition", toolspec.ToolPatch)
	}
	tests := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "malformed JSON", raw: json.RawMessage(`{`)},
		{name: "blank string", raw: json.RawMessage(`"   "`)},
		{name: "blank object", raw: json.RawMessage(`{"patch":"\n\t"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := patch.BuildToolCallMeta(ToolCallContext{WorkingDir: "/workspace"}, test.raw)
			if meta.PatchRender != nil {
				t.Fatalf("default fallback unexpectedly carried a patch render: %+v", meta.PatchRender)
			}
		})
	}
}
