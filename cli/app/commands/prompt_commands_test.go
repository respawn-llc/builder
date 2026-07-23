package commands

import "testing"

func TestBuildPromptSubmissionAppendsTrimmedArgsWhenNoPlaceholder(t *testing.T) {
	prompt := "# review\nbody\n"
	got := buildPromptSubmission(prompt, "  src/internal  ")
	want := "# review\nbody\n\nsrc/internal"
	if got != want {
		t.Fatalf("unexpected prompt submission\nwant: %q\ngot:  %q", want, got)
	}
}

func TestBuildPromptSubmissionReplacesArgumentsPlaceholder(t *testing.T) {
	prompt := "# review\nFocus on $ARGUMENTS\nRepeat: $ARGUMENTS\n"
	got := buildPromptSubmission(prompt, "  retry logic  ")
	want := "# review\nFocus on retry logic\nRepeat: retry logic\n"
	if got != want {
		t.Fatalf("unexpected prompt submission\nwant: %q\ngot:  %q", want, got)
	}
}

func TestBuildPromptSubmissionReturnsArgsForEmptyPrompt(t *testing.T) {
	got := buildPromptSubmission("", "  src/internal  ")
	if got != "src/internal" {
		t.Fatalf("unexpected prompt submission: %q", got)
	}
}
