package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

type PromptAnswerDisposition string

const (
	PromptAnswerDispositionAnswered PromptAnswerDisposition = "answered"
	PromptAnswerDispositionDeclined PromptAnswerDisposition = "declined"
)

type PromptAnswerCommand struct {
	PromptID    clientui.PromptID
	Disposition PromptAnswerDisposition
	Response    tools.AskQuestionResponse
}

type PromptAnswerOutcome string

const (
	PromptAnswerOutcomeResolved PromptAnswerOutcome = "resolved"
	PromptAnswerOutcomeSkipped  PromptAnswerOutcome = "skipped"
)

type PromptAnswerResult struct {
	PromptID clientui.PromptID
	Outcome  PromptAnswerOutcome
}

type preparedPromptAnswer struct {
	command PromptAnswerCommand
	entry   *executionPromptEntry
}

func PendingPromptOrderLess(leftCreatedAt time.Time, leftID string, rightCreatedAt time.Time, rightID string) bool {
	if leftCreatedAt.Equal(rightCreatedAt) {
		return leftID < rightID
	}
	return leftCreatedAt.Before(rightCreatedAt)
}

func (a *Authority) ResolvePromptBatch(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	stepID runtimeids.StepID,
	commands []PromptAnswerCommand,
) ([]PromptAnswerResult, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if sessionID.IsZero() {
		return nil, errors.New("session id is required")
	}
	if stepID.IsZero() {
		return nil, errors.New("step id is required")
	}
	if err := validatePromptAnswerCommands(commands); err != nil {
		return nil, err
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	execution := a.sessionExecution(sessionID)
	if execution == nil {
		return skippedPromptAnswerResults(commands), nil
	}
	return execution.prompts.ResolvePromptBatch(ctx, stepID, commands)
}

func (s *executionPromptStore) ResolvePromptBatch(
	ctx context.Context,
	stepID runtimeids.StepID,
	commands []PromptAnswerCommand,
) ([]PromptAnswerResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if stepID.IsZero() {
		return nil, errors.New("step id is required")
	}
	if err := validatePromptAnswerCommands(commands); err != nil {
		return nil, err
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	results := skippedPromptAnswerResults(commands)
	resultIndexes := make(map[clientui.PromptID]int, len(results))
	for index, result := range results {
		resultIndexes[result.PromptID] = index
	}

	prepared := make([]preparedPromptAnswer, 0, len(commands))
	s.mu.RLock()
	for _, command := range commands {
		entry := s.pending[string(command.PromptID)]
		if entry == nil || entry.snapshot.Request.StepID != stepID.String() {
			continue
		}
		if _, _, err := promptAnswerDelivery(entry, command); err != nil {
			s.mu.RUnlock()
			return nil, err
		}
		prepared = append(prepared, preparedPromptAnswer{command: command, entry: entry})
	}
	s.mu.RUnlock()

	sort.Slice(prepared, func(i, j int) bool {
		left := prepared[i].entry.snapshot
		right := prepared[j].entry.snapshot
		return PendingPromptOrderLess(left.CreatedAt, left.Request.ID, right.CreatedAt, right.Request.ID)
	})
	for _, answer := range prepared {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		resolved, err := s.resolvePreparedPromptAnswer(answer)
		if resolved {
			results[resultIndexes[answer.command.PromptID]].Outcome = PromptAnswerOutcomeResolved
		}
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (s *executionPromptStore) resolvePreparedPromptAnswer(answer preparedPromptAnswer) (bool, error) {
	response, submitErr, err := promptAnswerDelivery(answer.entry, answer.command)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	removed := s.removePromptEntryLocked(string(answer.command.PromptID), answer.entry)
	s.mu.Unlock()
	if !removed {
		return false, nil
	}
	return true, s.deliverPromptResolution(answer.entry, response, submitErr)
}

func (s *executionPromptStore) removePromptEntryLocked(requestID string, expected *executionPromptEntry) bool {
	if s.pending[requestID] != expected {
		return false
	}
	delete(s.pending, requestID)
	return true
}

func (s *executionPromptStore) deliverPromptResolution(
	entry *executionPromptEntry,
	response tools.AskQuestionResponse,
	submitErr error,
) error {
	<-entry.publicationDone
	publicationErr := s.publishResolved(entry.snapshot)
	entry.response <- executionPromptResult{response: response, err: submitErr}
	return publicationErr
}

func promptAnswerDelivery(
	entry *executionPromptEntry,
	command PromptAnswerCommand,
) (tools.AskQuestionResponse, error, error) {
	if entry == nil {
		return tools.AskQuestionResponse{}, nil, errors.New("pending prompt entry is required")
	}
	switch command.Disposition {
	case PromptAnswerDispositionAnswered:
		if err := validatePromptResponse(entry, command.Response, nil); err != nil {
			return tools.AskQuestionResponse{}, nil, err
		}
		if _, err := preparedSuccessorPromptIDs(entry.snapshot.Request, nil); err != nil {
			return tools.AskQuestionResponse{}, nil, err
		}
		return command.Response, nil, nil
	case PromptAnswerDispositionDeclined:
		if err := validatePromptResponse(entry, tools.AskQuestionResponse{}, context.Canceled); err != nil {
			return tools.AskQuestionResponse{}, nil, err
		}
		if _, err := preparedSuccessorPromptIDs(entry.snapshot.Request, context.Canceled); err != nil {
			return tools.AskQuestionResponse{}, nil, err
		}
		return tools.AskQuestionResponse{RequestID: string(command.PromptID)}, context.Canceled, nil
	default:
		return tools.AskQuestionResponse{}, nil, fmt.Errorf("prompt answer disposition %q is invalid", command.Disposition)
	}
}

func validatePromptResponse(
	entry *executionPromptEntry,
	response tools.AskQuestionResponse,
	submitErr error,
) error {
	if entry == nil {
		return errors.New("pending prompt entry is required")
	}
	if submitErr != nil {
		return nil
	}
	return tools.ValidateAskQuestionResponse(entry.snapshot.Request, response)
}

func validatePromptAnswerCommands(commands []PromptAnswerCommand) error {
	if len(commands) == 0 {
		return errors.New("prompt answer commands are required")
	}
	seen := make(map[clientui.PromptID]struct{}, len(commands))
	for index, command := range commands {
		if err := command.PromptID.Validate(); err != nil {
			return fmt.Errorf("prompt answer command %d: %w", index, err)
		}
		switch command.Disposition {
		case PromptAnswerDispositionAnswered:
			if command.Response.RequestID != string(command.PromptID) {
				return fmt.Errorf("prompt answer command %d response request id does not match prompt id", index)
			}
		case PromptAnswerDispositionDeclined:
			if command.Response.RequestID != "" ||
				command.Response.Answer != "" ||
				command.Response.SelectedOptionNumber != nil ||
				command.Response.FreeformAnswer != "" ||
				command.Response.Approval != nil {
				return fmt.Errorf("prompt answer command %d declined disposition cannot contain a response", index)
			}
		default:
			return fmt.Errorf("prompt answer command %d disposition is invalid", index)
		}
		if _, exists := seen[command.PromptID]; exists {
			return fmt.Errorf("prompt answer command prompt id %q is duplicated", command.PromptID)
		}
		seen[command.PromptID] = struct{}{}
	}
	return nil
}

func skippedPromptAnswerResults(commands []PromptAnswerCommand) []PromptAnswerResult {
	results := make([]PromptAnswerResult, 0, len(commands))
	for _, command := range commands {
		results = append(results, PromptAnswerResult{
			PromptID: command.PromptID,
			Outcome:  PromptAnswerOutcomeSkipped,
		})
	}
	return results
}
