package llm

import (
	"core/shared/textutil"
	"strings"
	"testing"
)

func TestNormalizeReasoningSummaryTextPreservesBoldMarkers(t *testing.T) {
	text := normalizeReasoningSummaryLines(strings.Split(strings.ReplaceAll("**Preparing patch**\n\nI am exploring options.\n**Running checks**", "\r\n", "\n"), "\n"))
	if text != "**Preparing patch**\n\nI am exploring options.\n**Running checks**" {
		t.Fatalf("unexpected normalized text: %q", text)
	}
}

func TestReasoningSummaryDeltaFromTextCarriesCurrentStatus(t *testing.T) {
	delta := reasoningSummaryDeltaFromText("rs_1:summary:0", "reasoning", "**Checking tests**")
	if delta.Text != "**Checking tests**" {
		t.Fatalf("unexpected delta text: %q", delta.Text)
	}
	if delta.CurrentStatus == nil || delta.CurrentStatus.Text != "Checking tests" {
		t.Fatalf("unexpected current status: %+v", delta.CurrentStatus)
	}
}

func TestReasoningSummaryDeltaFromTextPreservesRawText(t *testing.T) {
	text := "\r\n\r\n**Checking tests**\r\n\r\n\r\nDetails\r\n"
	delta := reasoningSummaryDeltaFromText("rs_1:summary:0", "reasoning", text)
	if delta.Text != text {
		t.Fatalf("delta text = %q, want raw %q", delta.Text, text)
	}
}

func TestReasoningSummaryDeltaFromTextRejectsIncompleteStatus(t *testing.T) {
	delta := reasoningSummaryDeltaFromText("rs_1:summary:0", "reasoning", "**Checking tests")
	if delta.CurrentStatus != nil {
		t.Fatalf("unexpected current status: %+v", delta.CurrentStatus)
	}
}

func TestReasoningSummaryDeltaFromTextRejectsEmptyStatus(t *testing.T) {
	for _, text := range []string{"****", "**   **"} {
		delta := reasoningSummaryDeltaFromText("rs_1:summary:0", "reasoning", text)
		if delta.CurrentStatus != nil {
			t.Fatalf("text %q produced unexpected current status: %+v", text, delta.CurrentStatus)
		}
	}
}

func TestReasoningSummaryDeltaFromTextRejectsNonStrongMarkdown(t *testing.T) {
	for _, text := range []string{"Checking tests", "`**Checking tests**`"} {
		delta := reasoningSummaryDeltaFromText("rs_1:summary:0", "reasoning", text)
		if delta.CurrentStatus != nil {
			t.Fatalf("text %q produced unexpected current status: %+v", text, delta.CurrentStatus)
		}
	}
}

func TestReasoningSummaryDeltaFromTextRejectsLinkedStatus(t *testing.T) {
	for _, text := range []string{
		"[**Checking tests**](https://example.com)",
		"**[Checking tests](https://example.com)**",
	} {
		delta := reasoningSummaryDeltaFromText("rs_1:summary:0", "reasoning", text)
		if delta.CurrentStatus != nil {
			t.Fatalf("text %q produced unexpected current status: %+v", text, delta.CurrentStatus)
		}
	}
}

func TestReasoningSummaryDeltaFromTextRejectsStatusContainingNestedMarkup(t *testing.T) {
	for _, text := range []string{
		"**Checking _tests_**",
		"**`Checking tests`**",
		"**Checking ~~tests~~**",
	} {
		delta := reasoningSummaryDeltaFromText("rs_1:summary:0", "reasoning", text)
		if delta.CurrentStatus != nil {
			t.Fatalf("text %q produced unexpected current status: %+v", text, delta.CurrentStatus)
		}
	}
}

func TestReasoningSummaryDeltaFromTextUsesFirstValidStatus(t *testing.T) {
	delta := reasoningSummaryDeltaFromText(
		"rs_1:summary:0",
		"reasoning",
		"**[ignored](https://example.com)** then **Checking tests** then **Writing summary**",
	)
	if delta.CurrentStatus == nil || delta.CurrentStatus.Text != "Checking tests" {
		t.Fatalf("unexpected current status: %+v", delta.CurrentStatus)
	}
}

func TestReasoningSummaryDeltaFromTextTrimsStatusWhitespace(t *testing.T) {
	delta := reasoningSummaryDeltaFromText("rs_1:summary:0", "reasoning", "\n  **Checking tests**  \n")
	if delta.CurrentStatus == nil || delta.CurrentStatus.Text != "Checking tests" {
		t.Fatalf("unexpected current status: %+v", delta.CurrentStatus)
	}
}

func TestNormalizeReasoningEntriesKeepsBoldOnlyReasoningEntries(t *testing.T) {
	got := normalizeReasoningEntries([]ReasoningEntry{{Role: textutil.Value("reasoning"), Text: "**Preparing patch**"}})
	if len(got) != 1 || got[0].Text != "**Preparing patch**" {
		t.Fatalf("expected bold-only reasoning entry preserved, got %+v", got)
	}
}
