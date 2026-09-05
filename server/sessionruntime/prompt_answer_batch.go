package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"core/server/runtime"
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
	ToolCallID clientui.ToolCallID
	Payload    PromptAnswerPayload
}

type PromptAnswerOutcome string

const (
	PromptAnswerOutcomeResolved PromptAnswerOutcome = "resolved"
	PromptAnswerOutcomeSkipped  PromptAnswerOutcome = "skipped"
)

type PromptAnswerResult struct {
	ToolCallID clientui.ToolCallID
	Outcome    PromptAnswerOutcome
}

type preparedPromptAnswer struct {
	command       PromptAnswerCommand
	entry         *executionPromptEntry
	resolution    tools.AskQuestionResolution
	submitErr     error
	stepID        runtimeids.StepID
	questionBatch *validatedQuestionBatchDescriptor
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
	return execution.prompts.resolvePromptBatch(ctx, stepID, commands, func(answer preparedPromptAnswer) (bool, error) {
		return a.resolveExactApproval(ctx, execution, answer)
	})
}

func (s *executionPromptStore) ResolvePromptBatch(
	ctx context.Context,
	stepID runtimeids.StepID,
	commands []PromptAnswerCommand,
) ([]PromptAnswerResult, error) {
	return s.resolvePromptBatch(ctx, stepID, commands, nil)
}

func (s *executionPromptStore) resolvePromptBatch(
	ctx context.Context,
	stepID runtimeids.StepID,
	commands []PromptAnswerCommand,
	resolveApproval func(preparedPromptAnswer) (bool, error),
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
	resultIndexes := make(map[clientui.ToolCallID]int, len(results))
	for index, result := range results {
		resultIndexes[result.ToolCallID] = index
	}

	prepared := make([]preparedPromptAnswer, 0, len(commands))
	s.mu.RLock()
	for _, command := range commands {
		entry := s.pending[string(command.ToolCallID)]
		if entry == nil || entry.snapshot.Request.StepID != stepID.String() {
			continue
		}
		resolution, submitErr, questionBatch, err := promptResolutionForCommand(entry, command)
		if err != nil {
			s.mu.RUnlock()
			return nil, err
		}
		prepared = append(prepared, preparedPromptAnswer{
			command:       command,
			entry:         entry,
			resolution:    resolution,
			submitErr:     submitErr,
			stepID:        stepID,
			questionBatch: questionBatch,
		})
	}
	s.mu.RUnlock()

	sort.Slice(prepared, func(i, j int) bool {
		left := prepared[i].entry.snapshot
		right := prepared[j].entry.snapshot
		return PendingPromptOrderLess(left.CreatedAt, left.Request.ToolCallID, right.CreatedAt, right.Request.ToolCallID)
	})
	for _, answer := range prepared {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		var resolved bool
		var err error
		if answer.entry.snapshot.Request.Approval && resolveApproval != nil {
			resolved, err = resolveApproval(answer)
		} else {
			resolved, err = s.resolvePreparedPromptAnswer(ctx, answer)
		}
		if resolved {
			results[resultIndexes[answer.command.ToolCallID]].Outcome = PromptAnswerOutcomeResolved
		}
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (s *executionPromptStore) resolvePreparedPromptAnswer(ctx context.Context, answer preparedPromptAnswer) (bool, error) {
	if answer.entry.snapshot.Request.Approval {
		if answer.entry.approval == nil {
			return false, errors.New("pending Approval lifecycle is required")
		}
		return s.resolveApproval(ctx, answer, nil)
	}
	s.mu.Lock()
	if s.pending[string(answer.command.ToolCallID)] != answer.entry {
		s.mu.Unlock()
		return false, nil
	}
	s.resolvePromptFollowUpLocked(answer.stepID, answer.command.ToolCallID, answer.questionBatch)
	removed := s.removePromptEntryLocked(string(answer.command.ToolCallID), answer.entry)
	s.mu.Unlock()
	if !removed {
		return false, nil
	}
	return true, s.deliverPromptResolution(answer.entry, answer.resolution, answer.submitErr)
}

func (a *Authority) resolveExactApproval(
	ctx context.Context,
	execution *execution,
	answer preparedPromptAnswer,
) (bool, error) {
	execution.exactMu.Lock()
	defer execution.exactMu.Unlock()
	a.mu.Lock()
	live := a.byScope[execution.scope.ID()] == execution
	a.mu.Unlock()
	if !live || execution.resource == nil {
		return false, nil
	}
	return execution.prompts.resolveApproval(ctx, answer, func() error {
		approval, ok := answer.resolution.(tools.AskQuestionApproval)
		if !ok || approval.Decision == tools.AskQuestionApprovalDecisionDeny || approval.Commentary == nil {
			return nil
		}
		identity := tools.ExecutionIdentity{
			RunID:      answer.entry.snapshot.Request.RunID,
			StepID:     answer.entry.snapshot.Request.StepID,
			ToolCallID: answer.command.ToolCallID,
		}
		return execution.resource.withEngine(
			context.Background(),
			execution.resource.ref,
			func(_ context.Context, engine *runtime.Engine) error {
				return engine.SubmitExactApprovalCommentary(identity, *approval.Commentary)
			},
		)
	})
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
	result := resolvedExecutionPromptResult(resolution)
	if submitErr != nil {
		result = declinedExecutionPromptResult(submitErr)
	}
	entry.response <- result
	return publicationErr
}

func promptResolutionForCommand(
	entry *executionPromptEntry,
	command PromptAnswerCommand,
) (tools.AskQuestionResolution, error, *validatedQuestionBatchDescriptor, error) {
	if entry == nil {
		return nil, nil, nil, errors.New("pending prompt entry is required")
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
		return nil, nil, nil, errors.New("prompt answer command payload is invalid")
	}
	if err := validatePromptResolution(entry, resolution, submitErr); err != nil {
		return nil, nil, nil, err
	}
	questionBatch, err := validateQuestionBatchMetadata(entry.snapshot.Request)
	if err != nil {
		return nil, nil, nil, err
	}
	if submitErr != nil {
		return nil, submitErr, questionBatch, nil
	}
	return resolution, nil, questionBatch, nil
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
	seen := make(map[clientui.ToolCallID]struct{}, len(commands))
	for index, command := range commands {
		if err := command.ToolCallID.Validate(); err != nil {
			return fmt.Errorf("prompt answer command %d: %w", index, err)
		}
		switch answer := command.Payload.(type) {
		case PromptQuestionAnswerCommand:
			if err := tools.ValidateAskQuestionResolutionShape(answer.Answer); err != nil {
				return fmt.Errorf("prompt answer command %d: %w", index, err)
			}
		case PromptApprovalAnswerCommand:
			if err := tools.ValidateAskQuestionResolutionShape(answer.Answer); err != nil {
				return fmt.Errorf("prompt answer command %d: %w", index, err)
			}
		case PromptDeclinedCommand:
		default:
			return fmt.Errorf("prompt answer command %d has an invalid payload variant", index)
		}
		if _, exists := seen[command.ToolCallID]; exists {
			return fmt.Errorf("prompt answer command tool call id %q is duplicated", command.ToolCallID)
		}
		seen[command.ToolCallID] = struct{}{}
	}
	return nil
}

func skippedPromptAnswerResults(commands []PromptAnswerCommand) []PromptAnswerResult {
	results := make([]PromptAnswerResult, 0, len(commands))
	for _, command := range commands {
		results = append(results, PromptAnswerResult{
			ToolCallID: command.ToolCallID,
			Outcome:    PromptAnswerOutcomeSkipped,
		})
	}
	return results
}
