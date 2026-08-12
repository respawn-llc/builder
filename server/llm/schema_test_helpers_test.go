package llm

import (
	"testing"

	"core/shared/jsoncontract"
)

type testReviewerStructuredOutput struct {
	Suggestions []string `json:"suggestions"`
}

type testWorkflowStructuredOutput struct {
	Transition string  `json:"transition"`
	Commentary *string `json:"commentary" jsonschema:"nullable"`
	Risk       *string `json:"risk" jsonschema:"nullable"`
	Summary    *string `json:"summary" jsonschema:"nullable"`
}

type testNestedFunctionInput struct {
	Question string `json:"question"`
	Meta     struct {
		Foo string `json:"foo"`
	} `json:"meta"`
}

func mustTestFunctionSchema(t testing.TB, source any) jsoncontract.Function {
	t.Helper()
	schema, err := jsoncontract.NewPreparer(false).Function("llm test function", source)
	if err != nil {
		t.Fatalf("prepare test function schema: %v", err)
	}
	return schema
}

func mustTestStructuredSchema(t testing.TB, source any) jsoncontract.Structured {
	t.Helper()
	schema, err := jsoncontract.NewPreparer(false).Structured("llm test structured output", source)
	if err != nil {
		t.Fatalf("prepare test structured schema: %v", err)
	}
	return schema
}
