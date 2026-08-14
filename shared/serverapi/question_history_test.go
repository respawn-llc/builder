package serverapi

import "testing"

func TestQuestionHistorySubscribeRequestValidation(t *testing.T) {
	valid := QuestionHistorySubscribeRequest{
		SessionID:   "12345678-1234-4234-8234-123456789012",
		MaxHandoffs: 25,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	invalid := valid
	invalid.MaxHandoffs = 0
	if err := invalid.Validate(); err == nil {
		t.Fatal("nonpositive max_handoffs accepted")
	}
}

func TestQuestionHistoryEventValidation(t *testing.T) {
	started := false
	omitted := true
	events := []QuestionHistoryEvent{
		{Kind: QuestionHistoryEventStarted, LargeHistory: &started},
		{
			Kind: QuestionHistoryEventQuestion,
			Question: &QuestionHistoryQuestion{
				Question: "question",
				Answer:   "answer",
			},
		},
		{Kind: QuestionHistoryEventCompleted, HistoryOmitted: &omitted},
	}
	for _, event := range events {
		if err := event.Validate(); err != nil {
			t.Fatalf("valid event %#v: %v", event, err)
		}
	}
	if err := (QuestionHistoryEvent{
		Kind: QuestionHistoryEventQuestion,
	}).Validate(); err == nil {
		t.Fatal("Question event without record accepted")
	}
}
