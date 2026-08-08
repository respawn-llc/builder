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
	for _, name := range []string{" prompt:preview", "prompt:preview "} {
		t.Run("noncanonical name "+name, func(t *testing.T) {
			invalid := PromptCommandCatalogResponse{Commands: []PromptCommandCatalogEntry{{Name: name, Preview: "preview"}}}
			if err := invalid.Validate(); err == nil {
				t.Fatalf("name %q validated", name)
			}
		})
	}
	for _, preview := range []string{"", " leading", "trailing ", "two  spaces", "line\nbreak", "tab\tbreak"} {
		t.Run("invalid preview "+preview, func(t *testing.T) {
			invalid := PromptCommandCatalogResponse{Commands: []PromptCommandCatalogEntry{{Name: "prompt:preview", Preview: preview}}}
			if err := invalid.Validate(); err == nil {
				t.Fatalf("preview %q validated", preview)
			}
		})
	}
}

func TestPromptCommandErrorValidation(t *testing.T) {
	command := "prompt:missing"
	if err := (&PromptCommandError{Kind: PromptCommandErrorKindCommandNotFound, Command: &command}).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
