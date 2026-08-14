package sessionview

import (
	"encoding/json"
	"fmt"
	"strings"

	"core/server/session"
	"core/shared/transcript"
)

type questionHistoryRecord struct {
	Question             string
	Answer               string
	SelectedOptionNumber *int
	Commentary           *string
	At                   *transcript.CommittedAtUnixMs
}

func projectQuestionHistoryRecord(
	record session.EventRecord,
	version int,
) (*questionHistoryRecord, error) {
	payload, err := record.Payload()
	if err != nil {
		return nil, err
	}
	completion, ok := payload.(session.ToolCompletionRecord)
	if !ok || completion.Name != "ask_question" || completion.IsError {
		return nil, nil
	}
	presentation, ok := transcript.DecodeToolCallMeta(completion.Presentation)
	if !ok || strings.TrimSpace(presentation.Question) == "" {
		return nil, nil
	}
	question := strings.TrimSpace(presentation.Question)
	switch version {
	case session.EventLogVersionV1:
		var answer string
		if err := json.Unmarshal(completion.Output, &answer); err != nil {
			return nil, nil
		}
		return &questionHistoryRecord{
			Question: question,
			Answer:   answer,
		}, nil
	case session.EventLogVersionV2:
		if completion.QuestionAnswer == nil {
			return nil, fmt.Errorf(
				"event sequence %d successful ask_question completion is missing typed Question-answer facts",
				record.Seq(),
			)
		}
		answer := completion.QuestionAnswer
		projected := &questionHistoryRecord{
			Question: question,
			At:       record.CommittedAtUnixMs(),
		}
		if projected.At == nil {
			return nil, fmt.Errorf(
				"event sequence %d successful ask_question completion is missing committed timestamp",
				record.Seq(),
			)
		}
		if answer.SelectedOptionNumber == nil {
			if answer.Freeform == nil {
				return nil, fmt.Errorf(
					"event sequence %d typed Question answer has no answer",
					record.Seq(),
				)
			}
			projected.Answer = *answer.Freeform
			return projected, nil
		}
		optionIndex := *answer.SelectedOptionNumber - 1
		if optionIndex < 0 || optionIndex >= len(presentation.Suggestions) {
			return nil, nil
		}
		projected.Answer = presentation.Suggestions[optionIndex]
		selected := *answer.SelectedOptionNumber
		projected.SelectedOptionNumber = &selected
		if answer.Freeform != nil {
			commentary := *answer.Freeform
			projected.Commentary = &commentary
		}
		return projected, nil
	default:
		return nil, fmt.Errorf("unsupported event log version %d", version)
	}
}
