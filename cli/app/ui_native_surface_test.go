package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/invariant"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestNativeEmissionDoesNotRecordDeliveredProjection(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)

	entries := []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleUser,
		Text:      "hello",
	}}
	if err := m.emitNativeCommittedEntries(entries, false); err != nil {
		t.Fatalf("emit native entries: %v", err)
	}
	if !m.nativeImmutableTranscriptWritten {
		t.Fatal("expected immutable transcript written gate after native emission")
	}
	if len(m.nativePendingEmissions) != 0 {
		t.Fatalf("expected no pending emissions, got %d", len(m.nativePendingEmissions))
	}
	if plain := stripANSIForNativeSpecTest(out.String()); !strings.Contains(plain, "hello") {
		t.Fatalf("expected native output to contain committed entry, got %q", plain)
	}
}

func TestNativeProductionCodeDoesNotUseDeliveredLedgerReconciliation(t *testing.T) {
	forbidden := []string{
		"nativeDeliveredStableProjection",
		"nativePendingStableIntent",
		"nativeStableDeliveryIntent",
		"nativeStableRecoveryReconcileIntent",
		"deliverNativeStableProjectionChange",
		"deliverCurrentNativeStableProjectionAfterResize",
		"reprojectNativeDelivered",
		"nativeProjectionBlockSourceKey",
	}
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, term := range forbidden {
			if strings.Contains(string(content), term) {
				t.Fatalf("production file %s contains forbidden native scrollback ledger/reconcile term %q", path, term)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production app files: %v", err)
	}
}

func TestNativeEmissionQueuesWhileDetailSurfaceActiveAndDrainsOnReturn(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	m.activeSurface = uiSurfaceTranscriptDetail
	m.altScreenActive = true

	entries := []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleAssistant,
		Text:      "queued answer",
	}}
	if m.nativeStableOutputReady() {
		t.Fatal("native stable output should not be ready while detail surface owns alt-screen")
	}
	if err := m.queueNativeEmission(nativePendingEmission{kind: nativePendingEmissionEntries, entries: entries}); err != nil {
		t.Fatalf("queue native emission: %v", err)
	}
	if len(m.nativePendingEmissions) != 1 {
		t.Fatalf("expected one queued emission, got %d", len(m.nativePendingEmissions))
	}
	if plain := stripANSIForNativeSpecTest(out.String()); strings.Contains(plain, "queued answer") {
		t.Fatalf("queued native emission wrote while off-surface, got %q", plain)
	}

	m.activeSurface = uiSurfaceOngoingTranscript
	m.altScreenActive = false
	cmd := m.drainNativePendingEmissions()
	if cmd != nil {
		if msg := cmd(); msg != nil {
			t.Fatalf("unexpected drain command message: %T", msg)
		}
	}
	if len(m.nativePendingEmissions) != 0 {
		t.Fatalf("expected queue drained, got %d", len(m.nativePendingEmissions))
	}
	if plain := stripANSIForNativeSpecTest(out.String()); !strings.Contains(plain, "queued answer") {
		t.Fatalf("expected drained native output, got %q", plain)
	}
}

func TestNativeEmissionOverflowRequestsScratch(t *testing.T) {
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	entry := tui.TranscriptEntry{Committed: true, Role: tui.TranscriptRoleSystem, Text: "status"}
	for idx := 0; idx <= nativeMaxPendingEmissions; idx++ {
		if err := m.queueNativeEmission(nativePendingEmission{
			kind:    nativePendingEmissionEntries,
			entries: []tui.TranscriptEntry{entry},
		}); err != nil {
			t.Fatalf("queue emission %d: %v", idx, err)
		}
	}
	if !m.nativeScratchHydrationPending {
		t.Fatal("expected native scratch hydration pending after queue overflow")
	}
	if len(m.nativePendingEmissions) != 0 {
		t.Fatalf("expected queue dropped after overflow, got %d", len(m.nativePendingEmissions))
	}
}

func TestNativeWidthResizeSchedulesScratchOnlyAfterImmutableOutput(t *testing.T) {
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	m.windowSizeKnown = true
	m.termWidth = 100
	m.termHeight = 30

	if cmd := m.scheduleNativeResizeRehydrate(true); cmd != nil {
		t.Fatal("unexpected resize scratch command before immutable transcript output")
	}

	m.nativeImmutableTranscriptWritten = true
	cmd := m.scheduleNativeResizeRehydrate(true)
	if cmd == nil {
		t.Fatal("expected width resize scratch command after immutable transcript output")
	}
	if !m.nativeScratchHydrationPending {
		t.Fatal("expected scratch hydration barrier during resize debounce")
	}
}

