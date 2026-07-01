package app

import (
	"bytes"
	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"
	"core/shared/clientui"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestApplyRuntimeTranscriptPageAcceptsEqualRevisionTailReplacementWhenAuthoritativePageCorrectsOverlap(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 100, 20, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "user", Text: "prompt"}},
	}
	exitMainThread := m.enterUIMainThread("native equal-revision baseline")
	cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone)
	exitMainThread()
	if cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	out.Reset()

	exitMainThread = m.enterUIMainThread("native equal-revision stale tool append")
	cmd, mutated, needsHydration := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{Kind: clientui.EventToolCallStarted, TranscriptEntries: []clientui.ChatEntry{{
		Role:       "tool_call",
		Text:       "pwd",
		ToolCallID: "stale-call",
		ToolCall:   &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
	}}})
	exitMainThread()
	if cmd != nil || !mutated || needsHydration {
		t.Fatalf("expected live append without extra command, mutated=%t needsHydration=%t cmd=%v", mutated, needsHydration, cmd)
	}
	if !m.transcriptLiveDirty {
		t.Fatal("expected live append to mark transcript live-dirty")
	}
	if plain := stripANSIAndTrimRight(out.String()); !strings.Contains(plain, "pwd") {
		t.Fatalf("native stable did not write stale pending tool before authoritative correction, got %q", plain)
	}
	out.Reset()

	corrected := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries: []clientui.ChatEntry{
			{Role: "user", Text: "prompt"},
			{Role: "tool_call", Text: "pwd", ToolCallID: "call-1", ToolCall: &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"}},
			{Role: "tool_result_ok", Text: "/tmp", ToolCallID: "call-1"},
		},
	}
	exitMainThread = m.enterUIMainThread("native equal-revision correction")
	cmd = m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, corrected, clientui.TranscriptRecoveryCauseNone)
	exitMainThread()
	if cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	if m.nativeLiveAreaError != nil {
		t.Fatalf("authoritative correction produced native surface error: %v", m.nativeLiveAreaError)
	}
	if m.nativeSurface == nil {
		t.Fatal("authoritative correction disabled native surface")
	}
	if plain := stripANSIAndTrimRight(out.String()); !strings.Contains(plain, "/tmp") {
		t.Fatalf("native stable did not append corrected tool result after stale pending row, got %q", plain)
	}

	if got, want := len(m.transcriptEntries), 3; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	if got := m.transcriptEntries[1].ToolCallID; got != "call-1" {
		t.Fatalf("corrected tool call id = %q, want call-1", got)
	}
	if got := m.transcriptEntries[2].ToolCallID; got != "call-1" {
		t.Fatalf("corrected tool result id = %q, want call-1", got)
	}
	if m.transcriptLiveDirty {
		t.Fatal("expected corrective equal-revision refresh to clear transcriptLiveDirty")
	}
	projection := m.nativeCommittedProjectionForEntries(m.transcriptEntries)
	if len(projection.Blocks) != 3 {
		t.Fatalf("projection block count = %d, want 3: %#v", len(projection.Blocks), projection.Blocks)
	}
	if got := projection.Blocks[1].Role; got != tui.RenderIntentToolShell {
		t.Fatalf("corrected projection tool call block role = %q, want %q: %#v", got, tui.RenderIntentToolShell, projection.Blocks[1])
	}
	if projection.Blocks[1].EntryIndex != 1 {
		t.Fatalf("corrected projection tool block entry index = %d, want 1: %#v", projection.Blocks[1].EntryIndex, projection.Blocks[1])
	}
	if got := projection.Blocks[2].Role; got != tui.RenderIntentToolSuccess {
		t.Fatalf("corrected projection tool result block role = %q, want %q: %#v", got, tui.RenderIntentToolSuccess, projection.Blocks[2])
	}
	if projection.Blocks[2].EntryIndex != 2 {
		t.Fatalf("corrected projection tool result block entry index = %d, want 2: %#v", projection.Blocks[2].EntryIndex, projection.Blocks[2])
	}
}

