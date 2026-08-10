package runtime

import (
	"context"
	"errors"

	"core/server/llm"
)

type reviewerFollowUpWork struct {
	stepID                    string
	original                  llm.Message
	originalCommittedStart    int
	originalCommittedStartSet bool
	reviewerClient            llm.Client
	pipeline                  reviewerPipeline
}

type reviewerFollowUpRuntimeResult struct {
	result reviewerFollowUpResult
	err    error
}

type reviewerFollowUpApplication struct {
	err error
}

func (e *Engine) runReviewerFollowUpAsRuntimeWork(
	ctx context.Context,
	stepID string,
	original llm.Message,
	originalCommittedStart int,
	originalCommittedStartSet bool,
	reviewerClient llm.Client,
	pipeline reviewerPipeline,
) (reviewerFollowUpResult, error) {
	if pipeline == nil {
		return reviewerFollowUpResult{}, errors.New("Reviewer pipeline is not configured")
	}
	work := reviewerFollowUpWork{
		stepID:                    stepID,
		original:                  original,
		originalCommittedStart:    originalCommittedStart,
		originalCommittedStartSet: originalCommittedStartSet,
		reviewerClient:            reviewerClient,
		pipeline:                  pipeline,
	}
	if e.runtimeEvents == nil {
		return reviewerFollowUpResult{}, errors.New("Reviewer work requires a Runtime Event queue")
	}
	return submitRuntimeEventWork(
		ctx,
		ctx,
		e.lifecycleCtx,
		e,
		work,
		func(
			admission runtimeEventAdmission,
			accepted reviewerFollowUpWork,
		) error {
			return e.applyReviewerStarted(admission, accepted.stepID)
		},
		func(
			workCtx context.Context,
			accepted reviewerFollowUpWork,
		) reviewerFollowUpRuntimeResult {
			result, workErr := accepted.pipeline.RunFollowUp(
				workCtx,
				accepted.stepID,
				accepted.original,
				accepted.originalCommittedStart,
				accepted.originalCommittedStartSet,
				accepted.reviewerClient,
			)
			return reviewerFollowUpRuntimeResult{
				result: result,
				err:    workErr,
			}
		},
		func(
			admission runtimeEventAdmission,
			accepted reviewerFollowUpWork,
			terminal reviewerFollowUpRuntimeResult,
		) (reviewerFollowUpApplication, error) {
			return reviewerFollowUpApplication{
				err: e.applyReviewerCompleted(
					admission,
					accepted.stepID,
					terminal.result.Completion,
				),
			}, nil
		},
		func(
			terminal reviewerFollowUpRuntimeResult,
			applied reviewerFollowUpApplication,
			resultErr error,
		) (reviewerFollowUpResult, error) {
			return terminal.result, errors.Join(
				terminal.err,
				applied.err,
				resultErr,
			)
		},
	)
}

func (e *Engine) applyReviewerStarted(
	admission runtimeEventAdmission,
	stepID string,
) error {
	return admission.applySteering(
		stepID,
		steerEventIntent(Event{
			Kind:   EventReviewerStarted,
			StepID: stepID,
		}),
	)
}

func (e *Engine) applyReviewerCompleted(
	admission runtimeEventAdmission,
	stepID string,
	status *ReviewerStatus,
) error {
	return admission.applySteering(
		stepID,
		steerEventIntent(Event{
			Kind:     EventReviewerCompleted,
			StepID:   stepID,
			Reviewer: status,
		}),
	)
}