func TestNativeWidthResizeDuringActiveStreamSchedulesScratch(t *testing.T) {
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	m.nativeImmutableTranscriptWritten = true
	if _, err := m.streamNativeAssistantDelta("partial", clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}

	cmd := m.scheduleNativeResizeRehydrate(true)
	if cmd == nil {
		t.Fatal("expected width resize scratch command while native assistant stream is active")
	}
	if !m.nativeScratchHydrationPending {
		t.Fatal("expected active-stream width resize to set scratch hydration barrier")
	}
	if !m.nativeSurface.AssistantStreaming() {
		t.Fatal("resize debounce should not drop the native assistant stream before scratch append")
	}
	if m.nativeLiveAreaError != nil {
		t.Fatalf("width resize should not disable native output: %v", m.nativeLiveAreaError)
	}
}

func TestNativeHeightOnlyResizeDoesNotScheduleScratch(t *testing.T) {
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	m.nativeImmutableTranscriptWritten = true
	if cmd := m.scheduleNativeResizeRehydrate(false); cmd != nil {
		t.Fatal("height-only resize must not schedule native scratch hydration")
	}
	if m.nativeScratchHydrationPending {
		t.Fatal("height-only resize must not set native scratch barrier")
	}
}

func TestNativeScratchPageAppendsDuplicateLookingActiveSegment(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)

	initial := []tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleUser, Text: "repeat"}}
	if err := m.emitNativeCommittedEntries(initial, false); err != nil {
		t.Fatalf("emit initial entry: %v", err)
	}
	m.transcriptEntries = []tui.TranscriptEntry{
		{Committed: true, Role: tui.TranscriptRoleUser, Text: "repeat"},
		{Committed: true, Role: tui.TranscriptRoleAssistant, Text: "fresh"},
	}
	m.nativeScratchHydrationPending = true
	if err := m.appendNativeScratchTranscript(m.transcriptEntries); err != nil {
		t.Fatalf("append scratch transcript: %v", err)
	}
	plain := stripANSIForNativeSpecTest(out.String())
	if strings.Count(plain, "repeat") < 2 {
		t.Fatalf("scratch append should not suppress duplicate-looking rows, got %q", plain)
	}
	if !strings.Contains(plain, "fresh") {
		t.Fatalf("scratch append missing fresh row, got %q", plain)
	}
}

func TestNativeInvariantPanicKeepsOngoingRendererFromReplayingTranscript(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	m.rendererOutputGate = newUIRendererOutputGateState()
	m.syncRendererOutputGate()

	entries := []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleUser,
		Text:      "already emitted",
	}}
	if err := m.emitNativeCommittedEntries(entries, false); err != nil {
		t.Fatalf("emit native entries: %v", err)
	}
	m.transcriptEntries = entries

	assertNativeTranscriptInvariantPanic(t, func() {
		m.nativeInvariantViolationCmd("hydrate committed transcript", errors.New("committed transcript divergence"))
	})
	if !m.rendererOutputGate.shouldDrop([]byte("ongoing renderer frame")) {
		t.Fatal("fatal native invariant must keep ongoing normal-buffer renderer output suppressed")
	}
	if rendered := stripANSIForNativeSpecTest(m.View()); strings.Contains(rendered, "already emitted") {
		t.Fatalf("fatal native invariant replayed emitted transcript through renderer: %q", rendered)
	}
}

func TestNativeOwnerAbsentAfterImmutableOutputSuppressesOngoingRendererReplay(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	m.rendererOutputGate = newUIRendererOutputGateState()
	m.syncRendererOutputGate()

	entries := []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleUser,
		Text:      "already emitted",
	}}
	if err := m.emitNativeCommittedEntries(entries, false); err != nil {
		t.Fatalf("emit native entries: %v", err)
	}
	m.transcriptEntries = entries
	m.dropNativeSurface()
	if m.nativeSurfaceEnabled() {
		t.Fatal("expected native owner absent after drop")
	}
	if !m.rendererOutputGate.shouldDrop([]byte("ongoing renderer frame")) {
		t.Fatal("renderer gate must remain suppressed after immutable native output loses its owner")
	}
	if rendered := stripANSIForNativeSpecTest(m.View()); strings.Contains(rendered, "already emitted") || strings.TrimSpace(rendered) != "" {
		t.Fatalf("absent native owner replayed emitted transcript through renderer: %q", rendered)
	}
}

func TestNativeOwnerAbsentAfterImmutableOutputStillPanicsOnCommittedDivergence(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModelWithClient(&out, &runtimeControlFakeClient{})
	entries := []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleUser,
		Text:      "already emitted",
	}}
	if err := m.emitNativeCommittedEntries(entries, false); err != nil {
		t.Fatalf("emit native entries: %v", err)
	}
	m.transcriptEntries = entries
	m.dropNativeSurface()
	if m.nativeSurfaceConfigured() {
		t.Fatal("expected native owner absent after drop")
	}

	diagnostic := assertNativeTranscriptInvariantPanic(t, func() {
		m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
			Kind:                       clientui.EventAssistantMessage,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         11,
			CommittedEntryStartSet:     true,
			CommittedEntryStart:        2,
			CommittedEntryCount:        3,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  "after gap",
				Phase: string(clientui.MessagePhaseFinal),
			}},
		})
	})
	if got := len(m.transcriptEntries); got != 1 {
		t.Fatalf("expected transcript entries unchanged, got %d", got)
	}
	if diagnostic.Fields[invariant.FieldEventKind] == "" || diagnostic.Fields[invariant.FieldTranscriptState] == "" {
		t.Fatalf("owner-absent panic diagnostic missing event/state fields: %+v", diagnostic)
	}
}

