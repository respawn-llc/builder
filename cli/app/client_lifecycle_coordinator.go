package app

import (
	"fmt"

	"core/shared/clientui"
	"core/shared/lifecyclecontract"
	"core/shared/serverapi"
)

type lifecycleEnvelopeSink interface {
	AcceptLifecycleEnvelope(lifecyclecontract.Envelope)
}

type clientLifecycleCoordinator struct {
	sink            lifecycleEnvelopeSink
	contextSnapshot func() lifecyclecontract.Context
	focusedSnapshot func() bool
}

func newClientLifecycleCoordinator(
	sink lifecycleEnvelopeSink,
	contextSnapshot func() lifecyclecontract.Context,
	focusedSnapshot func() bool,
) *clientLifecycleCoordinator {
	if contextSnapshot == nil {
		contextSnapshot = func() lifecyclecontract.Context { return lifecyclecontract.Context{} }
	}
	if focusedSnapshot == nil {
		focusedSnapshot = func() bool { return false }
	}
	return &clientLifecycleCoordinator{
		sink:            sink,
		contextSnapshot: contextSnapshot,
		focusedSnapshot: focusedSnapshot,
	}
}

func (c *clientLifecycleCoordinator) AcceptLiveRunBatchFinished(
	fact clientui.TranscriptLiveRunBatchFinished,
) {
	if c == nil || c.sink == nil {
		return
	}
	if err := fact.Validate(); err != nil {
		panic(fmt.Sprintf("accept client lifecycle batch-finished fact: %v", err))
	}
	var category lifecyclecontract.Category
	var details lifecyclecontract.Details
	var truncation *lifecyclecontract.Truncation
	switch fact.Disposition {
	case clientui.LiveRunBatchDispositionFinalAnswer:
		category = lifecyclecontract.CategoryTaskComplete
		details = lifecyclecontract.NewTaskCompleteDetails(
			fact.FinalAnswerPreview.Markdown,
			fact.WorkPerformed,
		)
		truncation = lifecycleFinalAnswerTruncation(fact.FinalAnswerPreview)
	case clientui.LiveRunBatchDispositionRuntimeFailure:
		category = lifecyclecontract.CategoryTaskError
		details = lifecyclecontract.NewTaskErrorDetails(fact.FailureDiagnostic.Detail)
	case clientui.LiveRunBatchDispositionNoFinalAnswer:
		category = lifecyclecontract.CategoryTaskError
		details = lifecyclecontract.NewTaskErrorDetails(serverapi.ErrRuntimeNoFinalAnswer.Error())
	default:
		return
	}
	envelope, err := lifecyclecontract.NewEnvelope(lifecyclecontract.EnvelopeInput{
		Scope:      lifecyclecontract.ScopeClient,
		Category:   category,
		OccurredAt: fact.FinishedAt,
		Focused:    c.focusedSnapshot(),
		Context:    c.contextSnapshot(),
		Details:    details,
		Truncation: truncation,
	})
	if err != nil {
		panic(fmt.Sprintf("build client lifecycle terminal envelope: %v", err))
	}
	c.sink.AcceptLifecycleEnvelope(envelope)
}

func lifecycleFinalAnswerTruncation(
	preview *clientui.TranscriptFinalAnswerPreview,
) *lifecyclecontract.Truncation {
	if preview == nil || preview.Truncation == nil {
		return nil
	}
	return &lifecyclecontract.Truncation{
		Fields: []lifecyclecontract.TruncationField{
			lifecyclecontract.TruncationFieldFinalAnswer,
		},
	}
}
