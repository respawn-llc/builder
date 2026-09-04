package protoapi

import (
	"testing"

	"core/shared/serverapi"
)

func TestRunPromptOverridesToProtoRejectsBlankTheme(t *testing.T) {
	theme := "  "
	if _, err := RunPromptOverridesToProto(serverapi.RunPromptOverrides{Theme: &theme}); err == nil {
		t.Fatal("expected blank Theme to be rejected")
	}
}

func TestRunPromptOverridesToProtoPreservesPresentTheme(t *testing.T) {
	theme := " dark "
	message, err := RunPromptOverridesToProto(serverapi.RunPromptOverrides{Theme: &theme})
	if err != nil {
		t.Fatalf("RunPromptOverridesToProto: %v", err)
	}
	if message.Theme == nil || *message.Theme != "dark" {
		t.Fatalf("Theme = %v, want trimmed present value", message.Theme)
	}
}
