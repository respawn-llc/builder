package sessionview

import (
	"context"
	"io"

	"core/server/session"
	"core/shared/serverapi"
)

const largeQuestionHistoryBytes = int64(1_073_741_824)

type questionHistorySubscription struct {
	cursor    *session.QuestionHistoryCursor
	started   bool
	completed bool
}

func (s *questionHistorySubscription) Next(ctx context.Context) (serverapi.QuestionHistoryEvent, error) {
	if err := ctx.Err(); err != nil {
		return serverapi.QuestionHistoryEvent{}, err
	}
	if !s.started {
		s.started = true
		large := s.cursor.InitialSize() >= largeQuestionHistoryBytes
		event := serverapi.QuestionHistoryEvent{
			Kind:         serverapi.QuestionHistoryEventStarted,
			LargeHistory: &large,
		}
		return event, event.Validate()
	}
	for !s.completed {
		if err := ctx.Err(); err != nil {
			return serverapi.QuestionHistoryEvent{}, err
		}
		record, err := s.cursor.Next()
		if err != nil {
			return serverapi.QuestionHistoryEvent{}, err
		}
		if record == nil {
			s.completed = true
			omitted := s.cursor.HistoryOmitted()
			event := serverapi.QuestionHistoryEvent{
				Kind:           serverapi.QuestionHistoryEventCompleted,
				HistoryOmitted: &omitted,
			}
			return event, event.Validate()
		}
		question, err := projectQuestionHistoryRecord(*record, s.cursor.Version())
		if err != nil {
			return serverapi.QuestionHistoryEvent{}, err
		}
		if question == nil {
			continue
		}
		event := serverapi.QuestionHistoryEvent{
			Kind: serverapi.QuestionHistoryEventQuestion,
			Question: &serverapi.QuestionHistoryQuestion{
				Question:             question.Question,
				Answer:               question.Answer,
				SelectedOptionNumber: question.SelectedOptionNumber,
				Commentary:           question.Commentary,
				At:                   question.At,
			},
		}
		return event, event.Validate()
	}
	return serverapi.QuestionHistoryEvent{}, io.EOF
}

func (s *questionHistorySubscription) Close() error {
	if s == nil || s.cursor == nil {
		return nil
	}
	err := s.cursor.Close()
	s.cursor = nil
	return err
}

var _ serverapi.QuestionHistorySubscription = (*questionHistorySubscription)(nil)
