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

type PromptQuestionAnswerCommand struct {
	Answer tools.AskQuestionAnswer
}

func (PromptQuestionAnswerCommand) promptAnswerPayload() {}

type PromptApprovalAnswerCommand struct {
	Answer tools.AskQuestionApproval
}

func (PromptApprovalAnswerCommand) promptAnswerPayload() {}

type PromptDeclinedCommand struct{}

func (PromptDeclinedCommand) promptAnswerPayload() {}

type PromptAnswerPayload interface {
	promptAnswerPayload()
}

type PromptAnswerCommand struct {
	PromptID clientui.PromptID
	Payload  PromptAnswerPayload
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
	command    PromptAnswerCommand
	entry      *executionPromptEntry
	resolution tools.AskQuestionResolution
	submitErr  error
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
		resolution, submitErr, err := promptResolutionForCommand(entry, command)
		if err != nil {
			s.mu.RUnlock()
			return nil, err
		}
		prepared = append(prepared, preparedPromptAnswer{
			command:    command,
			entry:      entry,
			resolution: resolution,
			submitErr:  submitErr,
		})
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
	s.mu.Lock()
	removed := s.removePromptEntryLocked(string(answer.command.PromptID), answer.entry)
	s.mu.Unlock()
	if !removed {
		return false, nil
	}
	return true, s.deliverPromptResolution(answer.entry, answer.resolution, answer.submitErr)
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
	resolution tools.AskQuestionResolution,
	submitErr error,
) error {
	<-entry.publicationDone
	publicationErr := s.publishResolved(entry.snapshot)
	entry.response <- executionPromptResult{resolution: resolution, err: submitErr}
	return publicationErr
}

func promptResolutionForCommand(
	entry *executionPromptEntry,
	command PromptAnswerCommand,
) (tools.AskQuestionResolution, error, error) {
	if entry == nil {
		return nil, nil, errors.New("pending prompt entry is required")
	}
	var submitErr error
	var resolution tools.AskQuestionResolution
	switch answer := command.Payload.(type) {
	case PromptQuestionAnswerCommand:
		resolution = answer.Answer
	case PromptApprovalAnswerCommand:
		resolution = answer.Answer
	case PromptDeclinedCommand:
		submitErr = context.Canceled
	default:
		return nil, nil, errors.New("prompt answer command payload is invalid")
	}
	if err := validatePromptResolution(entry, resolution, submitErr); err != nil {
		return nil, nil, err
	}
	if _, err := preparedSuccessorPromptIDs(entry.snapshot.Request, submitErr); err != nil {
		return nil, nil, err
	}
	if submitErr != nil {
		return nil, submitErr, nil
	}
	return resolution, nil, nil
}

func validatePromptResolution(
	entry *executionPromptEntry,
	resolution tools.AskQuestionResolution,
	submitErr error,
) error {
	if entry == nil {
		return errors.New("pending prompt entry is required")
	}
	if submitErr != nil {
		return nil
	}
	return tools.ValidateAskQuestionResolution(entry.snapshot.Request, resolution)
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
		var resolution tools.AskQuestionResolution
		switch answer := command.Payload.(type) {
		case PromptQuestionAnswerCommand:
			resolution = answer.Answer
		case PromptApprovalAnswerCommand:
			resolution = answer.Answer
		case PromptDeclinedCommand:
			resolution = tools.AskQuestionDeclined{}
		default:
			return fmt.Errorf("prompt answer command %d has an invalid payload variant", index)
		}
		if err := tools.ValidateAskQuestionResolutionShape(resolution); err != nil {
			return fmt.Errorf("prompt answer command %d: %w", index, err)
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
