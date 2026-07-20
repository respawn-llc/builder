package app

import (
	"fmt"
	"strings"
	"time"

	"core/shared/clientui"
	"core/shared/lifecyclecontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type lifecycleEnvelopeSink interface {
	AcceptLifecycleEnvelope(lifecyclecontract.Envelope)
}

type clientLifecycleCoordinator struct {
	sink            lifecycleEnvelopeSink
	contextSnapshot func() lifecyclecontract.Context
	focusedSnapshot func() bool
	now             func() time.Time
	sessionID       *runtimeids.SessionID
	sessionTitle    *string
}

func newClientLifecycleCoordinator(
	sink lifecycleEnvelopeSink,
	contextSnapshot func() lifecyclecontract.Context,
	focusedSnapshot func() bool,
	now func() time.Time,
) *clientLifecycleCoordinator {
	if contextSnapshot == nil {
		contextSnapshot = func() lifecyclecontract.Context { return lifecyclecontract.Context{} }
	}
	if focusedSnapshot == nil {
		focusedSnapshot = func() bool { return false }
	}
	clock := time.Now
	if now != nil {
		clock = now
	}
	return &clientLifecycleCoordinator{
		sink:            sink,
		contextSnapshot: contextSnapshot,
		focusedSnapshot: focusedSnapshot,
		now:             clock,
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
	c.emit(category, fact.FinishedAt, details, truncation, nil)
}

func (c *clientLifecycleCoordinator) AcceptSessionIdentity(identity clientui.TranscriptSessionIdentity) {
	if c == nil {
		return
	}
	if err := identity.Validate(); err != nil {
		panic(fmt.Sprintf("accept client lifecycle session identity: %v", err))
	}
	sessionID := identity.SessionID
	c.sessionID = &sessionID
	c.sessionTitle = nil
	if identity.SessionName != nil {
		title := strings.Clone(*identity.SessionName)
		c.sessionTitle = &title
	}
}

func (c *clientLifecycleCoordinator) AcceptAttentionFact(fact attentionFact) {
	if c == nil || c.sink == nil {
		return
	}
	var kind lifecyclecontract.InputKind
	switch fact.kind {
	case attentionFactKindQuestion:
		kind = lifecyclecontract.InputKindQuestion
	case attentionFactKindApproval:
		kind = lifecyclecontract.InputKindApproval
	default:
		panic(fmt.Sprintf("accept client lifecycle attention fact: unknown kind %d", fact.kind))
	}
	var truncation *lifecyclecontract.Truncation
	if fact.summaryTruncated {
		truncation = &lifecyclecontract.Truncation{
			Fields: []lifecyclecontract.TruncationField{
				lifecyclecontract.TruncationFieldInputSummary,
			},
		}
	}
	c.emit(
		lifecyclecontract.CategoryInputRequired,
		fact.occurredAt,
		lifecyclecontract.NewInputRequiredDetails(kind, fact.summary),
		truncation,
		fact.workflowTaskID,
	)
}

func (c *clientLifecycleCoordinator) AcceptCompactionStatus(status clientui.TranscriptCompactionStatus) {
	if c == nil || c.sink == nil {
		return
	}
	if err := status.Validate(); err != nil {
		panic(fmt.Sprintf("accept client lifecycle compaction status: %v", err))
	}
	if status.State != clientui.CompactionStarted ||
		status.Initiator != clientui.CompactionInitiatorAutomatic {
		return
	}
	c.emit(
		lifecyclecontract.CategoryResourceLimit,
		c.now().UTC(),
		lifecyclecontract.NewResourceLimitDetails(status.Mode),
		nil,
		nil,
	)
}

func (c *clientLifecycleCoordinator) emit(
	category lifecyclecontract.Category,
	occurredAt time.Time,
	details lifecyclecontract.Details,
	truncation *lifecyclecontract.Truncation,
	workflowTaskID *lifecyclecontract.WorkflowTaskID,
) {
	context := c.currentContext()
	if workflowTaskID != nil {
		taskID := *workflowTaskID
		context.WorkflowTaskID = &taskID
	}
	envelope, err := lifecyclecontract.NewEnvelope(lifecyclecontract.EnvelopeInput{
		Scope:      lifecyclecontract.ScopeClient,
		Category:   category,
		OccurredAt: occurredAt,
		Focused:    c.focusedSnapshot(),
		Context:    context,
		Details:    details,
		Truncation: truncation,
	})
	if err != nil {
		panic(fmt.Sprintf("build client lifecycle envelope for %q: %v", category, err))
	}
	c.sink.AcceptLifecycleEnvelope(envelope)
}

func (c *clientLifecycleCoordinator) currentContext() lifecyclecontract.Context {
	context := c.contextSnapshot()
	if c.sessionID != nil {
		sessionID := *c.sessionID
		context.SessionID = &sessionID
	}
	if c.sessionTitle != nil {
		title := strings.Clone(*c.sessionTitle)
		context.SessionTitle = &title
	} else if c.sessionID != nil {
		context.SessionTitle = nil
	}
	return context
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