func TestNativeScratchPageAppendsAfterActiveStream(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	if _, err := m.streamNativeAssistantDelta("partial", clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}

	pageEntries := []tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleAssistant, Text: "authoritative full"}}
	m.nativeScratchHydrationPending = true
	if err := m.appendNativeScratchTranscript(pageEntries); err != nil {
		t.Fatalf("append scratch transcript: %v", err)
	}
	plain := stripANSIForNativeSpecTest(out.String())
	if !strings.Contains(plain, "authoritative full") {
		t.Fatalf("scratch append during active stream must append page, got %q", plain)
	}
}

func TestNativeQueuedScratchUsesHydratedSnapshot(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	m.activeSurface = uiSurfaceTranscriptDetail
	m.altScreenActive = true
	snapshot := []tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleUser, Text: "scratch snapshot"}}
	m.nativeScratchHydrationPending = true
	if err := m.queueNativeEmission(nativePendingEmission{kind: nativePendingEmissionScratch, entries: snapshot}); err != nil {
		t.Fatalf("queue scratch emission: %v", err)
	}
	m.transcriptEntries = []tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleUser, Text: "later mutation"}}

	m.activeSurface = uiSurfaceOngoingTranscript
	m.altScreenActive = false
	if msg := runTeaCmdForNativeSpecTest(m.drainNativePendingEmissions()); msg != nil {
		t.Fatalf("unexpected drain message: %T", msg)
	}
	plain := stripANSIForNativeSpecTest(out.String())
	if !strings.Contains(plain, "scratch snapshot") || strings.Contains(plain, "later mutation") {
		t.Fatalf("queued scratch must use hydrated snapshot, got %q", plain)
	}
}

func TestNativeScratchWithStreamingDisablesNativeStreamUntilFinalCommit(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	if _, err := m.streamNativeAssistantDelta("partial", clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}
	m.nativeScratchHydrationPending = true

	m.runtimeAdapter().applyRuntimeTranscriptPageWithSyncCause(
		clientui.TranscriptPageRequest{},
		clientui.TranscriptPage{
			Entries: []clientui.ChatEntry{{
				Role: "user",
				Text: "prompt",
			}},
			Streaming: "partial",
		},
		runtimeTranscriptSyncCauseNativeScratch,
		clientui.TranscriptRecoveryCauseNone,
	)
	if !m.nativeAssistantStreamIncomplete {
		t.Fatal("expected native assistant stream disabled after scratch with hydrated streaming text")
	}
	if m.nativeSurface.AssistantStreaming() {
		t.Fatal("scratch must abort native assistant stream")
	}
	if handled, err := m.streamNativeAssistantDelta(" suffix", clientui.MessagePhaseFinal); err != nil || handled {
		t.Fatalf("expected post-scratch delta not to restart native streaming, handled=%t err=%v", handled, err)
	}

	_, mutated, needsHydration, _ := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		CommittedEntryStartSet:     true,
		CommittedEntryStart:        len(m.transcriptEntries),
		CommittedEntryCount:        len(m.transcriptEntries) + 1,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "partial suffix",
			Phase: string(clientui.MessagePhaseFinal),
		}},
	})
	if !mutated || needsHydration {
		t.Fatalf("expected final commit append without hydration, mutated=%t needsHydration=%t", mutated, needsHydration)
	}
	plain := stripANSIForNativeSpecTest(out.String())
	if !strings.Contains(plain, "partial suffix") {
		t.Fatalf("expected full final assistant row emitted after scratch-disabled stream, got %q", plain)
	}
}

