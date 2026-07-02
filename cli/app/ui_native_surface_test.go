package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/invariant"
	"core/shared/toolspec"
	"core/shared/transcript"

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

func TestNativeScratchPageEmitsCommittedEntriesAfterUnresolvedToolCall(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)

	pageEntries := []tui.TranscriptEntry{
		{Committed: true, Role: tui.TranscriptRoleUser, Text: "before tool"},
		{
			Committed:  true,
			Role:       tui.TranscriptRoleToolCall,
			Text:       "pwd",
			ToolCallID: "call-1",
			ToolCall:   &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
		},
		{Committed: true, Role: tui.TranscriptRoleSystem, Text: "notice after unresolved tool"},
		{Committed: true, Role: tui.TranscriptRoleUser, Text: "user after unresolved tool"},
		{Committed: true, Role: tui.TranscriptRoleAssistant, Text: "assistant after unresolved tool"},
	}
	m.transcriptEntries = pageEntries
	m.transcriptTotalEntries = len(pageEntries)
	m.transcriptRevision = 5
	m.nativeScratchHydrationPending = true

	if err := m.appendNativeScratchTranscript(pageEntries); err != nil {
		t.Fatalf("append scratch transcript: %v", err)
	}

	plain := stripANSIForNativeSpecTest(out.String())
	if !containsInOrder(plain, "before tool", "pwd", "notice after unresolved tool", "user after unresolved tool", "assistant after unresolved tool") {
		t.Fatalf("native scratch transcript omitted committed entries after unresolved tool call, got %q", plain)
	}
}

