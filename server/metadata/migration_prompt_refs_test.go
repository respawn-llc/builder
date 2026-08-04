package metadata

import "testing"

func TestMigrationPriorParameterReferencesAcceptHistoricalPromptShapes(t *testing.T) {
	refs, err := migrationPriorParameterReferences(
		"{{.TaskTitle}} {{.Inputs.summary}} {{.Params.summary}} {{.Params.review.summary}}",
	)
	if err != nil {
		t.Fatalf("migrationPriorParameterReferences: %v", err)
	}
	if len(refs) != 1 ||
		string(refs[0].TransitionKey) != "review" ||
		refs[0].ParameterKey != "summary" ||
		refs[0].Placeholder != ".Params.review.summary" {
		t.Fatalf("historical prior references = %+v, want only prior .Params reference", refs)
	}
}

func TestMigrationPriorParameterReferencesAcceptFunctionWrappedHistoricalReferences(t *testing.T) {
	refs, err := migrationPriorParameterReferences(`{{printf "%s" .Inputs.summary}} {{printf "%s" .Params.review.summary}}`)
	if err != nil {
		t.Fatalf("migrationPriorParameterReferences: %v", err)
	}
	if len(refs) != 1 ||
		string(refs[0].TransitionKey) != "review" ||
		refs[0].ParameterKey != "summary" ||
		refs[0].Placeholder != ".Params.review.summary" {
		t.Fatalf("function-wrapped historical prior references = %+v, want one prior .Params reference", refs)
	}
}

func TestMigrationPriorParameterReferencesRejectUnsupportedHistoricalPromptShapes(t *testing.T) {
	for _, prompt := range []string{
		"{{.Inputs}}",
		"{{.Inputs.summary.more}}",
		"{{.Params}}",
		"{{.Params.review.summary.extra}}",
		"{{.Nodes.review.summary}}",
		`{{index .Params "summary"}}`,
		"{{.TaskTitle.more}}",
	} {
		t.Run(prompt, func(t *testing.T) {
			if _, err := migrationPriorParameterReferences(prompt); err == nil {
				t.Fatalf("migrationPriorParameterReferences(%q) accepted unsupported shape", prompt)
			}
		})
	}
}
