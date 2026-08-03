package runtimeinput

import "testing"

func TestInputAndCanonicalCommandProjection(t *testing.T) {
	input := Command("prompt:review", " src ")
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	if got, err := input.CanonicalHistoryText(); err != nil || got != "/review src" {
		t.Fatalf("history = %q, %v", got, err)
	}
	builtin := BuiltinCommand(BuiltinPromptCommandReview, " src ")
	if got, err := builtin.CanonicalHistoryText(); err != nil || got != "/review src" {
		t.Fatalf("built-in history = %q, %v", got, err)
	}
}

func TestCommandTokenParsesNamespaceStructurally(t *testing.T) {
	token, err := ParseCommandToken("prompt:review")
	if err != nil {
		t.Fatal(err)
	}
	if token.Namespace != NamespacePrompt || token.Identifier == nil || *token.Identifier != "review" {
		t.Fatalf("token = %+v", token)
	}
	if _, err := ParsePromptCommandName("prompt:Review"); err == nil {
		t.Fatal("noncanonical prompt command name validated")
	}
	for _, name := range []string{" prompt:review", "prompt:review "} {
		if _, err := ParsePromptCommandName(name); err == nil {
			t.Fatalf("prompt command name with outer whitespace validated: %q", name)
		}
	}
}

func TestNormalizeIdentifierReportsUnformableInput(t *testing.T) {
	if _, err := NormalizeIdentifier("___"); err == nil {
		t.Fatal("unformable identifier was accepted")
	}
	if got, err := NormalizeIdentifier("Review Plan"); err != nil || got != "review_plan" {
		t.Fatalf("normalized identifier = %q, %v", got, err)
	}
}
