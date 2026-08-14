package serverapi

import (
	"context"
	"errors"
	"strings"

	"core/shared/transcript"
)

type QuestionHistorySubscribeRequest struct {
	SessionID   string `json:"session_id"`
	MaxHandoffs int    `json:"max_handoffs"`
}

func (r QuestionHistorySubscribeRequest) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if r.MaxHandoffs < 1 {
		return errors.New("max_handoffs must be positive")
	}
	return nil
}

type QuestionHistoryEventKind string

const (
	QuestionHistoryEventStarted   QuestionHistoryEventKind = "started"
	QuestionHistoryEventQuestion  QuestionHistoryEventKind = "question"
	QuestionHistoryEventCompleted QuestionHistoryEventKind = "completed"
)

type QuestionHistoryQuestion struct {
	Question             string                        `json:"question"`
	Answer               string                        `json:"answer"`
	SelectedOptionNumber *int                          `json:"selected_option_number"`
	Commentary           *string                       `json:"commentary"`
	At                   *transcript.CommittedAtUnixMs `json:"at"`
}

type QuestionHistoryEvent struct {
	Kind           QuestionHistoryEventKind `json:"kind"`
	LargeHistory   *bool                    `json:"large_history,omitempty"`
	Question       *QuestionHistoryQuestion `json:"question,omitempty"`
	HistoryOmitted *bool                    `json:"history_omitted,omitempty"`
}

func (e QuestionHistoryEvent) Validate() error {
	switch e.Kind {
	case QuestionHistoryEventStarted:
		if e.LargeHistory == nil || e.Question != nil || e.HistoryOmitted != nil {
			return errors.New("Question-history started event has invalid fields")
		}
	case QuestionHistoryEventQuestion:
		if e.LargeHistory != nil || e.Question == nil || e.HistoryOmitted != nil {
			return errors.New("Question-history question event has invalid fields")
		}
		if strings.TrimSpace(e.Question.Question) == "" ||
			strings.TrimSpace(e.Question.Answer) == "" {
			return errors.New("Question-history Question and Answer are required")
		}
		if e.Question.SelectedOptionNumber != nil &&
			*e.Question.SelectedOptionNumber < 1 {
			return errors.New("Question-history selected option number must be positive")
		}
		if e.Question.Commentary != nil &&
			strings.TrimSpace(*e.Question.Commentary) == "" {
			return errors.New("Question-history Commentary must not be blank")
		}
		if e.Question.At != nil {
			if err := e.Question.At.Validate(); err != nil {
				return err
			}
		}
	case QuestionHistoryEventCompleted:
		if e.LargeHistory != nil || e.Question != nil || e.HistoryOmitted == nil {
			return errors.New("Question-history completed event has invalid fields")
		}
	default:
		return errors.New("Question-history event kind is invalid")
	}
	return nil
}

type QuestionHistorySubscription interface {
	Next(ctx context.Context) (QuestionHistoryEvent, error)
	Close() error
}