func TestNativeLiveFrameBoundsOngoingStreamTailWhileQuestionIsActive(t *testing.T) {
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	m.termWidth = 80
	m.termHeight = 30
	m.windowSizeKnown = true
	m.layout().syncViewport()
	streamLines := make([]string, 0, 20)
	for idx := 0; idx < 20; idx++ {
		streamLines = append(streamLines, "stream line "+strconv.Itoa(idx))
	}
	m.forwardToView(tui.SetConversationMsg{Ongoing: strings.Join(streamLines, "\n")})
	testSetActiveAsk(m, &askEvent{req: clientui.PendingPromptEvent{
		Question: "Choose one",
		Suggestions: []string{
			"one",
			"two",
			"three",
		},
	}, reply: make(chan askReply, 1)})

	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing view returned renderer payload %q", rendered)
	}
	if m.nativeSurface == nil || !m.nativeSurface.lastFrameSet {
		t.Fatal("expected native live frame to render")
	}
	streamCount := 0
	plainFrame := make([]string, 0, len(m.nativeSurface.lastFrame.Lines))
	for _, line := range m.nativeSurface.lastFrame.Lines {
		plain := stripANSIForNativeSpecTest(line)
		plainFrame = append(plainFrame, plain)
		if strings.Contains(plain, "stream line ") {
			streamCount++
		}
	}
	if streamCount > nativeLiveAssistantTailMaxLines {
		t.Fatalf("native live frame rendered %d stream lines, want <= %d\n%s", streamCount, nativeLiveAssistantTailMaxLines, strings.Join(plainFrame, "\n"))
	}
	joined := strings.Join(plainFrame, "\n")
	if strings.Contains(joined, "stream line 0") {
		t.Fatalf("native live frame kept oldest stream line instead of tail:\n%s", joined)
	}
	if !strings.Contains(joined, "stream line 19") || !strings.Contains(joined, "Choose one") {
		t.Fatalf("native live frame missing stream tail or active question:\n%s", joined)
	}
}

func TestNativeLiveFrameRenderUsesBottomAnchorFromAppLayout(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	m.termWidth = 20
	m.termHeight = 6
	m.windowSizeKnown = true

	rendered := m.layout().renderNativeLiveAreaFrame(uiRenderFrame{
		width:      20,
		height:     6,
		inputPane:  []string{"input"},
		statusLine: "status",
		tailOnly:   true,
	})

	if strings.TrimSpace(rendered) != "" {
		t.Fatalf("native live frame should write directly to terminal, got fallback render %q", rendered)
	}
	raw := out.String()
	anchor := xansi.CursorPosition(1, 6)
	anchorIndex := strings.Index(raw, anchor)
	inputIndex := strings.Index(raw, "input")
	statusIndex := strings.Index(raw, "status")
	if anchorIndex < 0 || inputIndex < 0 || statusIndex < 0 || !(anchorIndex < inputIndex && inputIndex < statusIndex) {
		t.Fatalf("native app render did not anchor to terminal bottom before frame content: %q", raw)
	}
}

func TestNativeLiveFrameReservesTranscriptRowWhenFrameWouldFillTerminal(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	m.termWidth = 20
	m.termHeight = 3
	m.windowSizeKnown = true

	rendered := m.layout().renderNativeLiveAreaFrame(uiRenderFrame{
		width:      20,
		height:     3,
		chatPanel:  []string{"chat"},
		inputPane:  []string{"input"},
		statusLine: "status",
		tailOnly:   true,
	})

	if strings.TrimSpace(rendered) != "" {
		t.Fatalf("native live frame should write directly to terminal, got fallback render %q", rendered)
	}
	if m.nativeSurface == nil || !m.nativeSurface.lastFrameSet {
		t.Fatal("expected native live frame to render")
	}
	plainFrame := stripANSIForNativeSpecTest(strings.Join(m.nativeSurface.lastFrame.Lines, "\n"))
	if got, want := len(m.nativeSurface.lastFrame.Lines), 2; got != want {
		t.Fatalf("native live frame rows = %d, want %d: %q", got, want, plainFrame)
	}
	if strings.Contains(plainFrame, "chat") || !strings.Contains(plainFrame, "input") || !strings.Contains(plainFrame, "status") {
		t.Fatalf("native live frame did not keep bottom-priority rows while reserving transcript space: %q", plainFrame)
	}
	if err := m.nativeSurface.surface.Steer("stable"); err != nil {
		t.Fatalf("stable steer with reserved transcript row returned error: %v", err)
	}
	if plain := stripANSIForNativeSpecTest(out.String()); !strings.Contains(plain, "stable") {
		t.Fatalf("stable steer was not appended after reserved-row live frame: %q", plain)
	}
}

func TestNativeOneRowGeometryUsesRendererFallback(t *testing.T) {
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	m.termWidth = 20
	m.termHeight = 1
	m.windowSizeKnown = true
	m.forwardToView(tui.SetViewportSizeMsg{Width: 20, Lines: 1})

	if m.nativeSurfaceEnabled() {
		t.Fatal("native surface should not own one-row geometry")
	}
	m.syncRendererOutputGate()
	if m.rendererOutputGate != nil && m.rendererOutputGate.shouldDrop([]byte("fallback")) {
		t.Fatal("renderer output gate suppressed fallback rendering for one-row geometry")
	}
	if rendered := m.View(); strings.TrimSpace(rendered) == "" {
		t.Fatal("expected one-row geometry to return normal renderer fallback output")
	}
}

