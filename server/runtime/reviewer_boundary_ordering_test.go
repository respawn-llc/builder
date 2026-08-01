package runtime

import (
	"context"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
)

func TestReviewerContinuationDrainsManualCompactionBeforeFeedbackAndRecursiveDispatch(t *testing.T) {
	t.Parallel()

	reviewerStarted := make(chan struct{})
	releaseReviewer := make(chan struct{})
	mainClient := &fakeClient{responses: []llm.Response{
		{Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("initial")}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("compaction summary")}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("reviewed")}},
	}}
	reviewerClient := &hookClient{
		response: llm.Response{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":["tighten"]}`)},
		},
		beforeReturn: func() error {
			close(reviewerStarted)
			<-releaseReviewer
			return nil
		},
	}
	store := mustCreateTestSession(t)
	appendAgentStepBoundaryForEligibilityTest(t, store, "reviewer-outer-step")
	engine := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(), Config{
		Model:          "gpt-5",
		CompactionMode: "local",
		Reviewer: ReviewerConfig{
			Frequency: "all",
			Model:     "gpt-5",
			Client:    reviewerClient,
		},
	})

	submitDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "input")
		submitDone <- err
	}()
	select {
	case <-reviewerStarted:
	case <-time.After(15 * time.Second):
		select {
		case submitErr := <-submitDone:
			t.Fatalf("reviewer provider did not start: submit completed with %v, main calls=%d reviewer calls=%d", submitErr, len(mainClient.calls), len(reviewerClient.calls))
		default:
			t.Fatalf("reviewer provider did not start: main calls=%d reviewer calls=%d", len(mainClient.calls), len(reviewerClient.calls))
		}
	}

	compactDone := make(chan error, 1)
	go func() {
		compactDone <- engine.CompactContext(context.Background(), "preserve reviewer handoff")
	}()
	select {
	case err := <-compactDone:
		t.Fatalf("manual compaction completed before reviewer release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseReviewer)
	select {
	case err := <-compactDone:
		if err != nil {
			t.Fatalf("manual compaction: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("manual compaction did not complete at the outer boundary")
	}
	select {
	case err := <-submitDone:
		if err != nil {
			t.Fatalf("submit with reviewer continuation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reviewer continuation did not complete")
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(128)
	if err != nil {
		t.Fatalf("read reviewer ordering records: %v", err)
	}
	boundaries := make([]int, 0, 2)
	replacements := make([]int, 0, 1)
	feedback := make([]int, 0, 1)
	for index, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.AgentStepBoundaryRecord:
			boundaries = append(boundaries, index)
		case session.HistoryReplacementRecord:
			replacements = append(replacements, index)
		case session.MessageRecord:
			if payload.MessageType != nil && *payload.MessageType == session.MessageTypeReviewerFeedback {
				feedback = append(feedback, index)
			}
		}
	}
	if len(boundaries) != 3 || len(replacements) != 1 || len(feedback) != 1 {
		t.Fatalf("reviewer ordering facts = boundaries:%v replacements:%v feedback:%v", boundaries, replacements, feedback)
	}
	if boundaries[0] >= boundaries[1] ||
		boundaries[1] >= replacements[0] ||
		replacements[0] >= feedback[0] ||
		feedback[0] >= boundaries[2] {
		t.Fatalf("reviewer ordering = boundaries:%v replacement:%v feedback:%v", boundaries, replacements, feedback)
	}
}
