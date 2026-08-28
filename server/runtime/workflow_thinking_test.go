package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session/sessiontest"
	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/textutil"
)

func TestPausedWorkflowThinkingDoesNotBlockOperatorThinkingAndMayApplyLater(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{
		Model:                   "workflow-thinking-model",
		ThinkingLevel:           "medium",
		SupportedThinkingValues: []string{"low", "medium"},
	})
	if err := engine.pauseRuntimeOperations(t.Context()); err != nil {
		t.Fatalf("pause Runtime FIFO: %v", err)
	}

	thinking, err := workflow.NewThinkingValue("max")
	if err != nil {
		t.Fatalf("NewThinkingValue: %v", err)
	}
	workflowDone := make(chan error, 1)
	go func() {
		workflowDone <- engine.SetWorkflowThinkingValue(thinking)
	}()
	waitForPendingRuntimeOperation(t, engine)

	if err := engine.SetThinkingLevel(t.Context(), "low"); err != nil {
		t.Fatalf("SetThinkingLevel while Workflow mutation is paused: %v", err)
	}
	if got := engine.ThinkingLevel(); got != "low" {
		t.Fatalf("operator Thinking = %q, want low", got)
	}

	if err := engine.drainRuntimeOperations(t.Context()); err != nil {
		t.Fatalf("drain Runtime FIFO: %v", err)
	}
	select {
	case err := <-workflowDone:
		if err != nil {
			t.Fatalf("SetWorkflowThinkingValue: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for Workflow Thinking mutation")
	}
	if got := engine.ThinkingLevel(); got != "max" {
		t.Fatalf("Thinking after later Workflow write = %q, want max", got)
	}
}

func TestOperatorThinkingAppliedAfterWorkflowMayReplaceIt(t *testing.T) {
	engine := mustNewExecTestEngine(t, mustCreateTestSession(t), &fakeClient{}, Config{
		Model:         "workflow-thinking-model",
		ThinkingLevel: "medium",
	})
	thinking, err := workflow.NewThinkingValue("max")
	if err != nil {
		t.Fatalf("NewThinkingValue: %v", err)
	}
	if err := engine.SetWorkflowThinkingValue(thinking); err != nil {
		t.Fatalf("SetWorkflowThinkingValue: %v", err)
	}
	if err := engine.SetThinkingLevel(t.Context(), "low"); err != nil {
		t.Fatalf("SetThinkingLevel: %v", err)
	}
	if got := engine.ThinkingLevel(); got != "low" {
		t.Fatalf("Thinking after later operator write = %q, want low", got)
	}
}

func TestOperatorThinkingPublicationFailureRemainsAppliedUntilLaterWorkflowWrite(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{
		Model:         "workflow-thinking-model",
		ThinkingLevel: "medium",
	})
	publicationErr := errors.New("setting publication failed")
	changed, err := engine.SetThinkingLevelWithPublication(t.Context(), "high", func(clientui.TranscriptSessionSettingFeedback) error {
		return publicationErr
	})
	if !errors.Is(err, publicationErr) || !changed || engine.ThinkingLevel() != "high" {
		t.Fatalf("operator Thinking = changed %t value %q error %v", changed, engine.ThinkingLevel(), err)
	}
	meta := store.Meta()
	if meta.ChatSettings == nil || meta.ChatSettings.Thinking == nil || *meta.ChatSettings.Thinking != "high" {
		t.Fatalf("durable operator Thinking = %+v, want high", meta.ChatSettings)
	}

	thinking, err := workflow.NewThinkingValue("low")
	if err != nil {
		t.Fatalf("NewThinkingValue: %v", err)
	}
	if err := engine.SetWorkflowThinkingValue(thinking); err != nil {
		t.Fatalf("SetWorkflowThinkingValue: %v", err)
	}
	if got := engine.ThinkingLevel(); got != "low" {
		t.Fatalf("Thinking after independent Workflow write = %q, want low", got)
	}
}

