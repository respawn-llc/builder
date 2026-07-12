package shell

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"core/server/tools/shell/postprocess"
)

func TestBackgroundEventCompletionOutputApplicability(t *testing.T) {
	exitCode := 0
	snapshot := Snapshot{ID: "1000", State: "completed", ExitCode: &exitCode}

	backgrounded := newBackgroundedEvent(snapshot)
	if backgrounded.completion != nil {
		t.Fatal("background transition must not carry completion output")
	}

	finalized := newFinalizedBackgroundEvent(EventCompleted, snapshot, "", nil, false)
	if finalized.completion == nil {
		t.Fatal("completed event must carry completion output")
	}
	if finalized.completion.source != completionOutputFinalized {
		t.Fatalf("finalized event source = %d", finalized.completion.source)
	}

	fallback := newFallbackBackgroundEvent(EventKilled, snapshot, "preview", nil, 1, false)
	if fallback.completion == nil {
		t.Fatal("killed event must carry completion output")
	}
	if fallback.completion.source != completionOutputFallback {
		t.Fatalf("fallback event source = %d", fallback.completion.source)
	}

	_, err := SummarizeBackgroundEvent(Event{Type: EventCompleted, Snapshot: snapshot}, BackgroundNoticeOptions{})
	var invalid *InvalidBackgroundEventError
	if !errors.As(err, &invalid) {
		t.Fatalf("terminal event without completion output error = %v, want typed invalid event error", err)
	}
	if invalid.EventType != EventCompleted || invalid.ProcessID != snapshot.ID || invalid.State != snapshot.State {
		t.Fatalf("invalid event error = %+v", invalid)
	}
}

func TestBackgroundNoticeFinalizedOutputKeepsRetainedLogMetadata(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "1000.log")
	lines := strings.Repeat("line\n", 100)
	if err := os.WriteFile(logPath, []byte(lines), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	exitCode := 0
	event := newFinalizedBackgroundEvent(EventCompleted, Snapshot{
		ID:       "1000",
		State:    "completed",
		LogPath:  logPath,
		ExitCode: &exitCode,
	}, "semantic replacement", nil, false)

	summary, err := SummarizeBackgroundEvent(event, BackgroundNoticeOptions{MaxChars: 80, SuccessOutputMode: BackgroundOutputDefault})
	if err != nil {
		t.Fatalf("SummarizeBackgroundEvent: %v", err)
	}
	if summary.output.source != completionOutputFinalized {
		t.Fatalf("notice output source = %d, want finalized", summary.output.source)
	}
	if summary.output.inlinePreview == nil {
		t.Fatal("expected finalized output inline preview")
	}
	if !summary.output.retainedLogHasOutput {
		t.Fatal("expected retained-log output")
	}
	if summary.output.logLineCount == nil || *summary.output.logLineCount != 100 {
		t.Fatalf("retained log line count = %v, want 100", summary.output.logLineCount)
	}
	if summary.LineCount != 100 {
		t.Fatalf("summary line count = %d, want 100", summary.LineCount)
	}
	if summary.Truncated {
		t.Fatal("semantic replacement must not inherit retained-log truncation")
	}

	concise, err := SummarizeBackgroundEvent(event, BackgroundNoticeOptions{MaxChars: 80, SuccessOutputMode: BackgroundOutputConcise})
	if err != nil {
		t.Fatalf("SummarizeBackgroundEvent concise: %v", err)
	}
	if concise.output.inlinePreview != nil {
		t.Fatal("concise mode must suppress only command inline preview")
	}
	if concise.output.logLineCount == nil || *concise.output.logLineCount != 100 {
		t.Fatalf("concise retained log line count = %v, want 100", concise.output.logLineCount)
	}
}

