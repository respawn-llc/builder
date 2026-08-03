package textutil

import "testing"

func TestExpandPromptTemplateDoesNotReplaceEmbeddedArgumentsPlaceholder(t *testing.T) {
	prompt := "prefix$ARGUMENTS $ARGUMENTS_SUFFIX"
	if got := ExpandPromptTemplate(prompt, " src "); got != prompt+"\n\nsrc" {
		t.Fatalf("embedded placeholder expansion = %q", got)
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

func TestExpandPromptTemplateTreatsUnicodeAdjacentTextAsLiteral(t *testing.T) {
	prompt := "Review $ARGUMENTSé"
	if got := ExpandPromptTemplate(prompt, "src"); got != prompt+"\n\nsrc" {
		t.Fatalf("unicode-adjacent placeholder expansion = %q", got)
	}
}

func TestExpandPromptTemplateTreatsUnicodeConnectorPunctuationAsLiteral(t *testing.T) {
	prompt := "Review $ARGUMENTS‿suffix"
	if got := ExpandPromptTemplate(prompt, "src"); got != prompt+"\n\nsrc" {
		t.Fatalf("connector-adjacent placeholder expansion = %q", got)
	}
}
