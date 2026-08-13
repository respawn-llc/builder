package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"core/shared/serverapi"
)

type questionHistoryScriptSubscription struct {
	events []serverapi.QuestionHistoryEvent
	err    error
	closed bool
}

func (s *questionHistoryScriptSubscription) Next(context.Context) (serverapi.QuestionHistoryEvent, error) {
	if len(s.events) > 0 {
		event := s.events[0]
		s.events = s.events[1:]
		return event, nil
	}
	if s.err != nil {
		err := s.err
		s.err = nil
		return serverapi.QuestionHistoryEvent{}, err
	}
	return serverapi.QuestionHistoryEvent{}, io.EOF
}

func (s *questionHistoryScriptSubscription) Close() error {
	s.closed = true
	return nil
}

func TestQuestionHistoryHumanStreamingRetainsPartialOutputOnFailure(t *testing.T) {
	sub := &questionHistoryScriptSubscription{
		events: []serverapi.QuestionHistoryEvent{
			{Kind: serverapi.QuestionHistoryEventStarted, LargeHistory: boolPointer(false)},
			{
				Kind: serverapi.QuestionHistoryEventQuestion,
				Question: &serverapi.QuestionHistoryQuestion{
					Question: "opaque-question",
					Answer:   "opaque-answer",
				},
			},
		},
		err: errors.New("terminal failure"),
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exit := streamQuestionHistoryHuman(t.Context(), sub, &stdout, &stderr); exit != 1 {
		t.Fatalf("exit = %d, want 1", exit)
	}
	if stdout.Len() == 0 || stderr.Len() == 0 {
		t.Fatalf("partial output lengths = stdout %d stderr %d", stdout.Len(), stderr.Len())
	}
}

func TestQuestionHistoryJSONStreamsExplicitNullsAndCompletion(t *testing.T) {
	sub := &questionHistoryScriptSubscription{
		events: []serverapi.QuestionHistoryEvent{
			{Kind: serverapi.QuestionHistoryEventStarted, LargeHistory: boolPointer(true)},
			{
				Kind: serverapi.QuestionHistoryEventQuestion,
				Question: &serverapi.QuestionHistoryQuestion{
					Question: "q",
					Answer:   "a",
				},
			},
			{Kind: serverapi.QuestionHistoryEventCompleted, HistoryOmitted: boolPointer(false)},
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exit := streamQuestionHistoryJSON(t.Context(), sub, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit = %d stderr=%s", exit, stderr.String())
	}
	var decoded struct {
		Questions []struct {
			SelectedOptionNumber *int    `json:"selected_option_number"`
			Commentary           *string `json:"commentary"`
			At                   *string `json:"at"`
		} `json:"questions"`
		HistoryOmitted bool `json:"history_omitted"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	if len(decoded.Questions) != 1 ||
		decoded.Questions[0].SelectedOptionNumber != nil ||
		decoded.Questions[0].Commentary != nil ||
		decoded.Questions[0].At != nil ||
		decoded.HistoryOmitted {
		t.Fatalf("decoded JSON output = %#v", decoded)
	}
}

func TestQuestionHistoryJSONLeavesPartialDocumentOnFailure(t *testing.T) {
	sub := &questionHistoryScriptSubscription{
		events: []serverapi.QuestionHistoryEvent{
			{Kind: serverapi.QuestionHistoryEventStarted, LargeHistory: boolPointer(false)},
			{
				Kind: serverapi.QuestionHistoryEventQuestion,
				Question: &serverapi.QuestionHistoryQuestion{
					Question: "q",
					Answer:   "a",
				},
			},
		},
		err: errors.New("terminal failure"),
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exit := streamQuestionHistoryJSON(t.Context(), sub, &stdout, &stderr); exit != 1 {
		t.Fatalf("exit = %d, want 1", exit)
	}
	if json.Valid(stdout.Bytes()) {
		t.Fatalf("partial failure output unexpectedly valid JSON: %s", stdout.Bytes())
	}
}

func boolPointer(value bool) *bool {
	return &value
}
