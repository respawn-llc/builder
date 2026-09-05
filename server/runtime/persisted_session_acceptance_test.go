package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"core/internal/testharness/runtimewirefixture"
	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	edittool "core/server/tools/edit"
	patchtool "core/server/tools/patch"
	readimagetool "core/server/tools/readimage"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type persistedEffectFailure uint8

const (
	persistedEffectObserverFailure persistedEffectFailure = iota + 1
	persistedEffectProjectionFailure
)

type persistedEffectFixture struct {
	call       llm.ToolCall
	handler    tools.Handler
	path       string
	contents   []byte
	outputKind session.ToolOutputKind
}

type persistedBoundaryClient struct {
	mu       sync.Mutex
	eventLog session.MaterializedEventLog
	calls    int
	callIDs  []string
	err      error
}

func (*persistedBoundaryClient) ProviderCapabilities(
	context.Context,
) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:                     "openai",
		SupportsResponsesAPI:           true,
		SupportsResponsesCompact:       true,
		SupportsRequestInputTokenCount: true,
		SupportsPromptCacheKey:         true,
		SupportsReasoningEncrypted:     true,
		SupportsServerSideContextEdit:  true,
	}, nil
}

func (c *persistedBoundaryClient) Generate(
	_ context.Context,
	request llm.Request,
	_ llm.StreamCallbacks,
) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 {
		toolCalls := make([]llm.ToolCall, len(c.callIDs))
		for index, callID := range c.callIDs {
			toolCalls[index] = llm.ToolCall{
				ID:    callID,
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"cmd":"true"}`),
			}
		}
		return commentaryResponse("working", toolCalls...), nil
	}
	if c.calls == 2 {
		for _, callID := range c.callIDs {
			if !repairRequestHasToolOutput(request.Items, callID) {
				c.err = errors.Join(
					c.err,
					errors.New("next provider request omitted a committed tool output"),
				)
			}
		}
		window, err := c.eventLog.ReadRecentRecords(32)
		if err != nil {
			c.err = errors.Join(c.err, err)
		} else {
			var completionIDs []string
			for _, record := range window.Records {
				completion, ok := mustSessionEventPayload(record).(session.ToolCompletionRecord)
				if ok {
					completionIDs = append(completionIDs, completion.CallID)
				}
			}
			if len(completionIDs) < len(c.callIDs) {
				c.err = errors.Join(
					c.err,
					errors.New("next provider request started before the complete Result Group was durable"),
				)
			} else {
				tail := completionIDs[len(completionIDs)-len(c.callIDs):]
				for index := range c.callIDs {
					if tail[index] != c.callIDs[index] {
						c.err = errors.Join(
							c.err,
							errors.New("durable Result Group order changed before the next provider request"),
						)
						break
					}
				}
			}
		}
		return finalTextResponse("done"), nil
	}
	return llm.Response{}, errors.New("unexpected provider request after terminal response")
}

func TestPersistedSessionPatchObserverFailureRecoversWithoutEffectReplay(t *testing.T) {
	runPersistedEffectRecoveryCase(
		t,
		toolspec.ToolPatch,
		persistedEffectObserverFailure,
	)
}

func TestPersistedSessionPatchProjectionFailureRecoversWithoutEffectReplay(t *testing.T) {
	runPersistedEffectRecoveryCase(
		t,
		toolspec.ToolPatch,
		persistedEffectProjectionFailure,
	)
}

func TestPersistedSessionEditObserverFailureRecoversWithoutEffectReplay(t *testing.T) {
	runPersistedEffectRecoveryCase(
		t,
		toolspec.ToolEdit,
		persistedEffectObserverFailure,
	)
}

func TestPersistedSessionEditProjectionFailureRecoversWithoutEffectReplay(t *testing.T) {
	runPersistedEffectRecoveryCase(
		t,
		toolspec.ToolEdit,
		persistedEffectProjectionFailure,
	)
}

func TestPersistedSessionViewImageObserverFailureRecoversWithoutEffectReplay(t *testing.T) {
	runPersistedEffectRecoveryCase(
		t,
		toolspec.ToolViewImage,
		persistedEffectObserverFailure,
	)
}

func TestPersistedSessionViewImageProjectionFailureRecoversWithoutEffectReplay(t *testing.T) {
	runPersistedEffectRecoveryCase(
		t,
		toolspec.ToolViewImage,
		persistedEffectProjectionFailure,
	)
}

func TestPersistedSessionCrashWithBlockedPrefixRepairsWholeUncommittedGroup(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	customInput := "later custom input"
	calls := []llm.ToolCall{
		{
			ID:    "crash-earlier",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"true"}`),
		},
		{
			ID:          "crash-later",
			Name:        string(toolspec.ToolPatch),
			Input:       json.RawMessage(`{}`),
			Custom:      true,
			CustomInput: &customInput,
		},
	}
	accepted := acceptedResponseCalls{
		local: calls,
		order: []acceptedResponseCallRef{
			{source: acceptedResponseCallLocal, index: 0},
			{source: acceptedResponseCallLocal, index: 1},
		},
	}
	const stepID = "crash-unclosed-step"
	restoreStep := setTestActiveStep(engine, stepID)
	defer restoreStep()
	persistAcceptedToolCallIntents(t, engine, stepID, accepted)
	collector, err := newResultGroupCollector([]resultGroupCallIdentity{
		resultGroupIdentityFromToolCall(calls[0]),
		resultGroupIdentityFromToolCall(calls[1]),
	})
	if err != nil {
		t.Fatalf("new crash collector: %v", err)
	}
	laterResult := tools.Result{
		CallID: calls[1].ID,
		Name:   toolspec.ToolPatch,
		Output: json.RawMessage(`{"ok":"completed only in memory"}`),
	}
	var outcome *resultGroupReportOutcome
	if err := engine.steer(runtimeTestStepID(stepID), steerResultGroupReportIntent(
		collector,
		calls[1].ID,
		resultGroupUnit{result: laterResult},
		&outcome,
	),
	); err != nil || outcome == nil || *outcome != resultGroupReportAccepted {
		t.Fatalf("report later crash result = outcome:%v error:%v", outcome, err)
	}
	if collector.cursor != 0 || len(collector.readyPrefix()) != 0 {
		t.Fatalf(
			"blocked crash collector = cursor:%d ready:%d, want no committable prefix",
			collector.cursor,
			len(collector.readyPrefix()),
		)
	}
	if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot(calls[1].ID); found {
		t.Fatal("later in-memory result projected before the blocked prefix")
	}
	if _, _, found := completionRecordCount(t, store, calls[1].ID); found {
		t.Fatal("later in-memory result became durable before graceful close")
	}

	firstStore := mustOpenTestSession(t, store.Dir())
	first := mustNewTestEngine(
		t,
		firstStore,
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	assertFreshResourceRepairOnEngine(t, first, firstStore, calls[0].ID)
	assertFreshResourceRepairOnEngine(t, first, firstStore, calls[1].ID)
	var hydrated []string
	for _, row := range mustTranscriptHydrationSnapshot(t, first).CommittedRows {
		if row.Kind == TranscriptCommittedRowFactTool && row.Tool != nil {
			if row.Tool.ToolCallID == calls[0].ID || row.Tool.ToolCallID == calls[1].ID {
				hydrated = append(hydrated, row.Tool.ToolCallID)
			}
		}
	}
	if len(hydrated) != 2 ||
		hydrated[0] != calls[0].ID ||
		hydrated[1] != calls[1].ID {
		t.Fatalf("crash recovery tool order = %v, want %v", hydrated, []string{calls[0].ID, calls[1].ID})
	}
	for index, call := range calls {
		_, outputKind, completion := repairCompletionTypedFacts(t, firstStore, call.ID)
		wantKind := session.ToolOutputKindFunction
		if index == 1 {
			wantKind = session.ToolOutputKindCustom
		}
		if outputKind != wantKind {
			t.Fatalf("crash repair output kind for %q = %q, want %q", call.ID, outputKind, wantKind)
		}
		if !completion.IsError ||
			!bytes.Equal(completion.Output, missingToolOutputUnavailableOutput) {
			t.Fatalf(
				"crash repair for %q = error:%t output:%s",
				call.ID,
				completion.IsError,
				completion.Output,
			)
		}
	}

	secondStore := mustOpenTestSession(t, store.Dir())
	second := mustNewTestEngine(
		t,
		secondStore,
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	for _, call := range calls {
		assertFreshResourceRepairOnEngine(t, second, secondStore, call.ID)
		completions, warnings, found := completionRecordCount(t, secondStore, call.ID)
		if !found || completions != 1 || warnings != 1 {
			t.Fatalf(
				"second crash recovery for %q = completions:%d warnings:%d found:%t",
				call.ID,
				completions,
				warnings,
				found,
			)
		}
	}
}

func TestPersistedSessionGroupCommitPrecedesNextProviderAndStepCompletion(t *testing.T) {
	store := mustCreateTestSession(t)
	callIDs := []string{"boundary-first", "boundary-second"}
	client := &persistedBoundaryClient{
		eventLog: mustMaterializeTestEventLog(t, store),
		callIDs:  callIDs,
	}
	var (
		eventsMu sync.Mutex
		events   []Event
	)
	engine := mustNewTestEngine(
		t,
		store,
		client,
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{
			Model: "gpt-5",
			OnEvent: func(event Event) {
				eventsMu.Lock()
				events = append(events, event)
				eventsMu.Unlock()
			},
		},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "run boundary group"); err != nil {
		t.Fatalf("submit boundary group: %v", err)
	}
	client.mu.Lock()
	providerCalls := client.calls
	providerErr := client.err
	client.mu.Unlock()
	if providerErr != nil {
		t.Fatal(providerErr)
	}
	if providerCalls != 2 {
		t.Fatalf("provider calls = %d, want tool response then one continuation", providerCalls)
	}

	eventsMu.Lock()
	eventSnapshot := append([]Event(nil), events...)
	eventsMu.Unlock()
	completed := make(map[string]int, len(callIDs))
	finishedRunIndex := -1
	for index, event := range eventSnapshot {
		if event.Kind == EventToolCallCompleted &&
			event.ToolResult != nil {
			for _, callID := range callIDs {
				if event.ToolResult.CallID == callID {
					completed[callID] = index
				}
			}
		}
		if event.Kind == EventRunStateChanged &&
			event.RunState != nil &&
			event.RunState.Lifecycle.Phase == RunLifecycleFinished {
			finishedRunIndex = index
		}
	}
	if finishedRunIndex < 0 {
		t.Fatal("Step completion did not publish a finished Run State")
	}
	for _, callID := range callIDs {
		index, found := completed[callID]
		if !found {
			t.Fatalf("missing committed completion event for %q", callID)
		}
		if index >= finishedRunIndex {
			t.Fatalf(
				"completion event for %q at %d followed Step completion at %d",
				callID,
				index,
				finishedRunIndex,
			)
		}
	}
}

func runPersistedEffectRecoveryCase(
	t *testing.T,
	toolID toolspec.ID,
	failure persistedEffectFailure,
) {
	t.Helper()
	var (
		store            *session.Store
		gate             *sessiontest.PersistenceGate
		callbackObserver *callbackPersistenceObserver
	)
	switch failure {
	case persistedEffectObserverFailure:
		gate = sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
		store = mustCreateTestSessionAt(
			t,
			t.TempDir(),
			session.WithPersistenceObserver(gate),
		)
	case persistedEffectProjectionFailure:
		callbackObserver = newCallbackPersistenceObserver(runtimeTestSessionPersistence)
		store = mustCreateTestSessionAt(
			t,
			t.TempDir(),
			session.WithPersistenceObserver(callbackObserver),
		)
	default:
		t.Fatalf("unsupported persisted effect failure %d", failure)
	}

	workspace := t.TempDir()
	outside := testsetup.NonTemporaryDirectory(
		t,
		"kent-persisted-effect-",
		tools.IsPathInTemporaryDir,
	)
	broker := tools.NewAskQuestionBroker()
	brokerCalls := 0
	broker.SetAskHandler(func(
		context.Context,
		tools.AskQuestionRequest,
	) (tools.AskQuestionResolution, error) {
		brokerCalls++
		return tools.AskQuestionApproval{
			Decision: tools.AskQuestionApprovalDecisionAllowOnce,
		}, nil
	})
	fixture := newPersistedEffectFixture(t, workspace, outside, toolID, broker)
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolID,
			Handler: fixture.handler,
		}),
		Config{Model: "gpt-5"},
	)
	stepID := runtimeTestStepID("effect-step")
	restoreStep := setTestActiveStep(engine, stepID)
	defer restoreStep()
	calls := questionBarrierAcceptedCalls()
	calls.local[0] = fixture.call
	persistAcceptedToolCallIntents(t, engine, stepID, calls)
	cause := errors.New("persisted effect barrier failure")
	switch failure {
	case persistedEffectObserverFailure:
		gate.FailNext(cause)
	case persistedEffectProjectionFailure:
		callbackObserver.Arm(func() {
			engine.transcriptRuntimeState().CompleteLiveTool("hosted")
		})
	}

	results, err := engine.executeAcceptedToolCalls(
		context.Background(),
		stepID,
		calls,
	)
	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) || !fatal.Committed {
		t.Fatalf("%s effect error = %v, want committed collector fatal", toolID, err)
	}
	if failure == persistedEffectObserverFailure && !errors.Is(fatal.Cause, cause) {
		t.Fatalf("%s observer cause = %v, want %v", toolID, fatal.Cause, cause)
	}
	if len(results) != 0 {
		t.Fatalf("%s fatal results = %+v, want none", toolID, results)
	}
	if brokerCalls != 0 {
		t.Fatalf("%s approval handler calls = %d, want none", toolID, brokerCalls)
	}
	assertPersistedEffectContents(t, fixture)
	if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot(fixture.call.ID); found {
		t.Fatalf("%s blocked effect projected a semantic completion", toolID)
	}

	assertFreshResourceRepairExactlyOnceWith(
		t,
		store,
		fixture.call.ID,
		func(restored *Engine, restoredStore *session.Store) {
			assertHydratedToolRowsExactlyOnce(t, restored, "hosted")
			_, outputKind, completion := repairCompletionTypedFacts(
				t,
				restoredStore,
				fixture.call.ID,
			)
			if outputKind != fixture.outputKind {
				t.Fatalf(
					"%s recovered output kind = %q, want %q",
					toolID,
					outputKind,
					fixture.outputKind,
				)
			}
			if !completion.IsError ||
				!bytes.Equal(completion.Output, missingToolOutputUnavailableOutput) {
				t.Fatalf(
					"%s recovered neutral completion = error:%t output:%s",
					toolID,
					completion.IsError,
					completion.Output,
				)
			}
		},
	)
	assertPersistedEffectContents(t, fixture)
}

