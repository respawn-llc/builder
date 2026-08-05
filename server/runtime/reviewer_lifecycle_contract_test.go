package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

func TestDefaultStepExecutorOwnsReviewerLifecycleAndPropagatesFatalError(t *testing.T) {
	fatalErr := errors.New("reviewer application failed")
	var engine *Engine
	reviewer := &reviewerPipelineStub{
		runFollowUp: func(stepID string, original llm.Message) (reviewerFollowUpResult, error) {
			active := engine.reviewerRuntimeState().ActiveStepSnapshot()
			if active == nil || active.StepID != stepID {
				t.Fatalf("Reviewer was not active during pipeline execution: %+v", active)
			}
			return reviewerFollowUpResult{Message: original}, fatalErr
		},
	}
	var events []Event
	engine = mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value("original"),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}}}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})
	engine.stepFlow = &defaultStepExecutor{
		engine:   engine,
		phase:    engine.phaseProtocol,
		reviewer: reviewer,
		messages: engine.messageFlow,
		tools:    engine.toolFlow,
	}

	_, err := engine.runStepLoopWithOptions(
		context.Background(),
		"step-1",
		"all",
		&fakeClient{},
		false,
	)
	if !errors.Is(err, fatalErr) {
		t.Fatalf("outer Agent Step error = %v, want %v", err, fatalErr)
	}
	if active := engine.reviewerRuntimeState().ActiveStepSnapshot(); active != nil {
		t.Fatalf("Reviewer remained active after fatal pipeline return: %+v", active)
	}
	started, completed := 0, 0
	startIndex, completedIndex := -1, -1
	for index, event := range events {
		switch event.Kind {
		case EventReviewerStarted:
			started++
			startIndex = index
		case EventReviewerCompleted:
			completed++
			completedIndex = index
		}
	}
	if started != 1 || completed != 1 {
		t.Fatalf("Reviewer lifecycle counts = started:%d completed:%d events=%+v", started, completed, events)
	}
	if completedIndex <= startIndex {
		t.Fatalf("Reviewer completion did not follow start: events=%+v", events)
	}
}

type reviewerPipelineStub struct {
	runFollowUp func(string, llm.Message) (reviewerFollowUpResult, error)
}

func (*reviewerPipelineStub) ShouldRunTurn(string, llm.Client, bool) bool {
	return true
}

func (s *reviewerPipelineStub) RunFollowUp(
	_ context.Context,
	stepID string,
	original llm.Message,
	_ int,
	_ bool,
	_ llm.Client,
) (reviewerFollowUpResult, error) {
	return s.runFollowUp(stepID, original)
}

func (*reviewerPipelineStub) RunSuggestions(context.Context, string, llm.Client) (reviewerSuggestionsResult, error) {
	return reviewerSuggestionsResult{}, nil
}
