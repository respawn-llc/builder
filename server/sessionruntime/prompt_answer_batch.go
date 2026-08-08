package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

type PromptQuestionAnswerCommand struct {
	SelectedOptionNumber *int
	Freeform             *string
}

func (PromptQuestionAnswerCommand) promptAnswerPayload() {}

type PromptApprovalAnswerCommand struct {
	Decision   tools.AskQuestionApprovalDecision
	Commentary *string
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
	resolution promptResolution
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
		resolution, err := promptResolutionForCommand(entry, command)
		if err != nil {
			s.mu.RUnlock()
			return nil, err
		}
		prepared = append(prepared, preparedPromptAnswer{
			command:    command,
			entry:      entry,
			resolution: resolution,
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
	return true, s.deliverPromptResolution(answer.entry, answer.resolution)
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
	resolution promptResolution,
) error {
	<-entry.publicationDone
	publicationErr := s.publishResolved(entry.snapshot)
	entry.response <- executionPromptResult{resolution: resolution}
	return publicationErr
}

func promptResolutionForCommand(
	entry *executionPromptEntry,
	command PromptAnswerCommand,
) (promptResolution, error) {
	if entry == nil {
		return nil, errors.New("pending prompt entry is required")
	}
	switch answer := command.Payload.(type) {
	case PromptQuestionAnswerCommand:
		response := questionResolutionResponse(command.PromptID, answer)
		if err := validatePromptResponse(entry, response, nil); err != nil {
			return nil, err
		}
		if _, err := preparedSuccessorPromptIDs(entry.snapshot.Request, nil); err != nil {
			return nil, err
		}
		resolution := promptQuestionResolution{
			selectedOptionNumber: answer.SelectedOptionNumber,
		}
		if answer.Freeform != nil {
			resolution.text = &promptQuestionText{
				target: promptQuestionTextTargetFreeform,
				value:  *answer.Freeform,
			}
		}
		return resolution, nil
	case PromptApprovalAnswerCommand:
		response := approvalResolutionResponse(command.PromptID, answer)
		if err := validatePromptResponse(entry, response, nil); err != nil {
			return nil, err
		}
		if _, err := preparedSuccessorPromptIDs(entry.snapshot.Request, nil); err != nil {
			return nil, err
		}
		return promptApprovalResolution{answer: answer}, nil
	case PromptDeclinedCommand:
		if err := validatePromptResponse(entry, tools.AskQuestionResponse{}, context.Canceled); err != nil {
			return nil, err
		}
		if _, err := preparedSuccessorPromptIDs(entry.snapshot.Request, context.Canceled); err != nil {
			return nil, err
		}
		return promptFailureResolution{cause: context.Canceled}, nil
	default:
		return nil, errors.New("prompt answer command disposition is invalid")
	}
}

func questionResolutionResponse(
	promptID clientui.PromptID,
	answer PromptQuestionAnswerCommand,
) tools.AskQuestionResponse {
	response := tools.AskQuestionResponse{
		RequestID:            string(promptID),
		SelectedOptionNumber: answer.SelectedOptionNumber,
	}
	if answer.Freeform != nil {
		response.FreeformAnswer = *answer.Freeform
	}
	return response
}

func approvalResolutionResponse(
	promptID clientui.PromptID,
	answer PromptApprovalAnswerCommand,
) tools.AskQuestionResponse {
	payload := &tools.AskQuestionApprovalPayload{Decision: answer.Decision}
	if answer.Commentary != nil {
		payload.Commentary = *answer.Commentary
	}
	return tools.AskQuestionResponse{
		RequestID: string(promptID),
		Approval:  payload,
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
		switch answer := command.Payload.(type) {
		case PromptQuestionAnswerCommand:
			if answer.SelectedOptionNumber == nil && answer.Freeform == nil {
				return fmt.Errorf("prompt answer command %d question answer is required", index)
			}
			if answer.SelectedOptionNumber != nil && *answer.SelectedOptionNumber <= 0 {
				return fmt.Errorf("prompt answer command %d selected option number must be positive", index)
			}
			if answer.Freeform != nil && strings.TrimSpace(*answer.Freeform) == "" {
				return fmt.Errorf("prompt answer command %d freeform must be non-blank", index)
			}
		case PromptApprovalAnswerCommand:
			switch answer.Decision {
			case tools.AskQuestionApprovalDecisionAllowOnce,
				tools.AskQuestionApprovalDecisionAllowSession,
				tools.AskQuestionApprovalDecisionDeny:
			default:
				return fmt.Errorf("prompt answer command %d approval decision is invalid", index)
			}
			if answer.Commentary != nil && strings.TrimSpace(*answer.Commentary) == "" {
				return fmt.Errorf("prompt answer command %d commentary must be non-blank", index)
			}
		case PromptDeclinedCommand:
		default:
			return fmt.Errorf("prompt answer command %d has an invalid answer variant", index)
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