func TestNativeOneRowResizeDropsInitializedSurfaceBeforeAssistantStream(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	m.termWidth = 20
	m.termHeight = 6
	m.windowSizeKnown = true

	rendered := m.layout().renderNativeLiveAreaFrame(uiRenderFrame{
		width:      20,
		height:     6,
		inputPane:  []string{"input"},
		statusLine: "status",
		tailOnly:   true,
	})
	if strings.TrimSpace(rendered) != "" {
		t.Fatalf("native live frame should write directly to terminal, got fallback render %q", rendered)
	}
	if m.nativeSurface == nil || !m.nativeSurface.initialized() {
		t.Fatal("expected native surface initialized before resize")
	}

	out.Reset()
	m.termHeight = 1
	m.forwardToView(tui.SetViewportSizeMsg{Width: 20, Lines: 1})
	handled, err := m.streamNativeAssistantDelta("partial", clientui.MessagePhaseFinal)
	if err != nil {
		t.Fatalf("stream native assistant delta returned error: %v", err)
	}
	if handled {
		t.Fatal("one-row geometry should not handle assistant delta with native direct writes")
	}
	if m.nativeSurface == nil {
		t.Fatal("native surface wrapper should remain configured for later valid geometry")
	}
	if m.nativeSurface.initialized() {
		t.Fatal("one-row geometry should drop the initialized native surface")
	}
	if out.Len() != 0 {
		t.Fatalf("one-row geometry wrote native bytes after fallback took ownership: %q", out.String())
	}
}

func TestNativeAssistantFinalizerEmitsSuffixOnly(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	if _, err := m.streamNativeAssistantDelta("hello", clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}

	entries := []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleAssistant,
		Text:      "hello world",
		Phase:     clientui.MessagePhaseFinal,
	}}
	remaining, _, err := m.nativeCommittedEntriesAfterActiveAssistantFinalizer(entries, "hello")
	if err != nil {
		t.Fatalf("finalize assistant stream: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected finalizer entry skipped from stable append, got %d", len(remaining))
	}
	plain := stripANSIForNativeSpecTest(out.String())
	if !strings.Contains(plain, "hello world") {
		t.Fatalf("expected streamed suffix to complete assistant output, got %q", plain)
	}
}

func TestNativeAssistantFinalizerDrainsQueuedCommittedRows(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	if _, err := m.streamNativeAssistantDelta("hello", clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}
	if err := m.emitNativeCommittedEntries([]tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleUser,
		Text:      "queued user",
	}}, true); err != nil {
		t.Fatalf("queue committed row during stream: %v", err)
	}
	if len(m.nativePendingEmissions) != 1 {
		t.Fatalf("expected queued committed row during stream, got %d", len(m.nativePendingEmissions))
	}

	remaining, _, err := m.nativeCommittedEntriesAfterActiveAssistantFinalizer([]tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleAssistant,
		Text:      "hello",
		Phase:     clientui.MessagePhaseFinal,
	}}, "hello")
	if err != nil {
		t.Fatalf("finalize assistant stream: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected exact-match finalizer skipped from stable append, got %d", len(remaining))
	}
	if len(m.nativePendingEmissions) != 0 {
		t.Fatalf("expected queued committed row drained after finalizer, got %d", len(m.nativePendingEmissions))
	}
	plain := stripANSIForNativeSpecTest(out.String())
	assistantIndex := strings.Index(plain, "hello")
	queuedIndex := strings.Index(plain, "queued user")
	if assistantIndex < 0 || queuedIndex < 0 || assistantIndex > queuedIndex {
		t.Fatalf("expected stream finalizer before queued committed row, got %q", plain)
	}
}

func TestNativeAssistantCommentaryFinalizerEmitsSuffixOnly(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	if _, err := m.streamNativeAssistantDelta("note", clientui.MessagePhaseCommentary); err != nil {
		t.Fatalf("stream assistant commentary: %v", err)
	}

	entries := []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleAssistant,
		Text:      "note done",
		Phase:     clientui.MessagePhaseCommentary,
	}}
	remaining, _, err := m.nativeCommittedEntriesAfterActiveAssistantFinalizer(entries, "note")
	if err != nil {
		t.Fatalf("finalize assistant commentary stream: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected commentary finalizer skipped from stable append, got %d", len(remaining))
	}
	plain := stripANSIForNativeSpecTest(out.String())
	if !strings.Contains(plain, "note done") {
		t.Fatalf("expected streamed commentary suffix, got %q", plain)
	}
}

func TestNativeAssistantFinalizerMismatchFailsFast(t *testing.T) {
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	if _, err := m.streamNativeAssistantDelta("hello", clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}

	entries := []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleAssistant,
		Text:      "goodbye",
		Phase:     clientui.MessagePhaseFinal,
	}}
	if _, _, err := m.nativeCommittedEntriesAfterActiveAssistantFinalizer(entries, "hello"); err == nil {
		t.Fatal("expected non-prefix finalizer to fail")
	}
}

