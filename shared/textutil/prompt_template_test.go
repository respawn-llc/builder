package textutil

import "testing"

func TestExpandPromptTemplateReplacesEveryArgumentsTokenOrAppendsTrailingArguments(t *testing.T) {
	if got := ExpandPromptTemplate("Review $ARGUMENTS twice: $ARGUMENTS", " src "); got != "Review src twice: src" {
		t.Fatalf("replacement = %q", got)
	}
	if got := ExpandPromptTemplate("Review", " src "); got != "Review\n\nsrc" {
		t.Fatalf("append = %q", got)
	}
	if got := ExpandPromptTemplate("Review", " "); got != "Review" {
		t.Fatalf("empty args = %q", got)
	}
}