func newPersistedEffectFixture(
	t *testing.T,
	workspace string,
	outside string,
	toolID toolspec.ID,
	broker *tools.AskQuestionBroker,
) persistedEffectFixture {
	t.Helper()
	approver := func(
		ctx context.Context,
		_ tools.FileAccessApprovalRequest,
	) (tools.FileAccessApproval, error) {
		identity, err := tools.ExecutionIdentityFromContext(ctx)
		if err != nil {
			return tools.FileAccessApproval{Kind: tools.FileAccessApprovalDeny}, err
		}
		response, err := broker.Ask(ctx, tools.AskQuestionRequest{
			Question:   "Approve outside-workspace access?",
			Approval:   true,
			RunID:      identity.RunID,
			StepID:     identity.StepID,
			ToolCallID: string(identity.ToolCallID),
			ApprovalOptions: []tools.AskQuestionApprovalOption{{
				Decision: tools.AskQuestionApprovalDecisionAllowOnce,
				Label:    "Allow once",
			}},
		})
		if err != nil {
			return tools.FileAccessApproval{Kind: tools.FileAccessApprovalDeny}, err
		}
		approval, ok := response.(tools.AskQuestionApproval)
		if !ok || approval.Decision != tools.AskQuestionApprovalDecisionAllowOnce {
			return tools.FileAccessApproval{Kind: tools.FileAccessApprovalDeny}, nil
		}
		return tools.FileAccessApproval{Kind: tools.FileAccessApprovalAllowOnce}, nil
	}
	filesystemContext := runtimewirefixture.FilesystemContext(t, workspace)
	switch toolID {
	case toolspec.ToolPatch:
		path := filepath.Join(outside, "patch.txt")
		contents := []byte("before\n")
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			t.Fatalf("write patch fixture: %v", err)
		}
		handler, err := patchtool.New(
			filesystemContext,
			patchtool.WithOutsideWorkspaceApprover(approver),
		)
		if err != nil {
			t.Fatalf("new patch tool: %v", err)
		}
		patch := "*** Begin Patch\n*** Update File: " + path + "\n-before\n+after\n*** End Patch\n"
		return persistedEffectFixture{
			call: llm.ToolCall{
				ID:          "blocked-patch",
				Name:        string(toolID),
				Input:       json.RawMessage(`{}`),
				Custom:      true,
				CustomInput: textutil.Value(patch),
			},
			handler:    handler,
			path:       path,
			contents:   contents,
			outputKind: session.ToolOutputKindCustom,
		}
	case toolspec.ToolEdit:
		path := filepath.Join(outside, "edit.txt")
		contents := []byte("before\n")
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			t.Fatalf("write edit fixture: %v", err)
		}
		handler, err := edittool.New(
			filesystemContext,
			edittool.WithOutsideWorkspaceApprover(approver),
		)
		if err != nil {
			t.Fatalf("new edit tool: %v", err)
		}
		input, err := json.Marshal(map[string]any{
			"path":       path,
			"old_string": "before",
			"new_string": "after",
		})
		if err != nil {
			t.Fatalf("marshal edit input: %v", err)
		}
		return persistedEffectFixture{
			call: llm.ToolCall{
				ID:    "blocked-edit",
				Name:  string(toolID),
				Input: input,
			},
			handler:    handler,
			path:       path,
			contents:   contents,
			outputKind: session.ToolOutputKindFunction,
		}
	case toolspec.ToolViewImage:
		path := filepath.Join(outside, "image.png")
		contents := []byte("not-read-after-barrier")
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			t.Fatalf("write view-image fixture: %v", err)
		}
		handler, err := readimagetool.New(
			filesystemContext,
			true,
			readimagetool.WithOutsideWorkspaceApprover(approver),
		)
		if err != nil {
			t.Fatalf("new view-image tool: %v", err)
		}
		input, err := json.Marshal(map[string]any{"path": path})
		if err != nil {
			t.Fatalf("marshal view-image input: %v", err)
		}
		return persistedEffectFixture{
			call: llm.ToolCall{
				ID:    "blocked-view-image",
				Name:  string(toolID),
				Input: input,
			},
			handler:    handler,
			path:       path,
			contents:   contents,
			outputKind: session.ToolOutputKindFunction,
		}
	default:
		t.Fatalf("unsupported persisted effect tool %q", toolID)
		return persistedEffectFixture{}
	}
}

func assertPersistedEffectContents(
	t *testing.T,
	fixture persistedEffectFixture,
) {
	t.Helper()
	contents, err := os.ReadFile(fixture.path)
	if err != nil {
		t.Fatalf("read blocked effect fixture: %v", err)
	}
	if !bytes.Equal(contents, fixture.contents) {
		t.Fatalf(
			"blocked effect changed %s to %q, want %q",
			fixture.path,
			contents,
			fixture.contents,
		)
	}
}
