package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestAssistantMessageAfterCacheWarningOwnsOnlyAssistantRange(t *testing.T) {
	t.Parallel()
	var events []Event
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:            "gpt-5",
			CacheWarningMode: config.CacheWarningModeVerbose,
			OnEvent:          func(event Event) { events = append(events, event) },
		},
	)
	restoreStep := setTestActiveStep(engine, "step")
	defer restoreStep()
	if err := engine.observePromptCacheResponse("step", preparedCacheRequestObservation{
		request: persistedCacheRequestObserved{
			DigestVersion: requestCacheDigestVersion,
			CacheKey:      "cache-key",
			Scope:         transcript.CacheWarningScopeConversation,
			ChunkCount:    1,
			TerminalHash:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		exactWarning: &transcript.CacheWarning{
			Scope:  transcript.CacheWarningScopeConversation,
			Reason: transcript.CacheWarningReasonNonPostfix,
		},
		previousCachedInputTokens: 10,
	}, llm.Usage{CachedInputTokens: textutil.Value(0)}); err != nil {
		t.Fatalf("observe cache warning: %v", err)
	}

	assistant := llm.Message{
		Role:    llm.RoleAssistant,
		Content: textutil.Value("commentary"),
		Phase:   textutil.Value(llm.MessagePhaseCommentary),
		ToolCalls: []llm.ToolCall{
			{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
			{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
		},
	}
	if err := engine.steer(
		"step",
		steerMessagesWithPersistenceIntent(steeringMessageEventNone,
			true,
			[]llm.Message{assistant},
		),
	); err != nil {
		t.Fatalf("persist assistant message: %v", err)
	}
	assistantEntries := TranscriptEntriesFromEvent(Event{Kind: EventAssistantMessage, Message: assistant})
	assistantStart := engine.CommittedTranscriptEntryCount() - len(assistantEntries)
	if err := engine.steer(
		"step",
		steerCommittedAssistantMessageIntent(assistant, &committedAssistantCoordinate{start: assistantStart}),
	); err != nil {
		t.Fatalf("publish assistant message: %v", err)
	}

	committed := make([]Event, 0, 2)
	for _, event := range events {
		if event.CommittedTranscriptChanged && len(TranscriptEntriesFromEvent(event)) > 0 {
			committed = append(committed, event)
		}
	}
	if len(committed) != 2 || committed[0].Kind != EventCacheWarning || committed[1].Kind != EventAssistantMessage {
		t.Fatalf("committed event sequence = %+v", committed)
	}
	cacheWarning, assistantEvent := committed[0], committed[1]
	cacheEntries := TranscriptEntriesFromEvent(cacheWarning)
	if !cacheWarning.CommittedEntryStartSet ||
		len(cacheEntries) != 1 ||
		assistantEvent.CommittedEntryStart != cacheWarning.CommittedEntryStart+len(cacheEntries) ||
		assistantEvent.CommittedEntryStart+len(assistantEntries) != assistantEvent.CommittedEntryCount {
		t.Fatalf(
			"cache/assistant committed ranges = cache:%+v assistant:%+v",
			cacheWarning,
			assistantEvent,
		)
	}
}

func TestFinalAnswerToolMaterializationPublishesToolCallBeforeLocalEntry(t *testing.T) {
	t.Parallel()
	var events []Event
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)
	restoreStep := setTestActiveStep(engine, "step")
	defer restoreStep()
	executor := defaultStepExecutor{
		engine: engine,
	}
	call := llm.ToolCall{
		ID:    "call-1",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{}`),
	}
	if _, _, err := executor.materializeFinalAnswerToolCalls(
		context.Background(),
		"step",
		acceptedResponseCalls{
			local: []llm.ToolCall{call},
			order: []acceptedResponseCallRef{{
				source: acceptedResponseCallLocal,
				index:  0,
			}},
		},
	); err != nil {
		t.Fatalf("materialize final-answer tool call: %v", err)
	}
	if err := engine.steer(
		"step",
		steerLocalEntryIntent(storedLocalEntry{
			Role: string(transcript.EntryRoleReasoning),
			Text: "reasoning",
		}),
	); err != nil {
		t.Fatalf("append local entry: %v", err)
	}

	committed := durableTranscriptProjectionEvents(events)
	if len(committed) < 3 ||
		committed[0].Kind != EventAssistantMessage ||
		committed[len(committed)-1].Kind != EventLocalEntryAdded {
		t.Fatalf("committed materialization sequence = %+v", committed)
	}
	toolRows := TranscriptEntriesFromEvent(committed[0])
	if len(toolRows) != 1 ||
		toolRows[0].Role != "tool_call" ||
		toolRows[0].ToolCallID != call.ID {
		t.Fatalf("materialized tool-call rows = %+v", toolRows)
	}
	assertDurableTranscriptProjectionRangesContiguous(t, committed)
}

func TestStepLoopPublishesCommentaryToolEnvelopeBeforeReasoningAndToolResults(t *testing.T) {
	t.Parallel()
	toolCalls := []llm.ToolCall{
		{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
		{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
		{ID: "call-3", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
	}
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("commentary"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: toolCalls,
			Reasoning: []llm.ReasoningEntry{{
				Role: textutil.Value(string(transcript.EntryRoleReasoning)),
				Text: "reasoning",
			}},
			Usage: llm.Usage{WindowTokens: 200_000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("final"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200_000},
		},
	}}
	var events []Event
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)
	restoreStep := setTestActiveStep(engine, "step")
	defer restoreStep()
	if _, err := engine.runStepLoopWithOptions(context.Background(), "step", "off", nil, false); err != nil {
		t.Fatalf("run step loop: %v", err)
	}

	committed := durableTranscriptProjectionEvents(events)
	if len(committed) < len(toolCalls)+3 || committed[0].Kind != EventAssistantMessage {
		t.Fatalf("committed step-loop events = %+v", committed)
	}
	envelope := TranscriptEntriesFromEvent(committed[0])
	if len(envelope) != len(toolCalls)+1 || envelope[0].Role != "assistant" {
		t.Fatalf("commentary tool envelope = %+v", envelope)
	}
	for index, call := range toolCalls {
		entry := envelope[index+1]
		if entry.Role != "tool_call" || entry.ToolCallID != call.ID {
			t.Fatalf("commentary tool envelope entry[%d] = %+v", index+1, entry)
		}
	}

	reasoningIndex, firstToolResultIndex := -1, -1
	for index, event := range committed {
		switch event.Kind {
		case EventLocalEntryAdded:
			if event.LocalEntry != nil && event.LocalEntry.Role == string(transcript.EntryRoleReasoning) {
				reasoningIndex = index
			}
		case EventToolCallCompleted:
			if firstToolResultIndex < 0 {
				firstToolResultIndex = index
			}
		}
	}
	if reasoningIndex != 1 || firstToolResultIndex <= reasoningIndex {
		t.Fatalf(
			"commentary/tool/reasoning ordering = reasoning_index:%d first_tool_result_index:%d events:%+v",
			reasoningIndex,
			firstToolResultIndex,
			committed,
		)
	}
	assertDurableTranscriptProjectionRangesContiguous(t, committed)
}

func TestStepLoopPersistsReasoningAsDetailLocalEntry(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("final"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
		Reasoning: []llm.ReasoningEntry{{
			Role: textutil.Value(string(transcript.EntryRoleReasoning)),
			Text: "reasoning progress",
		}},
		Usage: llm.Usage{WindowTokens: 200_000},
	}}}
	var events []Event
	engine := mustNewTestEngine(
		t,
		store,
		client,
		tools.NewRegistry(),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)
	restoreStep := setTestActiveStep(engine, "step")
	defer restoreStep()

	if _, err := engine.runStepLoopWithOptions(context.Background(), "step", "off", nil, false); err != nil {
		t.Fatalf("run step loop: %v", err)
	}

	var localEvents []Event
	for index := range events {
		event := events[index]
		if event.Kind == EventLocalEntryAdded {
			localEvents = append(localEvents, event)
		}
	}
	if len(localEvents) != 1 {
		t.Fatalf("reasoning local-entry events = %+v", localEvents)
	}
	localEvent := localEvents[0]
	if !localEvent.CommittedTranscriptChanged ||
		!localEvent.CommittedEntryStartSet ||
		localEvent.LocalEntry == nil ||
		localEvent.LocalEntry.Visibility != transcript.EntryVisibilityDetail {
		t.Fatalf("reasoning local-entry event = %+v", localEvent)
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(8)
	if err != nil {
		t.Fatalf("read bounded reasoning records: %v", err)
	}
	var localRecords []session.LocalEntryRecord
	for _, record := range window.Records {
		entry, ok := mustSessionEventPayload(record).(session.LocalEntryRecord)
		if ok {
			localRecords = append(localRecords, entry)
		}
	}
	if len(localRecords) != 1 || localRecords[0].Visibility != session.EntryVisibilityDetail {
		t.Fatalf("persisted reasoning local entries = %+v", localRecords)
	}
}

func TestDeveloperContextTranscriptRowsRespectVisibilityMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		messageType llm.MessageType
		visibility  transcript.EntryVisibility
	}{
		{llm.MessageTypeSubagents, transcript.EntryVisibilityDetail},
		{llm.MessageTypeWorkflowMode, transcript.EntryVisibilityOngoingCollapsed},
		{llm.MessageTypeWorktreeMode, transcript.EntryVisibilityOngoing},
		{llm.MessageTypeWorktreeModeExit, transcript.EntryVisibilityOngoing},
		{llm.MessageTypeGoal, transcript.EntryVisibilityOngoing},
		{llm.MessageTypeActiveGoalContinuation, transcript.EntryVisibilityDetail},
		{llm.MessageTypeBackgroundNotice, transcript.EntryVisibilityOngoingCollapsed},
	}

	for _, test := range tests {
		facts := TranscriptCommittedRowFactsFromEvent(Event{
			Kind: EventConversationUpdated,
			Message: llm.Message{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(test.messageType),
				Content:     textutil.Value("context"),
			},
		})
		if len(facts) != 1 ||
			facts[0].Kind != TranscriptCommittedRowFactNotice ||
			facts[0].Notice == nil ||
			facts[0].Notice.MessageType != test.messageType ||
			facts[0].Visibility != test.visibility {
			t.Fatalf("developer-context transcript rows = %+v", facts)
		}
	}
}

func TestAssistantTranscriptRowsRespectPhaseVisibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		phase      llm.MessagePhase
		visibility transcript.EntryVisibility
	}{
		{llm.MessagePhaseCommentary, transcript.EntryVisibilityDetail},
		{llm.MessagePhaseFinal, transcript.EntryVisibilityOngoing},
	}

	for _, test := range tests {
		facts := TranscriptCommittedRowFactsFromEvent(Event{
			Kind: EventAssistantMessage,
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(test.phase),
				Content: textutil.Value("response"),
			},
		})
		if len(facts) != 1 ||
			facts[0].Kind != TranscriptCommittedRowFactAssistant ||
			facts[0].Assistant == nil ||
			facts[0].Assistant.Phase != test.phase ||
			facts[0].Visibility != test.visibility {
			t.Fatalf("assistant transcript rows = %+v", facts)
		}
	}
}

func TestBackgroundNoticeTranscriptRowPreservesExitCode(t *testing.T) {
	t.Parallel()
	exitCode := 2
	facts := TranscriptCommittedRowFactsFromEvent(Event{
		Kind: EventConversationUpdated,
		Message: llm.Message{
			Role:               llm.RoleDeveloper,
			MessageType:        textutil.Value(llm.MessageTypeBackgroundNotice),
			Content:            textutil.Value("background"),
			BackgroundExitCode: &exitCode,
		},
	})
	if len(facts) != 1 ||
		facts[0].Kind != TranscriptCommittedRowFactNotice ||
		facts[0].Notice == nil ||
		facts[0].Notice.MessageType != llm.MessageTypeBackgroundNotice ||
		facts[0].Notice.BackgroundExitCode == nil ||
		*facts[0].Notice.BackgroundExitCode != exitCode {
		t.Fatalf("background-notice transcript rows = %+v", facts)
	}
}

func TestTranscriptHydrationRetainsAdjacentRowsAroundProviderEmptyAssistant(t *testing.T) {
	t.Parallel()
	const (
		beforeStepID = "11111111-1111-4111-8111-111111111111"
		emptyStepID  = "22222222-2222-4222-8222-222222222222"
		afterStepID  = "33333333-3333-4333-8333-333333333333"
	)
	store := mustCreateTestSession(t)
	for _, testCase := range []struct {
		stepID  string
		message llm.Message
	}{
		{
			stepID:  beforeStepID,
			message: llm.Message{Role: llm.RoleUser, Content: textutil.Value("before")},
		},
		{
			stepID:  emptyStepID,
			message: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal)},
		},
		{
			stepID:  afterStepID,
			message: llm.Message{Role: llm.RoleUser, Content: textutil.Value("after")},
		},
	} {
		if _, _, err := appendTestEvent(t, store, testCase.stepID, testCase.message); err != nil {
			t.Fatalf("append persisted message for step %q: %v", testCase.stepID, err)
		}
	}

	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	restoreStep := setTestActiveStep(engine, "compaction")
	defer restoreStep()
	var hydration TranscriptHydrationSnapshot
	if err := engine.WithTranscriptHydrationSnapshot(func(snapshot TranscriptHydrationSnapshot) error {
		hydration = snapshot
		return nil
	}); err != nil {
		t.Fatalf("hydrate restored transcript: %v", err)
	}
	if len(hydration.CommittedRows) != 2 {
		t.Fatalf("hydrated rows = %+v", hydration.CommittedRows)
	}
	for index, stepID := range []string{beforeStepID, afterStepID} {
		row := hydration.CommittedRows[index]
		if row.StepID == nil || *row.StepID != stepID || row.Kind != TranscriptCommittedRowFactUser || row.User == nil {
			t.Fatalf("hydrated row[%d] = %+v", index, row)
		}
	}
}

func TestReopenedCompactionPublishesVisibleTranscriptCoordinates(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	for _, role := range []string{
		string(transcript.EntryRoleSystem),
		string(transcript.EntryRoleSystem),
	} {
		if err := engine.AppendCommittedEntry(role, "notice"); err != nil {
			t.Fatalf("append pre-compaction entry: %v", err)
		}
	}
	if err := steerTestActiveStep(engine,
		"compaction",
		steerHistoryReplacementIntent(
			"local",
			compactionModeAuto,
			1,
			"",
			nil,
			llm.ItemsFromMessages([]llm.Message{{
				Role:        llm.RoleUser,
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content:     textutil.Value("summary"),
			}}),
		),
	); err != nil {
		t.Fatalf("persist history replacement: %v", err)
	}
	if got := engine.CommittedTranscriptEntryCount(); got != 3 {
		t.Fatalf("pre-reopen committed entry count = %d", got)
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("close compacted engine: %v", err)
	}

	var events []Event
	reopened := mustNewTestEngine(
		t,
		mustOpenTestSession(t, store.Dir()),
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened engine: %v", err)
		}
	})
	if got := reopened.CommittedTranscriptEntryCount(); got != 3 {
		t.Fatalf("reopened committed entry count = %d", got)
	}
	if err := reopened.AppendCommittedEntry(string(transcript.EntryRoleSystem), "notice"); err != nil {
		t.Fatalf("append post-reopen entry: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("post-reopen events = %+v", events)
	}
	event := events[0]
	if event.Kind != EventLocalEntryAdded ||
		!event.CommittedEntryStartSet ||
		event.CommittedEntryStart != 3 ||
		event.CommittedEntryCount != 4 {
		t.Fatalf("post-reopen committed range = %+v", event)
	}
}

func TestHistoryReplacementPublishesPreservedUserMessageBeforeFollowingLocalEntry(t *testing.T) {
	t.Parallel()
	var events []Event
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)
	restoreStep := setTestActiveStep(engine, "compaction")
	defer restoreStep()
	carryover, ok := compactionPreservedUserMessage("carryover")
	if !ok {
		t.Fatal("expected typed compaction-preserved user message")
	}
	if err := engine.steer(
		"compaction",
		steerHistoryReplacementIntent(
			"local",
			compactionModeManual,
			1,
			"",
			nil,
			llm.ItemsFromMessages([]llm.Message{
				{
					Role:        llm.RoleUser,
					MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
					Content:     textutil.Value("summary"),
				},
				carryover,
			}),
		),
	); err != nil {
		t.Fatalf("persist manual history replacement: %v", err)
	}
	if err := engine.steer(
		"compaction",
		steerLocalEntryIntent(storedLocalEntry{
			Role: string(transcript.EntryRoleSystem),
			Text: "notice",
		}),
	); err != nil {
		t.Fatalf("append following local entry: %v", err)
	}

	committed := durableTranscriptProjectionEvents(events)
	if len(committed) != 3 {
		t.Fatalf("manual-compaction committed events = %+v", committed)
	}
	if !committed[0].LocalEntryProjected ||
		!committed[1].LocalEntryProjected ||
		committed[2].LocalEntryProjected {
		t.Fatalf("manual-compaction projection provenance = %+v", committed)
	}
	wantTypes := []llm.MessageType{
		llm.MessageTypeCompactionSummary,
		llm.MessageTypeCompactionPreservedUserMessage,
	}
	for index, wantType := range wantTypes {
		facts := TranscriptCommittedRowFactsFromEvent(committed[index])
		if len(facts) != 1 ||
			facts[0].Notice == nil ||
			facts[0].Notice.MessageType != wantType {
			t.Fatalf("manual-compaction projected row[%d] = %+v", index, facts)
		}
	}
	assertDurableTranscriptProjectionRangesContiguous(t, committed)
}

func durableTranscriptProjectionEvents(events []Event) []Event {
	committed := make([]Event, 0, len(events))
	for _, event := range events {
		switch event.Kind {
		case EventAssistantMessage, EventToolCallCompleted, EventLocalEntryAdded:
		default:
			continue
		}
		if event.CommittedTranscriptChanged && len(TranscriptEntriesFromEvent(event)) > 0 {
			committed = append(committed, event)
		}
	}
	return committed
}

func assertDurableTranscriptProjectionRangesContiguous(t *testing.T, events []Event) {
	t.Helper()
	for index, event := range events {
		if !event.CommittedEntryStartSet ||
			event.CommittedEntryCount < event.CommittedEntryStart {
			t.Fatalf("durable transcript event[%d] has invalid range: %+v", index, event)
		}
		if index > 0 && event.CommittedEntryStart != events[index-1].CommittedEntryCount {
			t.Fatalf(
				"durable transcript range discontinuity at %d: previous=%+v current=%+v",
				index,
				events[index-1],
				event,
			)
		}
	}
}