func TestApplyRuntimeTranscriptPageAcceptsEqualRevisionReplacementWhenToolMetadataChanges(t *testing.T) {
	m := newProjectedStaticUIModel()

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries: []clientui.ChatEntry{
			{Role: "user", Text: "prompt"},
			{Role: "tool_call", Text: "run", ToolCallID: "call-1", ToolCall: &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"}},
		},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	m.transcriptLiveDirty = true

	corrected := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries: []clientui.ChatEntry{
			{Role: "user", Text: "prompt"},
			{Role: "tool_call", Text: "run", ToolCallID: "call-1", ToolCall: &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "ls"}},
		},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, corrected, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	if got, want := len(m.transcriptEntries), 2; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	if m.transcriptEntries[1].ToolCall == nil {
		t.Fatalf("expected corrected tool metadata, got nil")
	}
	if got := m.transcriptEntries[1].ToolCall.Command; got != "ls" {
		t.Fatalf("tool command = %q, want ls", got)
	}
	if m.transcriptLiveDirty {
		t.Fatal("expected equal-revision metadata correction to clear transcriptLiveDirty")
	}
}

func TestProjectedAssistantToolCallEntriesApplyAsCommittedInRuntimeMode(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "user", Text: "prompt"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	toolStarted := clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_call",
			Text:       "pwd",
			ToolCallID: "call-1",
			ToolCall:   &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
		}},
	}
	_ = collectCmdMessages(t, m.runtimeAdapter().applyProjectedRuntimeEvent(toolStarted).cmd)

	if got, want := len(m.transcriptEntries), 2; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	if m.transcriptEntries[1].Transient || !m.transcriptEntries[1].Committed {
		t.Fatalf("expected runtime assistant tool call to apply as committed transcript state, got %+v", m.transcriptEntries[1])
	}
	if got := m.transcriptRevision; got != 11 {
		t.Fatalf("transcript revision = %d, want 11", got)
	}
}

func TestRuntimeAuthoritativeHydrateDoesNotRepairCommittedToolPathWhenLiveProjectionMatches(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "user", Text: "prompt"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	_ = collectCmdMessages(t, m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_call",
			Text:       "pwd",
			ToolCallID: "call-1",
			ToolCall:   &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
		}},
	}).
		cmd)
	_ = collectCmdMessages(t, m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventToolCallCompleted,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         12,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_result_ok",
			Text:       "/tmp",
			ToolCallID: "call-1",
		}},
	}).
		cmd)

	cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  12,
		Entries: []clientui.ChatEntry{
			{Role: "user", Text: "prompt"},
			{Role: "tool_call", Text: "pwd", ToolCallID: "call-1", ToolCall: &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"}},
			{Role: "tool_result_ok", Text: "/tmp", ToolCallID: "call-1"},
		},
	}, clientui.TranscriptRecoveryCauseNone)
	if cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	if m.transientStatus != "" {
		t.Fatalf("did not expect authoritative hydrate warning when live committed tool path already matches, got status=%q", m.transientStatus)
	}
	if got, want := len(m.transcriptEntries), 3; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	if !m.transcriptEntries[1].Committed || !m.transcriptEntries[2].Committed {
		t.Fatalf("expected tool path entries to remain committed for ordering after hydrate, got %+v", m.transcriptEntries)
	}
}

func TestRuntimeAuthoritativeHydrateDoesNotRepairCommittedReviewerStatusPathWhenLiveProjectionMatches(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	_ = collectCmdMessages(t, m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "reviewer_status",
			Text: "Supervisor ran and applied 2 suggestions.",
		}},
	}).
		cmd)

	if m.transcriptEntries[1].Transient || !m.transcriptEntries[1].Committed {
		t.Fatalf("expected reviewer status to apply as committed transcript state, got %+v", m.transcriptEntries[1])
	}

	cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  11,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "seed"},
			{Role: "reviewer_status", Text: "Supervisor ran and applied 2 suggestions."},
		},
	}, clientui.TranscriptRecoveryCauseNone)
	if cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	if m.transientStatus != "" {
		t.Fatalf("did not expect authoritative hydrate warning when reviewer status path already matches, got status=%q", m.transientStatus)
	}
	if got, want := len(m.transcriptEntries), 2; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	if !m.transcriptEntries[1].Committed {
		t.Fatalf("expected reviewer status to remain committed for ordering after hydrate, got %+v", m.transcriptEntries[1])
	}
}