func TestDefinitelyUncommittedOperatorThinkingDoesNotPublishOrWriteLive(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{
		Model:         "workflow-thinking-model",
		ThinkingLevel: "medium",
	})
	blockTestSessionMetadataMutations(t, store)
	publications := 0
	changed, err := engine.SetThinkingLevelWithPublication(t.Context(), "high", func(clientui.TranscriptSessionSettingFeedback) error {
		publications++
		return nil
	})
	if err == nil || changed || publications != 0 || engine.ThinkingLevel() != "medium" {
		t.Fatalf("uncommitted operator Thinking = changed %t publications %d value %q error %v", changed, publications, engine.ThinkingLevel(), err)
	}
}

func TestWorkflowThinkingSetterAcceptsStandardMaxAndCustomValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"high", "max", "provider-custom"} {
		t.Run(value, func(t *testing.T) {
			store := mustCreateTestSession(t)
			engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{
				Model: "workflow-thinking-model",
			})
			thinking, err := workflow.NewThinkingValue(value)
			if err != nil {
				t.Fatalf("NewThinkingValue: %v", err)
			}
			if err := engine.SetWorkflowThinkingValue(thinking); err != nil {
				t.Fatalf("SetWorkflowThinkingValue: %v", err)
			}
			if got := engine.ThinkingLevel(); got != value {
				t.Fatalf("ThinkingLevel = %q, want %q", got, value)
			}
		})
	}
}

func TestWorkflowThinkingProviderRejectionPropagates(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &providerContractFailClient{}
	engine := mustNewExecTestEngine(t, store, client, Config{
		Model: "workflow-thinking-model",
	})
	thinking, err := workflow.NewThinkingValue("provider-custom")
	if err != nil {
		t.Fatalf("NewThinkingValue: %v", err)
	}
	if err := engine.SetWorkflowThinkingValue(thinking); err != nil {
		t.Fatalf("SetWorkflowThinkingValue: %v", err)
	}
	if _, err := engine.SubmitUserMessage(context.Background(), "run"); err == nil || !llm.IsNonRetriableModelError(err) {
		t.Fatalf("SubmitUserMessage error = %v, want non-retriable provider rejection", err)
	}
}

func TestWorkflowThinkingSetterPreservesCacheAndContractBoundaries(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSessionAt(t, t.TempDir(), sessiontest.NewPersistence().Options()...)
	engine := mustNewExecTestEngine(t, store, &fakeClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
		}},
	}, Config{Model: "workflow-thinking-model"})
	if _, err := engine.SubmitUserMessage(context.Background(), "seed"); err != nil {
		t.Fatalf("seed SubmitUserMessage: %v", err)
	}
	before := store.Meta()
	if before.Locked == nil {
		t.Fatal("seed did not establish locked contract")
	}
	thinking, err := workflow.NewThinkingValue("max")
	if err != nil {
		t.Fatalf("NewThinkingValue: %v", err)
	}
	if err := engine.SetWorkflowThinkingValue(thinking); err != nil {
		t.Fatalf("SetWorkflowThinkingValue: %v", err)
	}
	after := store.Meta()
	if after.Locked == nil || !reflect.DeepEqual(after.Locked, before.Locked) {
		t.Fatalf("locked contract changed after thinking mutation: before=%+v after=%+v", before.Locked, after.Locked)
	}
}

func TestWorkflowThinkingClearPreservesCacheAndContractBoundaries(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSessionAt(t, t.TempDir(), sessiontest.NewPersistence().Options()...)
	engine := mustNewExecTestEngine(t, store, &fakeClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
		}},
	}, Config{Model: "workflow-thinking-model", ThinkingLevel: "high"})
	if _, err := engine.SubmitUserMessage(context.Background(), "seed"); err != nil {
		t.Fatalf("seed SubmitUserMessage: %v", err)
	}
	before := store.Meta()
	if before.Locked == nil {
		t.Fatal("seed did not establish locked contract")
	}
	if err := engine.ClearWorkflowThinkingValue(); err != nil {
		t.Fatalf("ClearWorkflowThinkingValue: %v", err)
	}
	if got := engine.ThinkingLevel(); got != "" {
		t.Fatalf("ThinkingLevel = %q, want cleared", got)
	}
	after := store.Meta()
	if after.Locked == nil || !reflect.DeepEqual(after.Locked, before.Locked) {
		t.Fatalf("locked contract changed after thinking clear: before=%+v after=%+v", before.Locked, after.Locked)
	}
}
