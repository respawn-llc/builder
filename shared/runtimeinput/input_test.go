package runtimeinput

import "testing"

func TestInputAndCanonicalCommandProjection(t *testing.T) {
	input := Command("prompt:review", " src ")
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	if got, err := input.CanonicalHistoryText(); err != nil || got != "/prompt:review src" {
		t.Fatalf("history = %q, %v", got, err)
	}
}

func TestCommandTokenParsesNamespaceStructurally(t *testing.T) {
	token, err := ParseCommandToken("prompt:review")
	if err != nil {
		t.Fatal(err)
	}
	if token.Namespace != "prompt" || token.Identifier != "review" {
		t.Fatalf("token = %+v", token)
	}
	if _, err := ParsePromptCommandName("prompt:Review"); err == nil {
		t.Fatal("noncanonical prompt command name validated")
	}
}