func TestApplyRuntimeTranscriptPageAcceptsEqualRevisionTailReplacementWhenOngoingErrorChanged(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	m.transcriptLiveDirty = true

	runtimeOnly := clientui.TranscriptPage{
		SessionID:      "session-1",
		Revision:       10,
		Entries:        []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
		StreamingError: "background continuation failed",
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, runtimeOnly, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	if got := m.view.OngoingErrorText(); got != "background continuation failed" {
		t.Fatalf("ongoing error text = %q, want background continuation failed", got)
	}
	if m.transcriptLiveDirty {
		t.Fatal("expected accepted equal-revision ongoing-error refresh to clear transcriptLiveDirty")
	}
}

func TestApplyRuntimeTranscriptPageAcceptsEqualRevisionTailReplacementWhenOngoingErrorCleared(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID:      "session-1",
		Revision:       10,
		Entries:        []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
		StreamingError: "background continuation failed",
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	m.transcriptLiveDirty = true

	cleared := clientui.TranscriptPage{
		SessionID:      "session-1",
		Revision:       10,
		Entries:        []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
		StreamingError: "",
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, cleared, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	if got := m.view.OngoingErrorText(); got != "" {
		t.Fatalf("ongoing error text = %q, want empty", got)
	}
	if m.transcriptLiveDirty {
		t.Fatal("expected accepted equal-revision ongoing-error clear to clear transcriptLiveDirty")
	}
	if got := len(m.transcriptEntries); got != 1 {
		t.Fatalf("transcript entry count = %d, want 1", got)
	}
}

func TestApplyRuntimeTranscriptPageRejectsEqualRevisionShiftedTailReplacement(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "seed-0"},
			{Role: "assistant", Text: "seed-1"},
		},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	m.transcriptLiveDirty = true

	shifted := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed-1"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, shifted, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		if msg := cmd(); msg != nil {
			t.Fatalf("expected shifted equal-revision page to be ignored, got %T", msg)
		}
	}

	if got := m.transcriptBaseOffset; got != 0 {
		t.Fatalf("transcript base offset = %d, want 0", got)
	}
	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("transcript entry count = %d, want 2", got)
	}
	if got := m.transcriptEntries[0].Text; got != "seed-0" {
		t.Fatalf("first transcript entry = %q, want seed-0", got)
	}
	if !m.transcriptLiveDirty {
		t.Fatal("expected rejected shifted equal-revision page to preserve transcriptLiveDirty")
	}
}

func TestApplyRuntimeTranscriptPageAcceptsNewerRevisionTailReplacementAfterLiveAppend(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	if cmd, mutated, needsHydration := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{Kind: clientui.EventAssistantMessage, TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "live append"}}}); cmd != nil || !mutated || needsHydration {
		t.Fatalf("expected live append without extra command, mutated=%t needsHydration=%t cmd=%v", mutated, needsHydration, cmd)
	}

	fresh := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  11,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "seed"},
			{Role: "assistant", Text: "live append"},
		},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, fresh, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	if got := m.transcriptRevision; got != 11 {
		t.Fatalf("transcript revision = %d, want 11", got)
	}
	if m.transcriptLiveDirty {
		t.Fatal("expected fresh authoritative page to clear live-dirty state")
	}
	if got, want := len(m.transcriptEntries), 2; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	if got := m.transcriptEntries[1].Text; got != "live append" {
		t.Fatalf("second transcript entry = %q, want live append", got)
	}
}