func TestNativeAssistantFinalizerMismatchPanicsBeforeTranscriptMutation(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	m.appendActiveAssistantStreamDelta("step-1", "hello")
	if _, err := m.streamNativeAssistantDelta("hello", clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}

	assertNativeTranscriptInvariantPanic(t, func() {
		m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
			Kind:                       clientui.EventAssistantMessage,
			StepID:                     "step-1",
			CommittedTranscriptChanged: true,
			CommittedEntryStartSet:     true,
			CommittedEntryStart:        0,
			CommittedEntryCount:        1,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  "goodbye",
				Phase: string(clientui.MessagePhaseFinal),
			}},
		})
	})
	if len(m.transcriptEntries) != 0 {
		t.Fatalf("expected transcript entries unchanged, got %d", len(m.transcriptEntries))
	}
}

func TestNativeAssistantFinalizerMismatchDiagnosticModePanicsBeforeTranscriptMutation(t *testing.T) {
	t.Setenv("KENT_DEBUG", "false")
	t.Setenv("KENT_INVARIANT_MODE", "")
	logger := &testUILogger{}
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	m.logger = logger
	m.appendActiveAssistantStreamDelta("step-1", "hello")
	if _, err := m.streamNativeAssistantDelta("hello", clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}

	assertNativeTranscriptInvariantPanic(t, func() {
		m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
			Kind:                       clientui.EventAssistantMessage,
			StepID:                     "step-1",
			CommittedTranscriptChanged: true,
			CommittedEntryStartSet:     true,
			CommittedEntryStart:        0,
			CommittedEntryCount:        1,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  "goodbye",
				Phase: string(clientui.MessagePhaseFinal),
			}},
		})
	})
	if len(m.transcriptEntries) != 0 {
		t.Fatalf("expected transcript entries unchanged after prefix mismatch, got %+v", m.transcriptEntries)
	}
	if len(logger.lines) == 0 {
		t.Fatalf("expected native invariant diagnostics in TUI log, got %#v", logger.lines)
	}
}

func TestNativeNewAssistantStepDuringActiveStreamPanicsBeforeStreamMutation(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	m.appendActiveAssistantStreamDelta("step-1", "hello")
	if _, err := m.streamNativeAssistantDelta("hello", clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}

	assertNativeTranscriptInvariantPanic(t, func() {
		m.handleRuntimeEventBatch([]clientui.Event{{
			Kind:                clientui.EventAssistantDelta,
			StepID:              "step-2",
			AssistantDelta:      "new step",
			AssistantDeltaPhase: clientui.MessagePhaseFinal,
		}})
	})
	if got := m.activeAssistantStreamText(); got != "hello" {
		t.Fatalf("expected active stream source unchanged after fatal new-step violation, got %q", got)
	}
	if m.sawAssistantDelta {
		t.Fatal("new step delta mutated assistant stream state after fatal native violation")
	}
}

func TestNativeNewAssistantStepWithUnknownActiveStepPanicsBeforeStreamMutation(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	m.appendActiveAssistantStreamDelta("", "hello")
	if _, err := m.streamNativeAssistantDelta("hello", clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}

	assertNativeTranscriptInvariantPanic(t, func() {
		m.handleRuntimeEventBatch([]clientui.Event{{
			Kind:                clientui.EventAssistantDelta,
			StepID:              "step-2",
			AssistantDelta:      "new step",
			AssistantDeltaPhase: clientui.MessagePhaseFinal,
		}})
	})
	if got := m.activeAssistantStreamText(); got != "hello" {
		t.Fatalf("expected unknown-step stream source unchanged after fatal violation, got %q", got)
	}
	if m.sawAssistantDelta {
		t.Fatal("new step delta mutated unknown-step assistant stream state after fatal native violation")
	}
}

func TestNativeNonGapCommittedDivergencePanicsBeforeHydration(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	m := newNativeSurfaceSpecTestModelWithClient(&bytes.Buffer{}, &runtimeControlFakeClient{})
	m.nativeImmutableTranscriptWritten = true
	m.transcriptEntries = []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleAssistant,
		Text:      "seed",
	}}
	m.transcriptRevision = 10
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   0,
		TotalEntries: 1,
		Entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
	})

	assertNativeTranscriptInvariantPanic(t, func() {
		m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
			Kind:                       clientui.EventAssistantMessage,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         11,
			CommittedEntryStartSet:     true,
			CommittedEntryStart:        2,
			CommittedEntryCount:        3,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  "after gap",
				Phase: string(clientui.MessagePhaseFinal),
			}},
		})
	})
	if got := len(m.transcriptEntries); got != 1 {
		t.Fatalf("expected transcript entries unchanged, got %d", got)
	}
}

