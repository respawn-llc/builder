package sessionruntime

import (
	"context"
	"errors"
	"sync"

	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

type approvalPromptState uint8

const (
	approvalPromptWaiting approvalPromptState = iota + 1
	approvalPromptClaimed
	approvalPromptDelivered
	approvalPromptConsumerAccepted
	approvalPromptClosed
)

type approvalPromptLifecycle struct {
	mu             sync.Mutex
	state          approvalPromptState
	result         *executionPromptResult
	stepID         runtimeids.StepID
	questionBatch  *validatedQuestionBatchDescriptor
	terminalDone   chan struct{}
	finalized      chan struct{}
	publicationErr error
}

func newApprovalPromptLifecycle() *approvalPromptLifecycle {
	return &approvalPromptLifecycle{
		state:        approvalPromptWaiting,
		terminalDone: make(chan struct{}),
		finalized:    make(chan struct{}),
	}
}

func (s *executionPromptStore) resolveApproval(
	ctx context.Context,
	answer preparedPromptAnswer,
	afterClaim func() error,
) (bool, error) {
	lifecycle := answer.entry.approval
	lifecycle.mu.Lock()
	if lifecycle.state != approvalPromptWaiting {
		finalized := lifecycle.finalized
		lifecycle.mu.Unlock()
		<-finalized
		return false, nil
	}
	if err := context.Cause(ctx); err != nil {
		lifecycle.mu.Unlock()
		return false, err
	}
	lifecycle.state = approvalPromptClaimed
	lifecycle.stepID = answer.stepID
	lifecycle.questionBatch = answer.questionBatch
	lifecycle.mu.Unlock()

	<-answer.entry.publicationDone
	lifecycle.mu.Lock()
	if lifecycle.state == approvalPromptClosed {
		result := lifecycle.result
		lifecycle.mu.Unlock()
		<-lifecycle.finalized
		return false, errors.Join(result.err, lifecycle.publicationErr)
	}
	var operationErr error
	if afterClaim != nil {
		operationErr = afterClaim()
	}
	if operationErr != nil {
		result := failedExecutionPromptResult(operationErr)
		lifecycle.state = approvalPromptClosed
		lifecycle.result = &result
		close(lifecycle.terminalDone)
		lifecycle.mu.Unlock()
		s.finalizeApproval(answer.entry)
		<-lifecycle.finalized
		return false, errors.Join(operationErr, lifecycle.publicationErr)
	}
	lifecycle.state = approvalPromptDelivered
	delivery := resolvedExecutionPromptResult(answer.resolution)
	if answer.submitErr != nil {
		delivery = declinedExecutionPromptResult(answer.submitErr)
	}
	lifecycle.mu.Unlock()
	select {
	case answer.entry.response <- delivery:
	case <-lifecycle.terminalDone:
	}
	<-lifecycle.finalized
	lifecycle.mu.Lock()
	state, result, publicationErr := lifecycle.state, lifecycle.result, lifecycle.publicationErr
	lifecycle.mu.Unlock()
	if state != approvalPromptConsumerAccepted {
		return false, errors.Join(result.err, publicationErr)
	}
	return true, publicationErr
}

func (s *executionPromptStore) acceptApproval(
	entry *executionPromptEntry,
	delivery executionPromptResult,
) executionPromptResult {
	lifecycle := entry.approval
	lifecycle.mu.Lock()
	if lifecycle.state == approvalPromptDelivered {
		result := delivery
		switch delivery.kind {
		case executionPromptResolved:
			if err := entry.snapshot.Request.AcceptApproval(delivery.resolution); err != nil {
				result = failedExecutionPromptResult(err)
				lifecycle.state = approvalPromptClosed
			} else {
				lifecycle.state = approvalPromptConsumerAccepted
			}
		case executionPromptDeclined:
			lifecycle.state = approvalPromptConsumerAccepted
		case executionPromptFailed:
			lifecycle.state = approvalPromptClosed
		default:
			result = failedExecutionPromptResult(errors.New("Approval delivery kind is invalid"))
			lifecycle.state = approvalPromptClosed
		}
		lifecycle.result = &result
		close(lifecycle.terminalDone)
		lifecycle.mu.Unlock()
		s.finalizeApproval(entry)
	} else {
		lifecycle.mu.Unlock()
	}
	<-lifecycle.finalized
	lifecycle.mu.Lock()
	result := *lifecycle.result
	if result.err == nil && lifecycle.publicationErr != nil {
		result.err = lifecycle.publicationErr
	}
	lifecycle.mu.Unlock()
	return result
}

func (s *executionPromptStore) closeApproval(
	entry *executionPromptEntry,
	err error,
	waitForClaim bool,
) error {
	lifecycle := entry.approval
	lifecycle.mu.Lock()
	if lifecycle.state == approvalPromptWaiting ||
		(!waitForClaim && (lifecycle.state == approvalPromptClaimed || lifecycle.state == approvalPromptDelivered)) {
		result := failedExecutionPromptResult(err)
		lifecycle.state = approvalPromptClosed
		lifecycle.result = &result
		close(lifecycle.terminalDone)
		lifecycle.mu.Unlock()
		s.finalizeApproval(entry)
	} else {
		lifecycle.mu.Unlock()
	}
	<-lifecycle.finalized
	lifecycle.mu.Lock()
	publicationErr := lifecycle.publicationErr
	lifecycle.mu.Unlock()
	return publicationErr
}

func (s *executionPromptStore) finalizeApproval(entry *executionPromptEntry) {
	lifecycle := entry.approval
	<-entry.publicationDone
	s.mu.Lock()
	var publicationErr error
	if s.removePromptEntryLocked(entry.snapshot.Request.ToolCallID, entry) {
		s.resolvePromptFollowUpLocked(
			lifecycle.stepID,
			clientui.ToolCallID(entry.snapshot.Request.ToolCallID),
			lifecycle.questionBatch,
		)
		publicationErr = s.publishResolved(entry.snapshot)
	} else {
		publicationErr = errors.New("Approval terminal finalizer lost its pending prompt")
	}
	s.mu.Unlock()
	lifecycle.mu.Lock()
	lifecycle.publicationErr = publicationErr
	close(lifecycle.finalized)
	lifecycle.mu.Unlock()
}

func (s *executionPromptStore) awaitApproval(
	ctx context.Context,
	entry *executionPromptEntry,
) (tools.AskQuestionResolution, error) {
	ctxDone := ctx.Done()
	for {
		select {
		case delivery := <-entry.response:
			result := s.acceptApproval(entry, delivery)
			return result.resolution, result.err
		case <-ctxDone:
			_ = s.closeApproval(entry, context.Cause(ctx), false)
			goto finalized
		case <-entry.approval.terminalDone:
			goto finalized
		}
	}
finalized:
	<-entry.approval.finalized
	entry.approval.mu.Lock()
	result := *entry.approval.result
	entry.approval.mu.Unlock()
	return result.resolution, result.err
}