func TestApplyProjectedTranscriptEntriesUsesTailOffsetWhileViewingOlderDetailPage(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	recentTail := clientui.TranscriptPage{SessionID: "session-1", HasMoreAbove: true}
	for i := 0; i < 200; i++ {
		recentTail.Entries = append(recentTail.Entries, clientui.ChatEntry{Role: "assistant", Text: fmt.Sprintf("tail %03d", 300+i)})
	}
	m.transcriptTotalEntries = 500
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, recentTail, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	olderDetailPage := clientui.TranscriptPage{SessionID: "session-1", HasMoreBelow: true}
	for i := 0; i < 300; i++ {
		olderDetailPage.Entries = append(olderDetailPage.Entries, clientui.ChatEntry{Role: "assistant", Text: fmt.Sprintf("history %03d", i)})
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{Cursor: 4096}, olderDetailPage, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	if m.view.TranscriptBaseOffset() != 0 {
		t.Fatalf("expected detail view to remain on older page, got base=%d", m.view.TranscriptBaseOffset())
	}
	if got := m.transcriptBaseOffset; got != 300 {
		t.Fatalf("live tail base offset = %d, want %d", got, 300)
	}

	appended := []clientui.ChatEntry{{Role: "assistant", Text: "tail 500"}, {Role: "assistant", Text: "tail 501"}}
	if cmd, mutated, needsHydration := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{Kind: clientui.EventAssistantMessage, TranscriptEntries: appended}); cmd != nil || !mutated || needsHydration {
		t.Fatalf("expected projected append to mutate without extra command, mutated=%t needsHydration=%t cmd=%v", mutated, needsHydration, cmd)
	}

	if got, want := len(m.transcriptEntries), 202; got != want {
		t.Fatalf("live tail entry count = %d, want %d", got, want)
	}
	if got := m.transcriptEntries[len(m.transcriptEntries)-2].Text; got != "tail 500" {
		t.Fatalf("expected first appended tail entry at live tail end, got %q", got)
	}
	if got := m.transcriptEntries[len(m.transcriptEntries)-1].Text; got != "tail 501" {
		t.Fatalf("expected second appended tail entry at live tail end, got %q", got)
	}
	if got, want := m.transcriptTotalEntries, 502; got != want {
		t.Fatalf("live tail total entries = %d, want %d", got, want)
	}
	if got, want := m.detailTranscript.totalEntries, 502; got != want {
		t.Fatalf("detail transcript total entries = %d, want %d", got, want)
	}
	if got, want := m.detailTranscript.offset, 0; got != want {
		t.Fatalf("detail transcript offset = %d, want %d", got, want)
	}
	if got, want := len(m.detailTranscript.entries), 502; got != want {
		t.Fatalf("detail transcript entry count = %d, want %d", got, want)
	}
	if got := m.detailTranscript.entries[500].Text; got != "tail 500" {
		t.Fatalf("expected first appended detail transcript entry at live tail offset, got %q", got)
	}
	if got := m.detailTranscript.entries[501].Text; got != "tail 501" {
		t.Fatalf("expected second appended detail transcript entry at live tail offset, got %q", got)
	}
	if got := m.view.TranscriptBaseOffset(); got != 0 {
		t.Fatalf("view base offset changed unexpectedly after live append: %d", got)
	}
}