func TestBackgroundNoticeWarningOnlyOutputDoesNotClaimLogContent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "1000.log")
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatalf("write empty log: %v", err)
	}
	warning, err := postprocess.NewWarning("recoverable warning")
	if err != nil {
		t.Fatalf("NewWarning: %v", err)
	}
	exitCode := 0
	event := newFinalizedBackgroundEvent(EventCompleted, Snapshot{
		ID:       "1000",
		State:    "completed",
		LogPath:  logPath,
		ExitCode: &exitCode,
	}, "", warning, false)

	summary, err := SummarizeBackgroundEvent(event, BackgroundNoticeOptions{SuccessOutputMode: BackgroundOutputConcise})
	if err != nil {
		t.Fatalf("SummarizeBackgroundEvent: %v", err)
	}
	if !summary.output.visible.HasVisibleContent() {
		t.Fatal("warning-only completion must be visible output")
	}
	if summary.output.visible.Warning() == nil {
		t.Fatal("warning-only completion must retain typed warning")
	}
	if summary.output.visible.HasCommandContent() {
		t.Fatal("warning-only completion must not acquire command output")
	}
	if summary.output.retainedLogHasOutput || summary.output.logLineCount != nil {
		t.Fatalf("warning-only completion must not claim retained log content: %+v", summary.output)
	}
}

func TestBackgroundNoticeWhitespaceLogRendersNoOutputCompletion(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "1000.log")
	if err := os.WriteFile(logPath, []byte("\n"), 0o644); err != nil {
		t.Fatalf("write whitespace log: %v", err)
	}
	exitCode := 0
	event := newFinalizedBackgroundEvent(EventCompleted, Snapshot{
		ID:       "1000",
		State:    "completed",
		LogPath:  logPath,
		ExitCode: &exitCode,
	}, "", nil, false)

	summary, err := SummarizeBackgroundEvent(event, BackgroundNoticeOptions{SuccessOutputMode: BackgroundOutputDefault})
	if err != nil {
		t.Fatalf("SummarizeBackgroundEvent: %v", err)
	}
	if summary.output.logLineCount == nil || *summary.output.logLineCount != 1 {
		t.Fatalf("whitespace log metadata = %+v, want one retained line", summary.output)
	}
	if !summary.output.shouldRenderNoOutputCompletion(&exitCode) {
		t.Fatalf("whitespace-only completion must render explicit no-output state: %+v", summary.output)
	}
	if sections := len(strings.Split(summary.DetailText, "\n")); sections != 4 {
		t.Fatalf("whitespace completion detail sections = %d, want 4", sections)
	}
}

func TestBackgroundNoticeFallbackCarriesTruncationWithoutFinalizedState(t *testing.T) {
	exitCode := 1
	event := newFallbackBackgroundEvent(EventCompleted, Snapshot{
		ID:       "1000",
		State:    "completed",
		ExitCode: &exitCode,
	}, strings.Repeat("x", 200), nil, 1, false)

	summary, err := SummarizeBackgroundEvent(event, BackgroundNoticeOptions{MaxChars: 80, SuccessOutputMode: BackgroundOutputDefault})
	if err != nil {
		t.Fatalf("SummarizeBackgroundEvent: %v", err)
	}
	if summary.output.source != completionOutputFallback {
		t.Fatalf("notice output source = %d, want fallback", summary.output.source)
	}
	if summary.output.inlinePreview == nil {
		t.Fatal("expected fallback inline preview")
	}
	if !summary.output.truncated || !summary.Truncated {
		t.Fatal("expected fallback truncation")
	}
}

func TestInvariantFailureBackgroundNoticeUsesDistinctTypedProjection(t *testing.T) {
	exitCode := 17
	summary := InvariantFailureBackgroundNotice(Event{
		Type: EventCompleted,
		Snapshot: Snapshot{
			ID:       "1000",
			State:    "completed",
			ExitCode: &exitCode,
			LogPath:  "/tmp/1000.log",
		},
	}, errors.New("missing completion output"))

	if summary.output.kind != backgroundNoticeOutputInvariantFailure {
		t.Fatalf("notice output kind = %d, want invariant failure", summary.output.kind)
	}
	if summary.output.invariantFailure == nil || summary.output.invariantFailure.message == "" {
		t.Fatalf("invariant failure projection = %+v", summary.output.invariantFailure)
	}
	if summary.DetailText == "" || summary.CondensedText == "" {
		t.Fatal("invariant failure notice must remain visible through the ordinary notice envelope")
	}
}