func TestNativeNonGapCommittedDivergenceDiagnosticModePanicsWithoutHydration(t *testing.T) {
	t.Setenv("KENT_DEBUG", "false")
	t.Setenv("KENT_INVARIANT_MODE", "")
	logger := &testUILogger{}
	m := newNativeSurfaceSpecTestModelWithClient(&bytes.Buffer{}, &runtimeControlFakeClient{})
	m.logger = logger
	m.nativeImmutableTranscriptWritten = true
	m.transcriptEntries = []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleAssistant,
		Text:      "seed",
	}}
	m.transcriptRevision = 10
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   0,
		TotalEntries: 1,
		Entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
	})

	assertNativeTranscriptInvariantPanic(t, func() {
		m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
			Kind:                       clientui.EventAssistantMessage,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         11,
			CommittedEntryStartSet:     true,
			CommittedEntryStart:        2,
			CommittedEntryCount:        3,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  "after gap",
				Phase: string(clientui.MessagePhaseFinal),
			}},
		})
	})
	if got := len(m.transcriptEntries); got != 1 {
		t.Fatalf("expected transcript entries unchanged, got %d", got)
	}
	if len(logger.lines) == 0 {
		t.Fatalf("expected native divergence diagnostics in TUI log, got %#v", logger.lines)
	}
}

func TestNativeNonAppendReplacePanicsBeforeTranscriptMutation(t *testing.T) {
	m := newNativeSurfaceSpecTestModelWithClient(&bytes.Buffer{}, &runtimeControlFakeClient{})
	m.nativeImmutableTranscriptWritten = true
	m.transcriptEntries = []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleAssistant,
		Text:      "seed",
	}}
	m.transcriptRevision = 10
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   0,
		TotalEntries: 1,
		Entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
	})

	diagnostic := assertNativeTranscriptInvariantPanic(t, func() {
		m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
			Kind:                       clientui.EventAssistantMessage,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         11,
			CommittedEntryStartSet:     true,
			CommittedEntryStart:        0,
			CommittedEntryCount:        1,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  "replacement",
				Phase: string(clientui.MessagePhaseFinal),
			}},
		})
	})
	if got := len(m.transcriptEntries); got != 1 {
		t.Fatalf("expected transcript entries unchanged, got %d", got)
	}
	if got := m.transcriptEntries[0].Text; got != "seed" {
		t.Fatalf("native replace invariant mutated transcript before panic, got %q", got)
	}
	if diagnostic.Fields[invariant.FieldEventKind] == "" || diagnostic.Fields[invariant.FieldTranscriptState] == "" {
		t.Fatalf("native replace panic diagnostic missing event/state fields: %+v", diagnostic)
	}
}

func TestNativeNonGapCommittedDivergenceStopsRuntimeBatch(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	m := newNativeSurfaceSpecTestModelWithClient(&bytes.Buffer{}, &runtimeControlFakeClient{})
	m.nativeImmutableTranscriptWritten = true
	m.transcriptEntries = []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleAssistant,
		Text:      "seed",
	}}
	m.transcriptRevision = 10
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   0,
		TotalEntries: 1,
		Entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
	})

	assertNativeTranscriptInvariantPanic(t, func() {
		m.runtimeAdapter().applyProjectedRuntimeEventsBatch([]clientui.Event{
			{
				Kind:                       clientui.EventAssistantMessage,
				CommittedTranscriptChanged: true,
				TranscriptRevision:         11,
				CommittedEntryStartSet:     true,
				CommittedEntryStart:        2,
				CommittedEntryCount:        3,
				TranscriptEntries: []clientui.ChatEntry{{
					Role:  "assistant",
					Text:  "after gap",
					Phase: string(clientui.MessagePhaseFinal),
				}},
			},
			{
				Kind:                clientui.EventAssistantDelta,
				StepID:              "later-step",
				AssistantDelta:      "must not apply",
				AssistantDeltaPhase: clientui.MessagePhaseFinal,
			},
		})
	})
	if got := len(m.transcriptEntries); got != 1 {
		t.Fatalf("expected transcript entries unchanged, got %d", got)
	}
	if m.sawAssistantDelta || m.activeAssistantStreamText() != "" {
		t.Fatalf("later batch event mutated assistant stream: saw=%t text=%q", m.sawAssistantDelta, m.activeAssistantStreamText())
	}
	if len(m.pendingRuntimeEvents) != 0 {
		t.Fatalf("fatal native invariant must not enqueue hydration backlog, got %#v", m.pendingRuntimeEvents)
	}
}

func TestNativeAssistantFinalizerMismatchWithoutPhysicalStreamPanicsBeforeMutation(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	m.appendActiveAssistantStreamDelta("step-1", "hello")
	m.nativeAssistantStreamIncomplete = true

	assertNativeTranscriptInvariantPanic(t, func() {
		m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
			Kind:                       clientui.EventAssistantMessage,
			StepID:                     "step-1",
			CommittedTranscriptChanged: true,
			TranscriptRevision:         1,
			CommittedEntryStartSet:     true,
			CommittedEntryStart:        0,
			CommittedEntryCount:        1,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  "goodbye",
				Phase: string(clientui.MessagePhaseFinal),
			}},
		})
	})
	if len(m.transcriptEntries) != 0 {
		t.Fatalf("expected transcript entries unchanged, got %d", len(m.transcriptEntries))
	}
	if got := m.activeAssistantStreamText(); got != "hello" {
		t.Fatalf("expected active assistant source preserved after fatal mismatch, got %q", got)
	}
}