func TestStartupSeedsFromRuntimeClientTranscriptAccessorBeforeBoundedSync(t *testing.T) {
	client := &startupTranscriptRuntimeClient{
		view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{
			SessionID:   "session-1",
			SessionName: "incident triage",
			Transcript:  clientui.TranscriptMetadata{CommittedEntryCount: 15},
		}},
		page:     clientui.TranscriptPage{SessionID: "session-1", HasMoreAbove: true, Entries: []clientui.ChatEntry{{Role: "assistant", Text: "cached tail"}}},
		loadPage: clientui.TranscriptPage{SessionID: "session-1", HasMoreAbove: true, Entries: []clientui.ChatEntry{{Role: "assistant", Text: "authoritative tail"}}},
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())

	next, startupCmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	updated := next.(*uiModel)
	_ = startupCmd
	if client.transcriptCalls != 1 {
		t.Fatalf("expected startup to seed from RuntimeClient.Transcript(), got %d calls", client.transcriptCalls)
	}
	if got := stripANSIAndTrimRight(updated.view.OngoingSnapshot()); !strings.Contains(got, "cached tail") {
		t.Fatalf("expected cached transcript tail visible before bounded sync, got %q", got)
	}
	if updated.sessionName != "incident triage" {
		t.Fatalf("session name = %q, want incident triage", updated.sessionName)
	}
	if got := len(client.loadRequests); got != 0 {
		t.Fatalf("expected no bounded transcript load before startup cmd executes, got %d", got)
	}
	refreshed, ok := startupCmdMessage[runtimeTranscriptRefreshedMsg](updated.startupCmds)
	if !ok {
		t.Fatalf("expected queued startup sync to return runtimeTranscriptRefreshedMsg, got %d command(s)", len(updated.startupCmds))
	}
	if refreshed.syncCause != runtimeTranscriptSyncCauseBootstrap {
		t.Fatalf("startup bounded sync cause = %q, want %q", refreshed.syncCause, runtimeTranscriptSyncCauseBootstrap)
	}
	if refreshed.req != (clientui.TranscriptPageRequest{}) {
		t.Fatalf("request = %+v, want recent-tail (zero cursor)", refreshed.req)
	}
	if got, want := len(client.loadRequests), 1; got != want {
		t.Fatalf("load request count = %d, want %d", got, want)
	}
	if client.loadRequests[0] != (clientui.TranscriptPageRequest{}) {
		t.Fatalf("request = %+v, want recent-tail (zero cursor)", client.loadRequests[0])
	}

	next, followUp := updated.Update(refreshed)
	if followUp != nil {
		_ = collectCmdMessages(t, followUp)
	}
	afterHydrate := next.(*uiModel)
	if got := stripANSIAndTrimRight(afterHydrate.view.OngoingSnapshot()); !strings.Contains(got, "authoritative tail") || strings.Contains(got, "cached tail") {
		t.Fatalf("expected authoritative startup hydrate without cached seed, got %q", got)
	}
}

func TestAssistantDeltaAppendsStreamingText(t *testing.T) {
	m := newProjectedStaticUIModel()

	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "hello"})
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: " world"})

	if got := m.view.OngoingStreamingText(); got != "hello world" {
		t.Fatalf("expected concatenated streaming text, got %q", got)
	}
}

func TestAssistantCommentaryCommitPlusDeltaDoesNotSplitOngoingView(t *testing.T) {
	m := newProjectedStaticUIModel()

	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{
		Kind:                       runtime.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: "Decision: keep tool name patch; expose custom tool with Lark grammar.",
			Phase:   llm.MessagePhaseCommentary,
		},
	})
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{
		Kind:           runtime.EventAssistantDelta,
		StepID:         "step-1",
		AssistantDelta: " Internally normalize custom calls into existing executor input.",
	})

	view := stripANSIPreserve(m.view.OngoingSnapshot())
	if strings.Contains(view, tui.TranscriptDivider) {
		t.Fatalf("expected projected commentary commit plus live assistant delta without divider, got %q", view)
	}
	if !containsInOrder(view, "Decision:", "executor input") {
		t.Fatalf("expected projected commentary commit and live delta in order, got %q", view)
	}
}

func TestAssistantDeltaSkipsNoopFinalToken(t *testing.T) {
	m := newProjectedStaticUIModel()

	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: uiNoopFinalToken})

	if got := m.view.OngoingStreamingText(); got != "" {
		t.Fatalf("expected noop final token to stay out of streaming text, got %q", got)
	}
	if m.sawAssistantDelta {
		t.Fatal("expected sawAssistantDelta to remain false for noop final token")
	}
}

func TestAssistantDeltaResetClearsStreamingText(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetConversationMsg{Ongoing: "partial"})

	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventAssistantDeltaReset})

	if got := m.view.OngoingStreamingText(); got != "" {
		t.Fatalf("expected reset to clear streaming text, got %q", got)
	}
}

func TestAssistantDeltaDoesNotSuppressNewStepThatMatchesPreviousAssistantText(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "Done", Phase: llm.MessagePhaseFinal}}
	m.lastCommittedAssistantStepID = "step-1"

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantDelta, StepID: "step-2", AssistantDelta: "Done"}).cmd

	if got := m.view.OngoingStreamingText(); got != "Done" {
		t.Fatalf("expected matching assistant delta from a new step to stream, got %q", got)
	}
	if !m.sawAssistantDelta {
		t.Fatal("expected matching assistant delta from a new step to preserve assistant delta flag")
	}
}

