package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

// SubmitExactApprovalCommentary commits one Allow commentary message to the
// existing Runtime FIFO and returns as soon as that FIFO accepts it.
func (e *Engine) SubmitExactApprovalCommentary(identity tools.ExecutionIdentity, commentary string) error {
	if e == nil {
		return ErrEngineClosed
	}
	if strings.TrimSpace(commentary) == "" {
		return errors.New("Approval commentary is required")
	}
	if err := e.validateExactApprovalCommentary(identity); err != nil {
		return err
	}
	deferred, accepted := trySubmitEngineRuntimeOperation(e, func(context.Context) (struct{}, error) {
		if err := e.validateExactApprovalCommentary(identity); err != nil {
			return struct{}{}, err
		}
		err := e.steer(identity.StepID, steerMessagesWithPersistenceIntent(
			steeringPriorityUser,
			steeringMessageEventDefault,
			true,
			[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value(commentary)}},
		))
		return struct{}{}, err
	})
	if !accepted {
		return ErrEngineClosed
	}
	e.approvalCommentaryMu.Lock()
	if e.approvalCommentary == nil {
		e.approvalCommentary = make(map[string][]runtimeDeferred[struct{}])
	}
	e.approvalCommentary[identity.StepID] = append(e.approvalCommentary[identity.StepID], deferred)
	e.approvalCommentaryMu.Unlock()
	return nil
}

func (e *Engine) validateExactApprovalCommentary(identity tools.ExecutionIdentity) error {
	if _, err := tools.ExecutionIdentityFromContext(tools.WithExecutionIdentity(context.Background(), identity)); err != nil {
		return err
	}
	snapshot := e.ActiveStepSnapshot()
	if snapshot == nil || snapshot.RunID != identity.RunID || snapshot.StepID != identity.StepID {
		return ErrActiveStepInactive
	}
	_, exists, err := e.transcriptRuntimeState().ToolCallSnapshot(string(identity.ToolCallID))
	if err != nil {
		return fmt.Errorf("snapshot Approval commentary Tool Call %q: %w", identity.ToolCallID, err)
	}
	if !exists {
		return fmt.Errorf("Approval commentary Tool Call %q is not active", identity.ToolCallID)
	}
	return nil
}

func (e *Engine) takeApprovalCommentaryError(stepID string) error {
	e.approvalCommentaryMu.Lock()
	deferred := e.approvalCommentary[stepID]
	delete(e.approvalCommentary, stepID)
	e.approvalCommentaryMu.Unlock()
	var err error
	for _, result := range deferred {
		_, resultErr := result.Await(context.Background())
		err = errors.Join(err, resultErr)
	}
	return err
}