func TestNativeAssistantFinalizerFailsWhenCommittedRowsPrecedeFinalizer(t *testing.T) {
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	if _, err := m.streamNativeAssistantDelta("hello", clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}

	entries := []tui.TranscriptEntry{
		{Committed: true, Role: tui.TranscriptRoleSystem, Text: "cannot precede finalizer"},
		{Committed: true, Role: tui.TranscriptRoleAssistant, Text: "hello", Phase: clientui.MessagePhaseFinal},
	}
	if _, _, err := m.nativeCommittedEntriesAfterActiveAssistantFinalizer(entries, "hello"); err == nil {
		t.Fatal("expected pre-finalizer committed row to fail")
	}
}

func TestNativeScratchHydrationFailureQuits(t *testing.T) {
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	m.nativeScratchHydrationPending = true
	m.nativePendingEmissions = []nativePendingEmission{{kind: nativePendingEmissionEntries, entries: []tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleUser, Text: "stale"}}}}

	msgs := collectCmdMessages(t, m.nativeScratchHydrationFailed(errors.New("scratch failed")))
	quit := false
	for _, msg := range msgs {
		if _, ok := msg.(tea.QuitMsg); ok {
			quit = true
		}
	}
	if !quit {
		t.Fatalf("expected scratch failure to quit, got %#v", msgs)
	}
	if len(m.nativePendingEmissions) != 0 {
		t.Fatalf("expected pending native emissions dropped, got %d", len(m.nativePendingEmissions))
	}
}

func TestRuntimeBatchStopsAtFirstHydrationBarrier(t *testing.T) {
	m := newNativeSurfaceSpecTestModelWithClient(&bytes.Buffer{}, &runtimeControlFakeClient{})
	hydrate := clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		CommittedTranscriptChanged: true,
		CommittedEntryStartSet:     true,
		CommittedEntryStart:        10,
		CommittedEntryCount:        11,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: string(tui.TranscriptRoleUser),
			Text: "gap",
		}},
	}
	after := clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		CommittedTranscriptChanged: true,
		CommittedEntryStartSet:     true,
		CommittedEntryStart:        11,
		CommittedEntryCount:        12,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: string(tui.TranscriptRoleSystem),
			Text: "after",
		}},
	}

	result := m.runtimeAdapter().applyProjectedRuntimeEventsBatch([]clientui.Event{hydrate, after})
	if !result.awaitsHydration {
		t.Fatal("expected batch to await hydration")
	}
	if len(m.pendingRuntimeEvents) != 1 || m.pendingRuntimeEvents[0].TranscriptEntries[0].Text != "after" {
		t.Fatalf("expected remaining event carried behind hydration, got %#v", m.pendingRuntimeEvents)
	}
}

func newNativeSurfaceSpecTestModel(out *bytes.Buffer) *uiModel {
	return newNativeSurfaceSpecTestModelWithClient(out, nil)
}

func newNativeSurfaceSpecTestModelWithClient(out *bytes.Buffer, client clientui.RuntimeClient) *uiModel {
	m := newProjectedClosedUIModel(client)
	m.termWidth = 100
	m.termHeight = 30
	m.windowSizeKnown = true
	m.activeSurface = uiSurfaceOngoingTranscript
	m.forwardToView(tui.SetViewportSizeMsg{Width: 100, Lines: 30})
	m.nativeSurface = newUINativeSurface(out, m.nativeNormalBufferAvailable, nil)
	if !m.ensureNativeSurface(100, 30) {
		panic("failed to initialize native surface")
	}
	return m
}

func assertNativeTranscriptInvariantPanic(t *testing.T, run func()) invariant.Diagnostic {
	t.Helper()
	var diagnostic invariant.Diagnostic
	panicked := false
	func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			panicked = true
			var ok bool
			diagnostic, ok = recovered.(invariant.Diagnostic)
			if !ok {
				t.Fatalf("panic payload = %T, want invariant.Diagnostic", recovered)
			}
			if diagnostic.Scope != invariant.ScopeNativeTranscript {
				t.Fatalf("panic diagnostic scope = %q, want %q", diagnostic.Scope, invariant.ScopeNativeTranscript)
			}
			if diagnostic.Fields[invariant.FieldOperation] == "" {
				t.Fatalf("panic diagnostic missing operation: %+v", diagnostic)
			}
			if diagnostic.Fields[invariant.FieldInvariantError] == "" {
				t.Fatalf("panic diagnostic missing invariant error: %+v", diagnostic)
			}
			if diagnostic.Stack == "" {
				t.Fatal("panic diagnostic missing stack")
			}
		}()
		run()
	}()
	if !panicked {
		t.Fatal("expected native transcript invariant panic")
	}
	return diagnostic
}

func stripANSIForNativeSpecTest(raw string) string {
	var out strings.Builder
	inEscape := false
	for _, r := range raw {
		if inEscape {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		if r == '\x1b' {
			inEscape = true
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func runTeaCmdForNativeSpecTest(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}