func TestNativeInvariantPanicKeepsOngoingRendererFromReplayingTranscript(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
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
	t.Setenv("KENT_INVARIANT_MODE", "panic")
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

func TestNativeRuntimeViewCommentaryToolCallEventKeepsCommittedFrontierContiguous(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	seed := []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleUser,
		Text:      "prompt",
	}}
	if err := m.emitNativeCommittedEntries(seed, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	m.transcriptEntries = seed
	m.transcriptRevision = 1
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   0,
		TotalEntries: 1,
		Entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
	})

	assistantEvent := projectRuntimeEvent(runtime.Event{
		Kind:                       runtime.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        3,
		Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: "working",
			Phase:   llm.MessagePhaseCommentary,
			ToolCalls: []llm.ToolCall{{
				ID:    "call-1",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"command":"pwd"}`),
			}},
		},
	})
	if got := len(assistantEvent.TranscriptEntries); got != 2 {
		t.Fatalf("runtimeview assistant event entries = %d, want assistant plus tool call: %+v", got, assistantEvent.TranscriptEntries)
	}
	result := m.runtimeAdapter().applyProjectedRuntimeEvent(assistantEvent)
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("assistant tool-call event result = %+v, want mutation without hydration/fatal", result)
	}
	if got := len(m.transcriptEntries); got != 3 {
		t.Fatalf("transcript entries after assistant event = %d, want 3: %+v", got, m.transcriptEntries)
	}

	localEvent := projectRuntimeEvent(runtime.Event{
		Kind:                       runtime.EventLocalEntryAdded,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         3,
		CommittedEntryStart:        3,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        4,
		LocalEntry:                 &runtime.ChatEntry{Role: "system", Text: "local note"},
	})
	result = m.runtimeAdapter().applyProjectedRuntimeEvent(localEvent)
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("local entry after assistant tool-call event result = %+v, want mutation without hydration/fatal", result)
	}
	if got := len(m.transcriptEntries); got != 4 {
		t.Fatalf("transcript entries after local entry = %d, want 4: %+v", got, m.transcriptEntries)
	}
}

func TestNativeRuntimeViewQueuedUserFlushAfterFinalAssistantKeepsCommittedFrontierContiguous(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	seed := []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleUser,
		Text:      "start",
	}}
	if err := m.emitNativeCommittedEntries(seed, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	m.transcriptEntries = seed
	m.transcriptRevision = 1
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   0,
		TotalEntries: 1,
		Entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
	})

	assistantEvent := projectRuntimeEvent(runtime.Event{
		Kind:                       runtime.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        2,
		Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: "first final",
			Phase:   llm.MessagePhaseFinal,
		},
	})
	result := m.runtimeAdapter().applyProjectedRuntimeEvent(assistantEvent)
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("assistant final event result = %+v, want mutation without hydration/fatal", result)
	}

	flushEvent := projectRuntimeEvent(runtime.Event{
		Kind:                       runtime.EventUserMessageFlushed,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         3,
		CommittedEntryStart:        2,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        3,
		UserMessage:                "steer now",
		UserMessageBatch:           []string{"steer now"},
	})
	result = m.runtimeAdapter().applyProjectedRuntimeEvent(flushEvent)
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("queued user flush after final assistant result = %+v, want mutation without hydration/fatal", result)
	}
	if got := len(m.transcriptEntries); got != 3 {
		t.Fatalf("transcript entries after queued flush = %d, want 3: %+v", got, m.transcriptEntries)
	}
}

func TestNativeRuntimeViewToolMessageBeforeLocalEntryKeepsCommittedFrontierContiguous(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	seed := []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleUser,
		Text:      "start",
	}}
	if err := m.emitNativeCommittedEntries(seed, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	m.transcriptEntries = seed
	m.transcriptRevision = 1
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   0,
		TotalEntries: 1,
		Entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
	})

	toolEvent := projectRuntimeEvent(runtime.Event{
		Kind:                       runtime.EventConversationUpdated,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        2,
		Message: llm.Message{
			Role:       llm.RoleTool,
			ToolCallID: "orphan-call",
			Name:       string(toolspec.ToolExecCommand),
			Content:    `{"output":"done","exit_code":0,"truncated":false}`,
		},
	})
	if got := len(toolEvent.TranscriptEntries); got != 1 {
		t.Fatalf("runtimeview tool message event entries = %d, want 1: %+v", got, toolEvent.TranscriptEntries)
	}
	result := m.runtimeAdapter().applyProjectedRuntimeEvent(toolEvent)
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("tool message event result = %+v, want mutation without hydration/fatal", result)
	}

	localEvent := projectRuntimeEvent(runtime.Event{
		Kind:                       runtime.EventLocalEntryAdded,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         3,
		CommittedEntryStart:        2,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        3,
		LocalEntry:                 &runtime.ChatEntry{Role: "system", Text: "local note"},
	})
	result = m.runtimeAdapter().applyProjectedRuntimeEvent(localEvent)
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("local entry after tool message event result = %+v, want mutation without hydration/fatal", result)
	}
	if got := len(m.transcriptEntries); got != 3 {
		t.Fatalf("transcript entries after local entry = %d, want 3: %+v", got, m.transcriptEntries)
	}
}

func TestNativeStaleUserFlushCoveredByAuthoritativeTailDoesNotPanicOrReplay(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	if err := m.emitNativeCommittedEntries([]tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleSystem,
		Text:      "already emitted before authoritative tail",
	}}, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}

	entries := make([]tui.TranscriptEntry, 226)
	for idx := range entries {
		entries[idx] = tui.TranscriptEntry{
			Committed: true,
			Role:      tui.TranscriptRoleAssistant,
			Text:      "authoritative tail " + strconv.Itoa(idx),
		}
	}
	entries[len(entries)-1] = tui.TranscriptEntry{
		Committed: true,
		Role:      tui.TranscriptRoleUser,
		Text:      "stale queued user",
	}
	m.transcriptBaseOffset = 681
	m.transcriptEntries = entries
	m.transcriptTotalEntries = 907
	m.transcriptRevision = 9778
	m.transcriptLiveDirty = false
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   m.transcriptBaseOffset,
		TotalEntries: m.transcriptTotalEntries,
		Entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
	})

	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		StepID:                     "4c4b3263-f50b-40a8-8eb1-1908c02da4a9",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         9779,
		CommittedEntryStart:        906,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        907,
		UserMessage:                "stale queued user",
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "user",
			Text: "stale queued user",
		}},
	})
	if result.fatal || result.awaitsHydration || result.transcriptMutated {
		t.Fatalf("stale user flush result = %+v, want skip without hydration/fatal/mutation", result)
	}
	if got := len(m.transcriptEntries); got != 226 {
		t.Fatalf("transcript entries = %d, want unchanged 226", got)
	}
	if got := m.transcriptRevision; got != 9779 {
		t.Fatalf("transcript revision = %d, want stale event revision recorded", got)
	}
	if plain := stripANSIForNativeSpecTest(out.String()); strings.Contains(plain, "stale queued user") {
		t.Fatalf("stale covered user flush replayed through native output: %q", plain)
	}
}

func TestNativeOverlappingUserFlushFromLiveTailStillPanics(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	if err := m.emitNativeCommittedEntries([]tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleUser,
		Text:      "already emitted",
	}}, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	m.transcriptEntries = []tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleUser,
		Text:      "live tail user",
	}}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 1
	m.transcriptRevision = 10
	m.transcriptLiveDirty = true

	assertNativeTranscriptInvariantPanic(t, func() {
		m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
			Kind:                       clientui.EventUserMessageFlushed,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         11,
			CommittedEntryStart:        0,
			CommittedEntryStartSet:     true,
			CommittedEntryCount:        1,
			UserMessage:                "live tail user",
			TranscriptEntries: []clientui.ChatEntry{{
				Role: "user",
				Text: "live tail user",
			}},
		})
	})
}

func TestNativeActiveAssistantFinalizerClearsStreamSourceBeforeSameStepToolLoop(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	seed := []tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleUser, Text: "start"}}
	if err := m.emitNativeCommittedEntries(seed, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	m.transcriptEntries = seed
	m.transcriptRevision = 1
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: 1, Entries: append([]tui.TranscriptEntry(nil), seed...)})

	firstStream := "\n\nqa model turn 8 ok\n\n"
	m.appendActiveAssistantStreamDelta("step-1", firstStream, &clientui.AssistantStreamMetadata{
		StepID:                  "step-1",
		BaseRevision:            1,
		BaseCommittedEntryCount: 1,
	})
	if _, err := m.streamNativeAssistantDelta(firstStream, clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream first assistant: %v", err)
	}
	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        3,
		TranscriptEntries: []clientui.ChatEntry{
			{Role: "assistant", Text: firstStream},
			{Role: "tool_call", Text: "printf ok", ToolCallID: "call-1"},
		},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("first assistant event result = %+v, want mutation without fatal/hydration", result)
	}
	if got := m.activeAssistantStreamText(); got != "" {
		t.Fatalf("active assistant stream after committed tool-loop assistant = %q, want cleared", got)
	}

	finalStream := "\n\nqa edit tool 11 ok"
	m.appendActiveAssistantStreamDelta("step-1", finalStream, &clientui.AssistantStreamMetadata{
		StepID:                  "step-1",
		BaseRevision:            2,
		BaseCommittedEntryCount: 3,
	})
	if _, err := m.streamNativeAssistantDelta(finalStream, clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream final assistant: %v", err)
	}
	result = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         3,
		CommittedEntryStart:        3,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        4,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  finalStream,
			Phase: string(clientui.MessagePhaseFinal),
		}},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("final assistant event result = %+v, want mutation without fatal/hydration", result)
	}
}

func TestNativeMissingPhaseAssistantToolCallCommitClearsStreamSourceBeforeSameStepFinal(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	seed := []tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleUser, Text: "start"}}
	if err := m.emitNativeCommittedEntries(seed, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	m.transcriptEntries = seed
	m.transcriptRevision = 1
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: 1, Entries: append([]tui.TranscriptEntry(nil), seed...)})

	firstStream := "ok"
	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:           clientui.EventAssistantDelta,
		StepID:         "step-1",
		AssistantDelta: firstStream,
		AssistantStreamMetadata: &clientui.AssistantStreamMetadata{
			StepID:                  "step-1",
			BaseRevision:            1,
			BaseCommittedEntryCount: 1,
		},
	})
	if result.fatal {
		t.Fatalf("first assistant delta result = %+v", result)
	}
	result = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        3,
		TranscriptEntries: []clientui.ChatEntry{
			{Role: "assistant", Text: firstStream},
			{Role: "tool_call", Text: "printf ok", ToolCallID: "call-1"},
		},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("first assistant event result = %+v, want mutation without fatal/hydration", result)
	}
	if got := m.activeAssistantStreamText(); got != "" {
		t.Fatalf("active assistant stream after missing-phase assistant+tool commit = %q, want cleared", got)
	}
	result = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventToolCallCompleted,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         3,
		CommittedEntryStart:        3,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        4,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_result_ok",
			Text:       "qa tool turn 7 ok",
			ToolCallID: "call-1",
		}},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("tool completion event result = %+v, want mutation without fatal/hydration", result)
	}

	finalStream := "qa real tui turn 7 ok"
	result = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantDelta,
		StepID:                     "step-1",
		AssistantDelta:             finalStream,
		AssistantDeltaPhase:        clientui.MessagePhaseFinal,
		CommittedTranscriptChanged: false,
		AssistantStreamMetadata: &clientui.AssistantStreamMetadata{
			StepID:                  "step-1",
			BaseRevision:            3,
			BaseCommittedEntryCount: 4,
		},
	})
	if result.fatal {
		t.Fatalf("final assistant delta result = %+v", result)
	}
	result = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         4,
		CommittedEntryStart:        4,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        5,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  finalStream,
			Phase: string(clientui.MessagePhaseFinal),
		}},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("final assistant event result = %+v, want mutation without fatal/hydration", result)
	}
}

func TestNativeCommittedRowsAfterUnresolvedToolCallAreEmitted(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	seed := []tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleUser, Text: "start"}}
	if err := m.emitNativeCommittedEntries(seed, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	m.transcriptEntries = seed
	m.transcriptRevision = 1
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: 1, Entries: append([]tui.TranscriptEntry(nil), seed...)})

	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        4,
		TranscriptEntries: []clientui.ChatEntry{
			{
				Role: "tool_call",
				Text: "printf still-running",
				ToolCall: &clientui.ToolCallMeta{
					ToolName:       "exec_command",
					Presentation:   "shell",
					RenderBehavior: "shell",
					IsShell:        true,
					Command:        "printf still-running",
				},
				ToolCallID: "call-1",
			},
			{Role: "user", Text: "queued user message"},
			{Role: "system", Text: "committed notice"},
		},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("event result = %+v, want committed rows emitted without hydration/fatal", result)
	}
	rendered := xansi.Strip(out.String())
	for _, want := range []string{"printf still-running", "queued user message", "committed notice"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("native output missing %q:\n%s", want, rendered)
		}
	}
}

func TestNativeWhitespaceOnlyAssistantToolCallCommitDiscardsStreamBeforeSameStepFinal(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	seed := []tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleUser, Text: "start"}}
	if err := m.emitNativeCommittedEntries(seed, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	m.transcriptEntries = seed
	m.transcriptRevision = 1
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: 1, Entries: append([]tui.TranscriptEntry(nil), seed...)})

	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:           clientui.EventAssistantDelta,
		StepID:         "step-1",
		AssistantDelta: "\n\n",
		AssistantStreamMetadata: &clientui.AssistantStreamMetadata{
			StepID:                  "step-1",
			BaseRevision:            1,
			BaseCommittedEntryCount: 1,
		},
	})
	if result.fatal {
		t.Fatalf("whitespace assistant delta result = %+v", result)
	}
	toolCallEvent := clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        2,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_call",
			Text:       "printf qa_tool_02",
			ToolCallID: "call-1",
		}},
	}
	preToolCallState := newProjectedTranscriptEventState(projectedTranscriptEventSnapshotFromModel(m))
	preToolCallReduction := reduceProjectedTranscriptEvent(preToolCallState, toolCallEvent)
	result = m.runtimeAdapter().applyProjectedRuntimeEvent(toolCallEvent)
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("whitespace assistant tool-call event result = %+v, state_live=%q state_step=%q reduction=%+v, want mutation without fatal/hydration", result, preToolCallState.liveAssistantText, preToolCallState.liveAssistantStepID, preToolCallReduction)
	}
	if got := m.activeAssistantStreamText(); got != "" {
		t.Fatalf("active assistant stream after whitespace assistant tool-call commit = %q, want cleared", got)
	}
	if m.nativeSurface.AssistantStreaming() {
		t.Fatal("native assistant stream still active after whitespace assistant tool-call commit")
	}
	result = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventToolCallCompleted,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         3,
		CommittedEntryStart:        2,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        3,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_result_ok",
			Text:       "qa_tool_02",
			ToolCallID: "call-1",
		}},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("tool completion event result = %+v, want mutation without fatal/hydration", result)
	}
	if got := m.activeAssistantStreamText(); got != "" {
		t.Fatalf("active assistant stream after whitespace assistant+tool commit = %q, want cleared", got)
	}
	if m.nativeSurface.AssistantStreaming() {
		t.Fatal("native assistant stream still active after whitespace assistant+tool commit")
	}

	finalStream := "\n\nqa turn 02 tool ok"
	result = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantDelta,
		StepID:                     "step-1",
		AssistantDelta:             finalStream,
		AssistantDeltaPhase:        clientui.MessagePhaseFinal,
		CommittedTranscriptChanged: false,
		AssistantStreamMetadata: &clientui.AssistantStreamMetadata{
			StepID:                  "step-1",
			BaseRevision:            3,
			BaseCommittedEntryCount: 3,
		},
	})
	if result.fatal {
		t.Fatalf("final assistant delta result = %+v", result)
	}
	result = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         4,
		CommittedEntryStart:        3,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        4,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  finalStream,
			Phase: string(clientui.MessagePhaseFinal),
		}},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("final assistant event result = %+v, want mutation without fatal/hydration", result)
	}
}

func TestNativeAssistantCommentaryWithToolCallsKeepsCommittedTailContiguousBeforeLocalEntry(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModelWithClient(&out, &runtimeControlFakeClient{})
	seed := make([]tui.TranscriptEntry, 677)
	for idx := range seed {
		seed[idx] = tui.TranscriptEntry{Committed: true, Role: tui.TranscriptRoleSystem, Text: "seed " + strconv.Itoa(idx)}
	}
	if err := m.emitNativeCommittedEntries(seed, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	m.transcriptEntries = append([]tui.TranscriptEntry(nil), seed...)
	m.transcriptRevision = 13038
	m.transcriptTotalEntries = len(seed)
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: len(seed), Entries: append([]tui.TranscriptEntry(nil), seed...)})

	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         13039,
		CommittedEntryStart:        677,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        679,
		TranscriptEntries: []clientui.ChatEntry{
			{
				Role:  "assistant",
				Text:  "The prod tmux TUI is attached and actively rendering this current session.",
				Phase: string(clientui.MessagePhaseCommentary),
			},
			{Role: "tool_call", Text: "tmux list-panes", ToolCallID: "call-1"},
		},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("assistant commentary tool-call event result = %+v, want mutation without fatal/hydration", result)
	}
	if got := len(m.transcriptEntries); got != 679 {
		t.Fatalf("transcript entries after assistant event = %d, want 679", got)
	}

	result = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         13040,
		CommittedEntryStart:        679,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        680,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "reasoning",
			Text: "**Monitoring tmux session status**",
		}},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("local entry after assistant tool-call event result = %+v, want mutation without fatal/hydration", result)
	}
	if got := len(m.transcriptEntries); got != 680 {
		t.Fatalf("transcript entries after local entry = %d, want 680", got)
	}
}

func TestNativeActiveAssistantCommentaryFinalizerOwnsSameEventToolCallRowsBeforeLocalEntry(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModelWithClient(&out, &runtimeControlFakeClient{})
	seed := make([]tui.TranscriptEntry, 676)
	for idx := range seed {
		seed[idx] = tui.TranscriptEntry{Committed: true, Role: tui.TranscriptRoleSystem, Text: "seed " + strconv.Itoa(idx)}
	}
	if err := m.emitNativeCommittedEntries(seed, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	m.transcriptEntries = append([]tui.TranscriptEntry(nil), seed...)
	m.transcriptRevision = 13038
	m.transcriptTotalEntries = len(seed)
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: len(seed), Entries: append([]tui.TranscriptEntry(nil), seed...)})

	commentary := "The prod tmux TUI is attached and actively rendering this current session."
	m.appendActiveAssistantStreamDelta("step-1", commentary, &clientui.AssistantStreamMetadata{
		StepID:                  "step-1",
		BaseRevision:            13038,
		BaseCommittedEntryCount: 676,
	})
	if _, err := m.streamNativeAssistantDelta(commentary, clientui.MessagePhaseCommentary); err != nil {
		t.Fatalf("stream native commentary: %v", err)
	}
	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         13039,
		CommittedEntryStart:        676,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        679,
		TranscriptEntries: []clientui.ChatEntry{
			{Role: "assistant", Text: commentary, Phase: string(clientui.MessagePhaseCommentary)},
			{Role: "tool_call", Text: "tmux list-panes", ToolCallID: "call-1"},
			{Role: "tool_call", Text: "tail tui.log", ToolCallID: "call-2"},
		},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("assistant commentary tool-call event result = %+v, want mutation without fatal/hydration", result)
	}
	if got := len(m.transcriptEntries); got != 679 {
		t.Fatalf("transcript entries after assistant event = %d, want 679", got)
	}
	if got := m.activeAssistantStreamText(); got != "" {
		t.Fatalf("active stream after assistant finalizer = %q, want cleared", got)
	}

	result = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         13040,
		CommittedEntryStart:        679,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        680,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "reasoning",
			Text: "**Monitoring tmux session status**",
		}},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("local entry after active finalizer tool-call event result = %+v, want mutation without fatal/hydration", result)
	}
	if got := len(m.transcriptEntries); got != 680 {
		t.Fatalf("transcript entries after local entry = %d, want 680", got)
	}
}

func TestNativeActiveAssistantCommentaryWithThreeToolCallsOwnsRowsBeforeFirstToolResult(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModelWithClient(&out, &runtimeControlFakeClient{})
	m.setRuntimeActivityBusyForTest(true)
	seed := make([]tui.TranscriptEntry, 656)
	for idx := range seed {
		seed[idx] = tui.TranscriptEntry{Committed: true, Role: tui.TranscriptRoleSystem, Text: "seed " + strconv.Itoa(idx)}
	}
	if err := m.emitNativeCommittedEntries(seed, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	m.transcriptEntries = append([]tui.TranscriptEntry(nil), seed...)
	m.transcriptRevision = 14203
	m.transcriptTotalEntries = len(seed)
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: len(seed), Entries: append([]tui.TranscriptEntry(nil), seed...)})

	commentary := "I’m addressing the supervisor findings before continuing proof."
	m.appendActiveAssistantStreamDelta("step-1", commentary, &clientui.AssistantStreamMetadata{
		StepID:                  "step-1",
		BaseRevision:            14203,
		BaseCommittedEntryCount: 656,
	})
	if _, err := m.streamNativeAssistantDelta(commentary, clientui.MessagePhaseCommentary); err != nil {
		t.Fatalf("stream native commentary: %v", err)
	}
	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         14204,
		CommittedEntryStart:        656,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        660,
		TranscriptEntries: []clientui.ChatEntry{
			{Role: "assistant", Text: commentary, Phase: string(clientui.MessagePhaseCommentary)},
			{Role: "tool_call", Text: "driver status", ToolCallID: "call-1"},
			{Role: "tool_call", Text: "diff status", ToolCallID: "call-2"},
			{Role: "tool_call", Text: "git status", ToolCallID: "call-3"},
		},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("assistant commentary tool-call event result = %+v, want mutation without fatal/hydration", result)
	}
	if got := len(m.transcriptEntries); got != 660 {
		t.Fatalf("transcript entries after assistant event = %d, want 660", got)
	}

	for idx, text := range []string{"**Handling supervisor wake and code review**", "**Assessing driver process and script updates**"} {
		result = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
			Kind:                       clientui.EventLocalEntryAdded,
			StepID:                     "step-1",
			CommittedTranscriptChanged: true,
			TranscriptRevision:         int64(14205 + idx),
			CommittedEntryStart:        660 + idx,
			CommittedEntryStartSet:     true,
			CommittedEntryCount:        661 + idx,
			TranscriptEntries: []clientui.ChatEntry{{
				Role: "reasoning",
				Text: text,
			}},
		})
		if result.fatal || result.awaitsHydration || !result.transcriptMutated {
			t.Fatalf("local entry %d result = %+v, want mutation without fatal/hydration", idx, result)
		}
	}
	if got := len(m.transcriptEntries); got != 662 {
		t.Fatalf("transcript entries after local entries = %d, want 662", got)
	}

	result = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventToolCallCompleted,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         14207,
		CommittedEntryStart:        662,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        663,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_result_ok",
			Text:       "driver pid: 81062",
			ToolCallID: "call-1",
		}},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("first tool result after commentary tool calls = %+v, want mutation without fatal/hydration", result)
	}
	if got := len(m.transcriptEntries); got != 663 {
		t.Fatalf("transcript entries after first tool result = %d, want 663", got)
	}
}

func TestNativeCommittedTailCountsPendingToolCallRowsAsOwned(t *testing.T) {
	m := newNativeSurfaceSpecTestModelWithClient(&bytes.Buffer{}, &runtimeControlFakeClient{})
	m.transcriptBaseOffset = 0
	m.transcriptRevision = 13039
	m.transcriptTotalEntries = 679
	m.transcriptEntries = make([]tui.TranscriptEntry, 679)
	for idx := range 677 {
		m.transcriptEntries[idx] = tui.TranscriptEntry{Committed: true, Role: tui.TranscriptRoleSystem, Text: "seed " + strconv.Itoa(idx)}
	}
	m.transcriptEntries[677] = tui.TranscriptEntry{Committed: true, Role: tui.TranscriptRoleAssistant, Text: "commentary", Phase: clientui.MessagePhaseCommentary}
	m.transcriptEntries[678] = tui.TranscriptEntry{Committed: true, Role: tui.TranscriptRoleToolCall, Text: "tmux list-panes", ToolCallID: "call-1"}

	revision, ownedTail := committedTranscriptOwnedTailIncludingDeferredTail(m)
	if revision != 13039 {
		t.Fatalf("owned revision = %d, want 13039", revision)
	}
	if ownedTail != 679 {
		t.Fatalf("owned tail = %d, want 679 pending tool-call row counted as server-owned", ownedTail)
	}
}

func TestNativeStaleAssistantFinalizerCoveredByAuthoritativeTailDoesNotPanicOrReplay(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	if err := m.emitNativeCommittedEntries([]tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleSystem,
		Text:      "already emitted before authoritative assistant tail",
	}}, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}

	entries := make([]tui.TranscriptEntry, 358)
	for idx := range entries {
		entries[idx] = tui.TranscriptEntry{
			Committed: true,
			Role:      tui.TranscriptRoleSystem,
			Text:      "authoritative assistant tail " + strconv.Itoa(idx),
		}
	}
	streamPrefix := strings.Repeat("x", 120)
	finalText := streamPrefix + strings.Repeat("y", 70)
	entries[len(entries)-1] = tui.TranscriptEntry{
		Committed: true,
		Role:      tui.TranscriptRoleAssistant,
		Text:      finalText,
		Phase:     clientui.MessagePhaseFinal,
	}
	m.transcriptBaseOffset = 942
	m.transcriptEntries = entries
	m.transcriptTotalEntries = 1302
	m.transcriptRevision = 10400
	m.transcriptLiveDirty = false
	m.appendActiveAssistantStreamDelta("4c4b3263-f50b-40a8-8eb1-1908c02da4a9", streamPrefix, nil)
	if _, err := m.streamNativeAssistantDelta(streamPrefix, clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant: %v", err)
	}
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   m.transcriptBaseOffset,
		TotalEntries: m.transcriptTotalEntries,
		Entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
		Ongoing:      streamPrefix,
	})

	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "4c4b3263-f50b-40a8-8eb1-1908c02da4a9",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         10403,
		CommittedEntryStart:        1299,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        1302,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  finalText,
			Phase: string(clientui.MessagePhaseFinal),
		}},
	})
	if result.fatal || result.awaitsHydration || result.transcriptMutated {
		t.Fatalf("stale assistant finalizer result = %+v, want skip without hydration/fatal/mutation", result)
	}
	if got := m.activeAssistantStreamText(); got != "" {
		t.Fatalf("active assistant stream after stale finalizer = %q, want cleared", got)
	}
	if plain := stripANSIForNativeSpecTest(out.String()); !strings.Contains(plain, strings.Repeat("y", 70)) {
		t.Fatalf("stale finalizer suffix was not emitted before finish: %q", plain)
	}
	if got := m.transcriptRevision; got != 10403 {
		t.Fatalf("transcript revision = %d, want stale finalizer revision recorded", got)
	}
	if got := m.transcriptTotalEntries; got != 1302 {
		t.Fatalf("transcript total entries = %d, want known authoritative total preserved", got)
	}
}

func TestNativeStaleAssistantFinalizerCannotRevealUnknownLaterRows(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	if err := m.emitNativeCommittedEntries([]tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleSystem,
		Text:      "already emitted",
	}}, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	finalText := "final text"
	m.transcriptBaseOffset = 9
	m.transcriptEntries = []tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleAssistant, Text: finalText, Phase: clientui.MessagePhaseFinal}}
	m.transcriptTotalEntries = 10
	m.transcriptRevision = 20
	m.transcriptLiveDirty = false
	m.appendActiveAssistantStreamDelta("step-1", finalText, nil)
	if _, err := m.streamNativeAssistantDelta(finalText, clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant: %v", err)
	}

	assertNativeTranscriptInvariantPanic(t, func() {
		m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
			Kind:                       clientui.EventAssistantMessage,
			StepID:                     "step-1",
			CommittedTranscriptChanged: true,
			TranscriptRevision:         21,
			CommittedEntryStart:        9,
			CommittedEntryStartSet:     true,
			CommittedEntryCount:        12,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  finalText,
				Phase: string(clientui.MessagePhaseFinal),
			}},
		})
	})
}

func TestNativeCommittedToolResultIgnoresTrailingTransientMutableRows(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	if err := m.emitNativeCommittedEntries([]tui.TranscriptEntry{{
		Committed: true,
		Role:      tui.TranscriptRoleSystem,
		Text:      "already emitted",
	}}, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}

	entries := make([]tui.TranscriptEntry, 35)
	for idx := 0; idx < 34; idx++ {
		entries[idx] = tui.TranscriptEntry{
			Committed: true,
			Role:      tui.TranscriptRoleSystem,
			Text:      "committed " + strconv.Itoa(idx),
		}
	}
	entries[34] = tui.TranscriptEntry{
		Transient: true,
		Role:      tui.TranscriptRoleSystem,
		Text:      "mutable background status",
	}
	m.transcriptEntries = entries
	m.transcriptTotalEntries = 34
	m.transcriptRevision = 49
	m.transcriptLiveDirty = true
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   0,
		TotalEntries: m.transcriptTotalEntries,
		Entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
	})

	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventToolCallCompleted,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         50,
		CommittedEntryStart:        34,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        35,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_result_ok",
			Text:       "done",
			ToolCallID: "call-1",
		}},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("tool result event result = %+v, want committed mutation without fatal/hydration", result)
	}
	if got := len(m.transcriptEntries); got != 35 {
		t.Fatalf("transcript entries = %d, want 35", got)
	}
	last := m.transcriptEntries[len(m.transcriptEntries)-1]
	if last.Transient || !last.Committed || last.Role != tui.TranscriptRoleToolResultOK || last.ToolCallID != "call-1" {
		t.Fatalf("last entry = %+v, want committed tool result replacing transient mutable row", last)
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

func TestNativeLiveChatPanelProjectsPendingRowsAtFrameWidth(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	m.termWidth = 77
	m.termHeight = 34
	m.windowSizeKnown = true
	m.forwardToView(tui.SetViewportSizeMsg{Width: 100, Lines: 30})
	m.forwardToView(tui.AppendTranscriptMsg{
		Role:       tui.TranscriptRoleToolCall,
		Text:       "./apps/desktop/src/features/workflow-editor/WorkflowDraftInspector.test.tsx -5",
		ToolCallID: "call-1",
		ToolCall: &transcript.ToolCallMeta{
			ToolName: "shell",
			IsShell:  true,
			Command:  "./apps/desktop/src/features/workflow-editor/WorkflowDraftInspector.test.tsx -5",
		},
	})

	rendered := m.layout().renderNativeLiveAreaFrame(uiRenderFrame{
		width:      77,
		height:     34,
		chatPanel:  m.layout().renderNativeLiveChatPanel(77, 4, uiThemeStyles(m.theme)),
		inputPane:  []string{"› " + strings.Repeat(" ", 75)},
		statusLine: "\x1b[38;2;48;133;252m ⡱ goal\x1b[0m \x1b[2;38;2;143;151;161mgpt-5.5 medium\x1b[0m \x1b[1;38;2;48;133;252mHandling patch confli…\x1b[0m",
		tailOnly:   true,
	})

	if strings.TrimSpace(rendered) != "" {
		t.Fatalf("native live frame should write directly to terminal, got fallback render %q", rendered)
	}
	if m.nativeLiveAreaError != nil {
		t.Fatalf("native live frame returned error: %v", m.nativeLiveAreaError)
	}
	if m.nativeSurface == nil || !m.nativeSurface.lastFrameSet {
		t.Fatal("expected native live frame to render")
	}
	for idx, line := range m.nativeSurface.lastFrame.Lines {
		if got := xansi.StringWidth(line); got > 77 {
			t.Fatalf("native live frame line %d width = %d, want <= 77: %q", idx, got, line)
		}
	}
	if len(m.nativeSurface.lastFrame.Lines) < 2 {
		t.Fatalf("native live frame lines = %q, want wrapped pending row plus input/status", m.nativeSurface.lastFrame.Lines)
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

func TestNativeAssistantStreamPromotesStableMarkdownRowsBeforeFinalizer(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	if handled, err := m.streamNativeAssistantDelta("**stable**\n\n", clientui.MessagePhaseCommentary); err != nil {
		t.Fatalf("stream stable assistant block: %v", err)
	} else if !handled {
		t.Fatal("expected native surface to handle stable assistant block")
	}
	if plain := stripANSIForNativeSpecTest(out.String()); strings.Contains(plain, "stable") {
		t.Fatalf("assistant block promoted before following block made it stable: %q", plain)
	}

	if handled, err := m.streamNativeAssistantDelta("mutable tail\n", clientui.MessagePhaseCommentary); err != nil {
		t.Fatalf("stream mutable assistant tail: %v", err)
	} else if !handled {
		t.Fatal("expected native surface to handle mutable assistant tail")
	}
	plain := stripANSIForNativeSpecTest(out.String())
	if !strings.Contains(plain, "stable") {
		t.Fatalf("expected stable assistant block promoted before finalizer, got %q", plain)
	}
	if strings.Contains(plain, "**") {
		t.Fatalf("stable assistant block promoted raw markdown markers: %q", plain)
	}
	tail := stripANSIForNativeSpecTest(strings.Join(m.nativeSurface.AssistantStreamTailLines(), "\n"))
	if strings.Contains(tail, "stable") || !strings.Contains(tail, "mutable tail") {
		t.Fatalf("expected only mutable assistant tail after promotion, got %q", tail)
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

func TestNativeAssistantFinalizerAllowsLeadingDeferredCommittedRows(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	var out bytes.Buffer
	m := newNativeSurfaceSpecTestModel(&out)
	seed := []tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleUser, Text: "done"}}
	if err := m.emitNativeCommittedEntries(seed, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	m.transcriptEntries = append([]tui.TranscriptEntry(nil), seed...)
	m.transcriptRevision = 1
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: 1, Entries: append([]tui.TranscriptEntry(nil), seed...)})

	commentary := "Restart confirmed. I’m verifying the daemon and then I’ll relaunch the tmux watcher from fresh offsets."
	m.appendActiveAssistantStreamDelta("step-1", commentary, &clientui.AssistantStreamMetadata{
		StepID:                  "step-1",
		BaseRevision:            1,
		BaseCommittedEntryCount: 1,
	})
	if _, err := m.streamNativeAssistantDelta(commentary, clientui.MessagePhaseCommentary); err != nil {
		t.Fatalf("stream assistant commentary: %v", err)
	}

	cacheResult := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventCacheWarning,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        2,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "system", Text: "conversation cache warning"}},
	})
	if cacheResult.fatal || cacheResult.awaitsHydration || cacheResult.transcriptMutated {
		t.Fatalf("cache warning result = %+v, want deferred while native stream is active", cacheResult)
	}

	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         3,
		CommittedEntryStart:        2,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        5,
		TranscriptEntries: []clientui.ChatEntry{
			{Role: "assistant", Text: commentary, Phase: string(clientui.MessagePhaseCommentary)},
			{Role: "tool_call", Text: "service status", ToolCallID: "call-1"},
			{Role: "tool_call", Text: "ps kent", ToolCallID: "call-2"},
		},
	})
	if result.fatal || result.awaitsHydration || !result.transcriptMutated {
		t.Fatalf("event result = %+v, want leading committed row queued behind finalizer without fatal/hydration", result)
	}
	if got := m.activeAssistantStreamText(); got != "" {
		t.Fatalf("active stream after finalizer = %q, want cleared", got)
	}
	rendered := stripANSIForNativeSpecTest(out.String())
	for _, want := range []string{"Restart confirmed", "watcher from fresh", "offsets.", "conversation cache warning", "service status", "ps kent"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("native output missing %q:\n%s", want, rendered)
		}
	}
	assistantIndex := strings.Index(rendered, "Restart confirmed")
	cacheIndex := strings.Index(rendered, "conversation cache warning")
	firstToolIndex := strings.Index(rendered, "service status")
	secondToolIndex := strings.Index(rendered, "ps kent")
	if !(assistantIndex >= 0 && assistantIndex < cacheIndex && cacheIndex < firstToolIndex && firstToolIndex < secondToolIndex) {
		t.Fatalf("native output order = assistant:%d cache:%d tool1:%d tool2:%d\n%s", assistantIndex, cacheIndex, firstToolIndex, secondToolIndex, rendered)
	}
	if got := len(m.transcriptEntries); got != 5 {
		t.Fatalf("transcript entries = %d, want 5", got)
	}
	if m.transcriptEntries[1].Role != tui.TranscriptRoleSystem || m.transcriptEntries[1].Text != "conversation cache warning" {
		t.Fatalf("transcript entry 1 = %+v, want leading committed system row", m.transcriptEntries[1])
	}
	if m.transcriptEntries[2].Role != tui.TranscriptRoleAssistant || m.transcriptEntries[2].Text != commentary {
		t.Fatalf("transcript entry 2 = %+v, want assistant finalizer row", m.transcriptEntries[2])
	}
	if m.transcriptEntries[3].Role != tui.TranscriptRoleToolCall || m.transcriptEntries[3].ToolCallID != "call-1" {
		t.Fatalf("transcript entry 3 = %+v, want first tool call row", m.transcriptEntries[3])
	}
	if m.transcriptEntries[4].Role != tui.TranscriptRoleToolCall || m.transcriptEntries[4].ToolCallID != "call-2" {
		t.Fatalf("transcript entry 4 = %+v, want second tool call row", m.transcriptEntries[4])
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
	m.appendActiveAssistantStreamDelta("step-1", "hello", nil)
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

func TestNativeAssistantFinalizerMismatchDiagnosticModeReturnsBeforeTranscriptMutation(t *testing.T) {
	t.Setenv("KENT_DEBUG", "false")
	t.Setenv("KENT_INVARIANT_MODE", "")
	logger := &testUILogger{}
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	m.logger = logger
	m.appendActiveAssistantStreamDelta("step-1", "hello", nil)
	if _, err := m.streamNativeAssistantDelta("hello", clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}

	_, mutated, awaitsHydration, fatal := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
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
	if !fatal {
		t.Fatal("expected diagnostic-mode native invariant to stop event application")
	}
	if mutated || awaitsHydration {
		t.Fatalf("diagnostic-mode native invariant mutated=%t awaitsHydration=%t, want both false", mutated, awaitsHydration)
	}
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
	m.appendActiveAssistantStreamDelta("step-1", "hello", nil)
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
	m.appendActiveAssistantStreamDelta("", "hello", nil)
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

func TestNativeSameStepDifferentStreamFrontierPanicsBeforeStreamMutation(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	for idx := 0; idx < 7; idx++ {
		m.transcriptEntries = append(m.transcriptEntries, tui.TranscriptEntry{Committed: true, Role: tui.TranscriptRoleSystem, Text: "seed " + strconv.Itoa(idx)})
	}
	m.transcriptTotalEntries = 7
	m.transcriptRevision = 12
	m.appendActiveAssistantStreamDelta("step-1", "hello", &clientui.AssistantStreamMetadata{
		StepID:                  "step-1",
		BaseRevision:            10,
		BaseCommittedEntryCount: 5,
	})
	if _, err := m.streamNativeAssistantDelta("hello", clientui.MessagePhaseCommentary); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}

	assertNativeTranscriptInvariantPanic(t, func() {
		m.handleRuntimeEventBatch([]clientui.Event{{
			Kind:                clientui.EventAssistantDelta,
			StepID:              "step-1",
			AssistantDelta:      "new segment",
			AssistantDeltaPhase: clientui.MessagePhaseCommentary,
			AssistantStreamMetadata: &clientui.AssistantStreamMetadata{
				StepID:                  "step-1",
				BaseRevision:            12,
				BaseCommittedEntryCount: 7,
			},
		}})
	})
	if got := m.activeAssistantStreamText(); got != "hello" {
		t.Fatalf("expected active stream source unchanged after fatal same-step segment violation, got %q", got)
	}
	if m.sawAssistantDelta {
		t.Fatal("same-step segment delta mutated assistant stream state after fatal native violation")
	}
}

func TestNativeAssistantDeltaWithoutStreamMetadataPanicsBeforeStreamMutation(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})

	assertNativeTranscriptInvariantPanic(t, func() {
		m.handleRuntimeEventBatch([]clientui.Event{{
			Kind:                clientui.EventAssistantDelta,
			StepID:              "step-1",
			AssistantDelta:      "missing metadata",
			AssistantDeltaPhase: clientui.MessagePhaseCommentary,
		}})
	})
	if got := m.activeAssistantStreamText(); got != "" {
		t.Fatalf("active stream source = %q, want no mutation after missing metadata", got)
	}
	if m.sawAssistantDelta {
		t.Fatal("missing-metadata delta mutated assistant stream state after fatal native violation")
	}
	if m.nativeSurface.AssistantStreaming() {
		t.Fatal("missing-metadata delta started native assistant streaming")
	}
}

func TestNativeAssistantFinalizerWithUnknownStreamFrontierPanicsBeforePrefixMatch(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	seed := []tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleUser, Text: "start"}}
	if err := m.emitNativeCommittedEntries(seed, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	m.transcriptEntries = seed
	m.transcriptRevision = 1
	m.transcriptTotalEntries = 1
	m.appendActiveAssistantStreamDelta("step-1", "same text", nil)
	if _, err := m.streamNativeAssistantDelta("same text", clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}

	assertNativeTranscriptInvariantPanic(t, func() {
		m.handleRuntimeEventBatch([]clientui.Event{{
			Kind:                       clientui.EventAssistantMessage,
			StepID:                     "step-1",
			CommittedTranscriptChanged: true,
			TranscriptRevision:         2,
			CommittedEntryStart:        1,
			CommittedEntryStartSet:     true,
			CommittedEntryCount:        2,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  "same text",
				Phase: string(clientui.MessagePhaseFinal),
			}},
		}})
	})
	if got := m.activeAssistantStreamText(); got != "same text" {
		t.Fatalf("active stream source = %q, want preserved after unknown-frontier finalizer", got)
	}
}

func TestNativeHydratedSameStepDifferentStreamFrontierRequestsRecovery(t *testing.T) {
	client := &runtimeControlFakeClient{transcript: clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  20,
		Entries: []clientui.ChatEntry{{
			Role: "assistant",
			Text: "authoritative recovered tail",
		}},
	}}
	m := newNativeSurfaceSpecTestModelWithClient(&bytes.Buffer{}, client)
	for idx := 0; idx < 7; idx++ {
		m.transcriptEntries = append(m.transcriptEntries, tui.TranscriptEntry{Committed: true, Role: tui.TranscriptRoleSystem, Text: "seed " + strconv.Itoa(idx)})
	}
	m.transcriptTotalEntries = 7
	m.transcriptRevision = 12
	m.refreshActiveAssistantStreamFromAuthoritativePageStreaming("newer segment", &clientui.AssistantStreamMetadata{
		StepID:                  "step-1",
		BaseRevision:            12,
		BaseCommittedEntryCount: 7,
	})
	if _, err := m.streamNativeAssistantDelta("newer segment", clientui.MessagePhaseCommentary); err != nil {
		t.Fatalf("stream hydrated assistant segment: %v", err)
	}

	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-1",
		AssistantDelta:      "older segment",
		AssistantDeltaPhase: clientui.MessagePhaseCommentary,
		AssistantStreamMetadata: &clientui.AssistantStreamMetadata{
			StepID:                  "step-1",
			BaseRevision:            10,
			BaseCommittedEntryCount: 5,
		},
	})

	if result.fatal || !result.awaitsHydration || result.transcriptMutated {
		t.Fatalf("result = %+v, want stream-gap hydration barrier without mutation/fatal", result)
	}
	if got := m.activeAssistantStreamText(); got != "newer segment" {
		t.Fatalf("active stream source = %q, want hydrated newer segment preserved", got)
	}
}

func TestNativeCoveredCommittedBacklogOlderThanActiveStreamFrontierSkips(t *testing.T) {
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	for idx := 0; idx < 7; idx++ {
		m.transcriptEntries = append(m.transcriptEntries, tui.TranscriptEntry{Committed: true, Role: tui.TranscriptRoleSystem, Text: "seed " + strconv.Itoa(idx)})
	}
	m.transcriptTotalEntries = 7
	m.transcriptRevision = 12
	m.refreshActiveAssistantStreamFromAuthoritativePageStreaming("newer segment", &clientui.AssistantStreamMetadata{
		StepID:                  "step-1",
		BaseRevision:            12,
		BaseCommittedEntryCount: 7,
	})
	if _, err := m.streamNativeAssistantDelta("newer segment", clientui.MessagePhaseCommentary); err != nil {
		t.Fatalf("stream hydrated assistant segment: %v", err)
	}

	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         13,
		CommittedEntryStart:        5,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        6,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "covered older segment",
			Phase: string(clientui.MessagePhaseCommentary),
		}},
	})

	if result.fatal || result.awaitsHydration || result.transcriptMutated {
		t.Fatalf("result = %+v, want covered stale backlog skip without hydration/fatal", result)
	}
	if got := len(m.transcriptEntries); got != 7 {
		t.Fatalf("transcript entry count = %d, want no duplicate append", got)
	}
	if got := m.activeAssistantStreamText(); got != "newer segment" {
		t.Fatalf("active stream source = %q, want hydrated newer segment preserved", got)
	}
}

func TestNativeMissingCommittedBacklogOlderThanLiveStreamFrontierPanics(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	for idx := 0; idx < 5; idx++ {
		m.transcriptEntries = append(m.transcriptEntries, tui.TranscriptEntry{Committed: true, Role: tui.TranscriptRoleSystem, Text: "seed " + strconv.Itoa(idx)})
	}
	m.transcriptTotalEntries = 7
	m.transcriptRevision = 12
	m.appendActiveAssistantStreamDelta("step-1", "live segment", &clientui.AssistantStreamMetadata{
		StepID:                  "step-1",
		BaseRevision:            12,
		BaseCommittedEntryCount: 7,
	})
	if _, err := m.streamNativeAssistantDelta("live segment", clientui.MessagePhaseCommentary); err != nil {
		t.Fatalf("stream live assistant segment: %v", err)
	}

	assertNativeTranscriptInvariantPanic(t, func() {
		m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
			Kind:                       clientui.EventAssistantMessage,
			StepID:                     "step-1",
			CommittedTranscriptChanged: true,
			TranscriptRevision:         13,
			CommittedEntryStart:        5,
			CommittedEntryStartSet:     true,
			CommittedEntryCount:        6,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  "missing older segment",
				Phase: string(clientui.MessagePhaseCommentary),
			}},
		})
	})
	if got := len(m.transcriptEntries); got != 5 {
		t.Fatalf("transcript entry count = %d, want no mutation after fatal stale backlog", got)
	}
	if got := m.activeAssistantStreamText(); got != "live segment" {
		t.Fatalf("active stream source = %q, want live segment preserved", got)
	}
}

func TestNativeStaleResetMismatchedStreamFrontierDoesNotClearHydratedStream(t *testing.T) {
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	m.refreshActiveAssistantStreamFromAuthoritativePageStreaming("newer segment", &clientui.AssistantStreamMetadata{
		StepID:                  "step-1",
		BaseRevision:            12,
		BaseCommittedEntryCount: 7,
	})
	if _, err := m.streamNativeAssistantDelta("newer segment", clientui.MessagePhaseCommentary); err != nil {
		t.Fatalf("stream hydrated assistant segment: %v", err)
	}

	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:   clientui.EventAssistantDeltaReset,
		StepID: "step-1",
		AssistantStreamMetadata: &clientui.AssistantStreamMetadata{
			StepID:                  "step-1",
			BaseRevision:            10,
			BaseCommittedEntryCount: 5,
		},
	})

	if result.fatal || result.awaitsHydration || result.transcriptMutated {
		t.Fatalf("result = %+v, want stale reset ignored without mutation/fatal", result)
	}
	if got := m.activeAssistantStreamText(); got != "newer segment" {
		t.Fatalf("active stream source = %q, want hydrated newer segment preserved", got)
	}
	if !m.nativeSurface.AssistantStreaming() {
		t.Fatal("stale reset cleared native assistant streaming")
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

func TestNativeNonGapCommittedDivergenceDiagnosticModeReturnsWithoutHydration(t *testing.T) {
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

	_, mutated, awaitsHydration, fatal := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
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
	if !fatal {
		t.Fatal("expected diagnostic-mode native invariant to stop event application")
	}
	if mutated || awaitsHydration {
		t.Fatalf("diagnostic-mode native divergence mutated=%t awaitsHydration=%t, want both false", mutated, awaitsHydration)
	}
	if got := len(m.transcriptEntries); got != 1 {
		t.Fatalf("expected transcript entries unchanged, got %d", got)
	}
	if len(logger.lines) == 0 {
		t.Fatalf("expected native divergence diagnostics in TUI log, got %#v", logger.lines)
	}
}

func TestNativeNonAppendReplacePanicsBeforeTranscriptMutation(t *testing.T) {
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
	m.appendActiveAssistantStreamDelta("step-1", "hello", nil)
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

func TestNativeAssistantFinalizerReturnsCommittedRowsPrecedingFinalizer(t *testing.T) {
	m := newNativeSurfaceSpecTestModel(&bytes.Buffer{})
	if _, err := m.streamNativeAssistantDelta("hello", clientui.MessagePhaseFinal); err != nil {
		t.Fatalf("stream assistant delta: %v", err)
	}

	entries := []tui.TranscriptEntry{
		{Committed: true, Role: tui.TranscriptRoleSystem, Text: "cannot precede finalizer"},
		{Committed: true, Role: tui.TranscriptRoleAssistant, Text: "hello", Phase: clientui.MessagePhaseFinal},
	}
	remaining, skippedLeading, err := m.nativeCommittedEntriesAfterActiveAssistantFinalizer(entries, "hello")
	if err != nil {
		t.Fatalf("finalize assistant stream with leading committed row: %v", err)
	}
	if skippedLeading != 0 {
		t.Fatalf("skipped leading = %d, want 0 when a committed row precedes the finalizer", skippedLeading)
	}
	if len(remaining) != 1 || remaining[0].Text != "cannot precede finalizer" {
		t.Fatalf("remaining entries = %+v, want leading committed row returned for stable append", remaining)
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

func TestNativeDeferredCommittedEventDoesNotDropTrailingTransientRows(t *testing.T) {
	m := newNativeSurfaceSpecTestModelWithClient(&bytes.Buffer{}, &runtimeControlFakeClient{})
	m.transcriptBaseOffset = 0
	m.transcriptEntries = []tui.TranscriptEntry{
		{Committed: true, Role: tui.TranscriptRoleAssistant, Text: "committed"},
		{Transient: true, Role: tui.TranscriptRoleToolResultOK, Text: "pending mutable status"},
	}
	m.forwardToView(tui.SetConversationMsg{Entries: append([]tui.TranscriptEntry(nil), m.transcriptEntries...)})
	m.appendActiveAssistantStreamDelta("step-1", "streaming", nil)

	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        2,
		TranscriptRevision:         2,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "system",
			Text: "committed after stream",
		}},
	})

	if result.fatal || result.transcriptMutated || result.awaitsHydration {
		t.Fatalf("result = %+v, want deferred event without mutation", result)
	}
	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("transcript entry count = %d, want transient preserved", got)
	}
	if !m.transcriptEntries[1].Transient || m.transcriptEntries[1].Text != "pending mutable status" {
		t.Fatalf("trailing transient entry = %+v, want preserved mutable row", m.transcriptEntries[1])
	}
	if len(m.deferredCommittedTail) != 1 {
		t.Fatalf("deferred tail count = %d, want 1", len(m.deferredCommittedTail))
	}
}

func TestNativeCountOnlyCommittedAdvanceBypassesBusyDeferralAndQueuesLaterCommittedRows(t *testing.T) {
	client := &runtimeControlFakeClient{transcript: clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10816,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "missing assistant", Phase: string(clientui.MessagePhaseFinal)},
			{Role: "reviewer_status", Text: "missing reviewer row"},
			{Role: "system", Text: "after"},
		},
	}}
	m := newNativeSurfaceSpecTestModelWithClient(&bytes.Buffer{}, client)
	m.transcriptBaseOffset = 1568
	m.transcriptRevision = 10813
	m.transcriptTotalEntries = 1577
	m.transcriptEntries = make([]tui.TranscriptEntry, 9)
	for idx := range m.transcriptEntries {
		m.transcriptEntries[idx] = tui.TranscriptEntry{Committed: true, Role: tui.TranscriptRoleSystem, Text: "seed " + strconv.Itoa(idx)}
	}
	m.setRuntimeActivityBusyForTest(true)
	m.activity = uiActivityRunning
	if err := m.emitNativeCommittedEntries([]tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleSystem, Text: "already written"}}, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}

	countOnlyAdvance := clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		StepID:                     "4c4b3263-f50b-40a8-8eb1-1908c02da4a9",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         10815,
		CommittedEntryCount:        1579,
	}
	laterLocalEntry := clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		StepID:                     "4c4b3263-f50b-40a8-8eb1-1908c02da4a9",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         10816,
		CommittedEntryStart:        1579,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        1580,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "system",
			Text: "after",
		}},
	}

	result := m.runtimeAdapter().applyProjectedRuntimeEventsBatch([]clientui.Event{countOnlyAdvance, laterLocalEntry})
	if result.fatal || !result.awaitsHydration || result.transcriptMutated {
		t.Fatalf("batch result = %+v, want hydration barrier without mutation/fatal", result)
	}
	if len(m.pendingRuntimeEvents) != 1 || m.pendingRuntimeEvents[0].Kind != clientui.EventLocalEntryAdded {
		t.Fatalf("pending events = %#v, want later local entry queued", m.pendingRuntimeEvents)
	}
	msgs := collectCmdMessages(t, result.cmd)
	var refresh runtimeTranscriptRefreshedMsg
	found := false
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			refresh = typed
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected runtime transcript refresh message, got %#v", msgs)
	}
	if refresh.syncCause != runtimeTranscriptSyncCauseCommittedGap {
		t.Fatalf("sync cause = %s, want %s", refresh.syncCause, runtimeTranscriptSyncCauseCommittedGap)
	}
	if client.refreshTranscriptCalls != 1 {
		t.Fatalf("refresh transcript calls = %d, want 1", client.refreshTranscriptCalls)
	}
}

func TestNativeCountOnlyCommittedAdvanceUsesOwnedTailNotKnownTotal(t *testing.T) {
	client := &runtimeControlFakeClient{transcript: clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10968,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "missing committed row"},
			{Role: "system", Text: "after"},
		},
	}}
	m := newNativeSurfaceSpecTestModelWithClient(&bytes.Buffer{}, client)
	m.transcriptBaseOffset = 1568
	m.transcriptRevision = 10967
	m.transcriptTotalEntries = 1677
	m.transcriptEntries = make([]tui.TranscriptEntry, 107)
	for idx := range m.transcriptEntries {
		m.transcriptEntries[idx] = tui.TranscriptEntry{Committed: true, Role: tui.TranscriptRoleSystem, Text: "seed " + strconv.Itoa(idx)}
	}
	if err := m.emitNativeCommittedEntries([]tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleSystem, Text: "already written"}}, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}

	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		StepID:                     "4c4b3263-f50b-40a8-8eb1-1908c02da4a9",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         10967,
		CommittedEntryCount:        1676,
	})

	if result.fatal || !result.awaitsHydration || result.transcriptMutated {
		t.Fatalf("result = %+v, want hydration barrier without mutation/fatal", result)
	}
	if result.cmd == nil {
		t.Fatal("expected hydration command")
	}
	msgs := collectCmdMessages(t, result.cmd)
	var refresh runtimeTranscriptRefreshedMsg
	found := false
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			refresh = typed
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected runtime transcript refresh message, got %#v", msgs)
	}
	if refresh.syncCause != runtimeTranscriptSyncCauseCommittedGap {
		t.Fatalf("sync cause = %s, want %s", refresh.syncCause, runtimeTranscriptSyncCauseCommittedGap)
	}
	if client.refreshTranscriptCalls != 1 {
		t.Fatalf("refresh transcript calls = %d, want 1", client.refreshTranscriptCalls)
	}
}

func TestNativeRecoveryConversationUpdateKeepsContinuityRecoverySync(t *testing.T) {
	client := &runtimeControlFakeClient{transcript: clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10968,
		Entries: []clientui.ChatEntry{{
			Role: "assistant",
			Text: "recovered row",
		}},
	}}
	m := newNativeSurfaceSpecTestModelWithClient(&bytes.Buffer{}, client)
	m.transcriptBaseOffset = 1568
	m.transcriptRevision = 10967
	m.transcriptTotalEntries = 1577
	m.transcriptEntries = make([]tui.TranscriptEntry, 9)
	for idx := range m.transcriptEntries {
		m.transcriptEntries[idx] = tui.TranscriptEntry{Committed: true, Role: tui.TranscriptRoleSystem, Text: "seed " + strconv.Itoa(idx)}
	}
	if err := m.emitNativeCommittedEntries([]tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleSystem, Text: "already written"}}, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}

	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		StepID:                     "4c4b3263-f50b-40a8-8eb1-1908c02da4a9",
		CommittedTranscriptChanged: true,
		RecoveryCause:              clientui.TranscriptRecoveryCauseStreamGap,
		TranscriptRevision:         10968,
		CommittedEntryCount:        1578,
	})

	if result.fatal || !result.awaitsHydration || result.transcriptMutated {
		t.Fatalf("result = %+v, want recovery hydration barrier without mutation/fatal", result)
	}
	msgs := collectCmdMessages(t, result.cmd)
	var refresh runtimeTranscriptRefreshedMsg
	found := false
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			refresh = typed
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected runtime transcript refresh message, got %#v", msgs)
	}
	if refresh.syncCause != runtimeTranscriptSyncCauseContinuityRecovery {
		t.Fatalf("sync cause = %s, want %s", refresh.syncCause, runtimeTranscriptSyncCauseContinuityRecovery)
	}
	if refresh.recoveryCause != clientui.TranscriptRecoveryCauseStreamGap {
		t.Fatalf("recovery cause = %s, want %s", refresh.recoveryCause, clientui.TranscriptRecoveryCauseStreamGap)
	}
}

func TestNativeCommittedBacklogOlderThanActiveStreamFrontierRequestsStreamGapRecovery(t *testing.T) {
	const stepID = "6093a4a2-16d6-401a-9358-a5dd848f3ac7"
	client := &runtimeControlFakeClient{transcript: clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  18,
		Entries: []clientui.ChatEntry{{
			Role: "assistant",
			Text: "authoritative recovered tail",
		}},
	}}
	m := newNativeSurfaceSpecTestModelWithClient(&bytes.Buffer{}, client)
	m.transcriptBaseOffset = 1
	m.transcriptRevision = 12
	m.transcriptEntries = []tui.TranscriptEntry{
		{Committed: true, Role: tui.TranscriptRoleSystem, Text: "seed 1"},
		{Committed: true, Role: tui.TranscriptRoleSystem, Text: "seed 2"},
		{Committed: true, Role: tui.TranscriptRoleSystem, Text: "seed 3"},
	}
	m.transcriptTotalEntries = 6
	if err := m.emitNativeCommittedEntries([]tui.TranscriptEntry{{Committed: true, Role: tui.TranscriptRoleSystem, Text: "already written"}}, false); err != nil {
		t.Fatalf("emit native seed: %v", err)
	}
	m.refreshActiveAssistantStreamFromAuthoritativePageStreaming("newer same-step segment", &clientui.AssistantStreamMetadata{
		StepID:                  stepID,
		BaseRevision:            15,
		BaseCommittedEntryCount: 6,
	})

	result := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     stepID,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         13,
		CommittedEntryStart:        4,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        5,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "older committed segment",
			Phase: string(clientui.MessagePhaseCommentary),
		}},
	})

	if result.fatal || !result.awaitsHydration || result.transcriptMutated {
		t.Fatalf("result = %+v, want stream-gap hydration barrier without mutation/fatal", result)
	}
	msgs := collectCmdMessages(t, result.cmd)
	var refresh runtimeTranscriptRefreshedMsg
	found := false
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			refresh = typed
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected runtime transcript refresh message, got %#v", msgs)
	}
	if refresh.syncCause != runtimeTranscriptSyncCauseContinuityRecovery {
		t.Fatalf("sync cause = %s, want %s", refresh.syncCause, runtimeTranscriptSyncCauseContinuityRecovery)
	}
	if refresh.recoveryCause != clientui.TranscriptRecoveryCauseStreamGap {
		t.Fatalf("recovery cause = %s, want %s", refresh.recoveryCause, clientui.TranscriptRecoveryCauseStreamGap)
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
