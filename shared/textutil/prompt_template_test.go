package textutil

import "testing"

func TestExpandPromptTemplateReplacesLiteralArgumentsOccurrence(t *testing.T) {
	if got := ExpandPromptTemplate("prefix$ARGUMENTS_SUFFIX", " src "); got != "prefixsrc_SUFFIX" {
		t.Fatalf("literal replacement = %q", got)
	}
}

func TestExpandPromptTemplateReplacesEveryArgumentsOccurrence(t *testing.T) {
	if got := ExpandPromptTemplate("Review $ARGUMENTS twice: $ARGUMENTS", " src "); got != "Review src twice: src" {
		t.Fatalf("repeated replacement = %q", got)
	}
}

func TestExpandPromptTemplateAppendsArgumentsAfterBlankLine(t *testing.T) {
	if got := ExpandPromptTemplate("Review", " src "); got != "Review\n\nsrc" {
		t.Fatalf("append = %q", got)
	}
}

func TestExpandPromptTemplateLeavesPromptUnchangedForEmptyArguments(t *testing.T) {
	if got := ExpandPromptTemplate("Review", " "); got != "Review" {
		t.Fatalf("empty args = %q", got)
	}
}
