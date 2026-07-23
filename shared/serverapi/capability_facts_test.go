package serverapi

import (
	"reflect"
	"testing"
)

func TestCapabilityFactsRequestValidation(t *testing.T) {
	if err := (CapabilityFactsRequest{}).Validate(); err != nil {
		t.Fatalf("absent workspace root rejected: %v", err)
	}
	workspaceRoot := "/tmp/workspace"
	if err := (CapabilityFactsRequest{WorkspaceRoot: &workspaceRoot}).Validate(); err != nil {
		t.Fatalf("workspace root rejected: %v", err)
	}
	blankWorkspaceRoot := " \t "
	if err := (CapabilityFactsRequest{WorkspaceRoot: &blankWorkspaceRoot}).Validate(); err == nil {
		t.Fatal("blank supplied workspace root accepted")
	}
	if err := (CapabilityFactsRequest{ExplicitLLMProviderIDs: []string{"openai", " "}}).Validate(); err == nil {
		t.Fatal("blank explicit provider id accepted")
	}
}

func TestCapabilityFactsRequestNormalizesExplicitProviders(t *testing.T) {
	req := CapabilityFactsRequest{ExplicitLLMProviderIDs: []string{" OpenAI ", "openai", "ANTHROPIC", "anthropic "}}

	got := req.NormalizedExplicitLLMProviderIDs()

	want := []string{"OpenAI", "ANTHROPIC"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized explicit provider ids = %#v, want %#v", got, want)
	}
}