func TestAssistantDeltaSuppressesLateMatchingDeltaFromCommittedStep(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "Done", Phase: llm.MessagePhaseFinal}}
	m.lastCommittedAssistantStepID = "step-1"

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantDelta, StepID: "step-1", AssistantDelta: "Done"}).cmd

	if got := m.view.OngoingStreamingText(); got != "" {
		t.Fatalf("expected matching assistant delta from the committed step to stay suppressed, got %q", got)
	}
	if m.sawAssistantDelta {
		t.Fatal("expected matching assistant delta from the committed step to keep assistant delta flag cleared")
	}
}

func TestProjectedAssistantMessageClearsStreamingTextOnCommit(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setRuntimeActivityBusyForTest(true)
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "partial"})
	if got := m.view.OngoingStreamingText(); got != "partial" {
		t.Fatalf("expected assistant delta in live stream, got %q", got)
	}

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "assistant",
			Text: "partial",
		}},
	}).
		cmd

	if got := m.view.OngoingStreamingText(); got != "" {
		t.Fatalf("expected committed assistant message to clear live stream, got %q", got)
	}
	if m.sawAssistantDelta {
		t.Fatal("expected committed assistant message to clear assistant delta flag")
	}
	if got := strings.Count(stripANSIPreserve(m.view.OngoingSnapshot()), "partial"); got != 1 {
		t.Fatalf("expected committed final to render once after stream clear, got %d in %q", got, stripANSIPreserve(m.view.OngoingSnapshot()))
	}
}

func TestProjectedAssistantCommentaryDoesNotClearStreamingFinal(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setRuntimeActivityBusyForTest(true)
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "final still streaming"})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "commentary note",
			Phase: string(llm.MessagePhaseCommentary),
		}},
	}).
		cmd

	if got := m.view.OngoingStreamingText(); got != "final still streaming" {
		t.Fatalf("expected commentary commit to preserve live stream, got %q", got)
	}
	if !m.sawAssistantDelta {
		t.Fatal("expected commentary commit to preserve assistant delta flag")
	}
}

func TestProjectedAssistantMessageDoesNotClearStreamingTextWhenCommitIsSkipped(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setRuntimeActivityBusyForTest(true)
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "older"}}
	m.transcriptRevision = 5
	m.transcriptTotalEntries = len(m.transcriptEntries)
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "newer live"})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         5,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "assistant",
			Text: "older",
		}},
	}).
		cmd

	if got := m.view.OngoingStreamingText(); got != "newer live" {
		t.Fatalf("expected skipped assistant commit to preserve live stream, got %q", got)
	}
	if !m.sawAssistantDelta {
		t.Fatal("expected skipped assistant commit to preserve assistant delta flag")
	}
}

func TestProjectedAssistantMessageClearsStreamingTextWhenSkippedCommitMatchesLiveStream(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setRuntimeActivityBusyForTest(true)
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "final"}}
	m.transcriptRevision = 5
	m.transcriptTotalEntries = len(m.transcriptEntries)
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "final"})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         5,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "assistant",
			Text: "final",
		}},
	}).
		cmd

	if got := m.view.OngoingStreamingText(); got != "" {
		t.Fatalf("expected skipped assistant commit matching live stream to clear it, got %q", got)
	}
	if m.sawAssistantDelta {
		t.Fatal("expected skipped matching assistant commit to clear assistant delta flag")
	}
}

func TestApplyRuntimeTranscriptPagePreservesNonEmptyAuthoritativeOngoingEvenWhenTextMatchesCommittedAssistant(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.sawAssistantDelta = true
	m.forwardToView(tui.SetConversationMsg{Ongoing: "final"})

	page := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  3,
		Entries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "final",
			Phase: string(llm.MessagePhaseFinal),
		}},
		Streaming: "final",
	}

	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, page, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	if got := m.view.OngoingStreamingText(); got != "final" {
		t.Fatalf("expected authoritative non-empty ongoing preserved, got %q", got)
	}
	if !m.sawAssistantDelta {
		t.Fatal("expected authoritative non-empty ongoing to preserve assistant delta flag")
	}
	if got := len(m.transcriptEntries); got != 1 || m.transcriptEntries[0].Text != "final" {
		t.Fatalf("expected committed assistant entry preserved after authoritative ongoing apply, got %+v", m.transcriptEntries)
	}
}

