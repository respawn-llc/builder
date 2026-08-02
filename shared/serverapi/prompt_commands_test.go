package serverapi

import "testing"

func TestPromptCommandCatalogResponseValidatesNamesAndUnicodePreviewLimit(t *testing.T) {
	response := PromptCommandCatalogResponse{Commands: []PromptCommandCatalogEntry{
		{Name: "prompt:review", Preview: "review"},
	}}
	if err := response.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	response.Commands = append(response.Commands, PromptCommandCatalogEntry{Name: "prompt:review"})
	if err := response.Validate(); err == nil {
		t.Fatal("duplicate catalog entry validated")
	}
}

func TestPromptCommandErrorValidation(t *testing.T) {
	command := "prompt:missing"
	if err := (&PromptCommandError{Kind: PromptCommandErrorKindCommandNotFound, Command: &command}).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