func TestApplyRuntimeTranscriptPageAllowsEqualRevisionToClearDuplicateCommittedAssistantOngoing(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.transcriptRevision = 3
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "final", Phase: llm.MessagePhaseFinal}}
	m.transcriptTotalEntries = len(m.transcriptEntries)
	m.sawAssistantDelta = true
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries, Ongoing: "final"})

	page := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  3,
		Entries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "final",
			Phase: string(llm.MessagePhaseFinal),
		}},
		Streaming: "",
	}

	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, page, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	if got := m.view.OngoingStreamingText(); got != "" {
		t.Fatalf("expected equal-revision authoritative page to clear duplicate ongoing, got %q", got)
	}
	if m.sawAssistantDelta {
		t.Fatal("expected equal-revision duplicate ongoing clear to reset assistant delta flag")
	}
}

func TestApplyRuntimeTranscriptPagePreservesAuthoritativeNonEmptyOngoingOverStaleLiveDuplicate(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.transcriptRevision = 3
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "final", Phase: llm.MessagePhaseFinal}}
	m.transcriptTotalEntries = len(m.transcriptEntries)
	m.sawAssistantDelta = true
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries, Ongoing: "final"})

	page := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  4,
		Entries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "final",
			Phase: string(llm.MessagePhaseFinal),
		}},
		Streaming: "final continuation",
	}

	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, page, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	if got := m.view.OngoingStreamingText(); got != "final continuation" {
		t.Fatalf("expected authoritative non-empty ongoing preserved, got %q", got)
	}
	if !m.sawAssistantDelta {
		t.Fatal("expected authoritative non-empty ongoing to preserve assistant delta flag")
	}
}

func TestReasoningDeltaUpdatesDetailTranscriptLive(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 80})
	m.forwardToView(tui.AppendTranscriptMsg{Role: "user", Text: "u"})
	m.forwardToView(tui.ToggleModeMsg{})

	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventReasoningDelta, ReasoningDelta: &llm.ReasoningSummaryDelta{Key: "rs_1:summary:0", Role: "reasoning", Text: "Plan summary"}})

	if detail := stripANSIAndTrimRight(m.view.View()); !strings.Contains(detail, "Plan summary") {
		t.Fatalf("expected live reasoning summary in detail view, got %q", detail)
	}
	if detail := stripANSIAndTrimRight(m.view.View()); strings.Contains(detail, "Preparing patch") {
		t.Fatalf("expected separate status field ignored for detail view, got %q", detail)
	}
}

func TestReasoningDeltaResetClearsLiveReasoningTranscript(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 80})
	m.forwardToView(tui.AppendTranscriptMsg{Role: "user", Text: "u"})
	m.forwardToView(tui.ToggleModeMsg{})
	m.forwardToView(tui.UpsertStreamingReasoningMsg{Key: "rs_1:summary:0", Role: "reasoning", Text: "Plan summary"})

	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventReasoningDeltaReset})

	if detail := stripANSIAndTrimRight(m.view.View()); strings.Contains(detail, "Plan summary") {
		t.Fatalf("expected live reasoning summary cleared after reset, got %q", detail)
	}
}

func TestApplyRuntimeTranscriptPageRejectsEqualRevisionReasoningClear(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 80})
	m.forwardToView(tui.AppendTranscriptMsg{Role: "user", Text: "u"})
	m.forwardToView(tui.ToggleModeMsg{})

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "user", Text: "u"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventReasoningDelta, ReasoningDelta: &llm.ReasoningSummaryDelta{Key: "rs_1:summary:0", Role: "reasoning", Text: "Plan summary"}})
	if detail := stripANSIAndTrimRight(m.view.View()); !strings.Contains(detail, "Plan summary") {
		t.Fatalf("expected live reasoning visible before stale page apply, got %q", detail)
	}

	stale := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "user", Text: "u"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, stale, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	if detail := stripANSIAndTrimRight(m.view.View()); !strings.Contains(detail, "Plan summary") {
		t.Fatalf("expected stale equal-revision page to preserve live reasoning, got %q", detail)
	}
}
