package app

import (
	"core/shared/clientui"
	"core/shared/textutil"
)

func (c *sessionRuntimeClient) mergeRuntimeTuple(
	candidate runtimeTupleCandidate,
	ingress runtimeTupleIngress,
) runtimeTupleMergeResult {
	if c == nil {
		return runtimeTupleMergeResult{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	decision := decideRuntimeTuple(c.mainView.Version, candidate.Version, ingress)
	if decision == runtimeTupleApply {
		applyRuntimeTuple(&c.mainView, candidate)
		if c.mainView.Session.SessionID == "" {
			c.mainView.Session.SessionID = c.sessionID
			c.advanceMetadataRevision()
		}
		c.hasMainView = true
	}
	return runtimeTupleMergeResult{decision: decision, view: c.mainView, project: decision == runtimeTupleApply}
}

func (c *sessionRuntimeClient) admitTranscriptMessageState(message clientui.TranscriptMessage) (runtimeTupleMergeResult, error) {
	if c == nil {
		return runtimeTupleMergeResult{}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	result := runtimeTupleMergeResult{decision: runtimeTupleIgnore, view: c.mainView}
	switch message.Kind() {
	case clientui.TranscriptMessageHydration:
		hydration := message.Payload().(clientui.TranscriptHydration)
		candidate := runtimeTupleFromReadModelUpdate(hydration.RuntimeReadModelUpdate)
		decision := decideRuntimeTuple(c.mainView.Version, candidate.Version, runtimeTupleIngressHydration)
		if decision == runtimeTupleIgnore && !runtimeTupleMatchesView(candidate, c.mainView) {
			return runtimeTupleMergeResult{}, hydrationRuntimeTupleError(c.mainView, candidate)
		}
		if decision == runtimeTupleApply {
			applyRuntimeTuple(&c.mainView, candidate)
		}
		applyTranscriptHydrationMetadataToMainView(&c.mainView, hydration)
		c.goalMutationPending = nil
		c.ensureMainViewIdentity()
		c.advanceMetadataRevision()
		result = runtimeTupleMergeResult{decision: decision, view: c.mainView, project: true}
	case clientui.TranscriptMessageRuntimeReadModelUpdate:
		candidate := runtimeTupleFromReadModelUpdate(message.Payload().(clientui.RuntimeReadModelUpdate))
		decision := decideRuntimeTuple(c.mainView.Version, candidate.Version, runtimeTupleIngressIncremental)
		if decision == runtimeTupleApply {
			applyRuntimeTuple(&c.mainView, candidate)
			if c.ensureMainViewIdentity() {
				c.advanceMetadataRevision()
			}
		}
		result = runtimeTupleMergeResult{decision: decision, view: c.mainView, project: decision == runtimeTupleApply}
	default:
		if message.Kind() == clientui.TranscriptMessageGoalStatus {
			c.goalMutationPending = nil
		}
		metadataChanged := applyTranscriptMetadataToMainView(&c.mainView, message)
		if c.ensureMainViewIdentity() || metadataChanged {
			c.advanceMetadataRevision()
		}
		result.view = c.mainView
	}
	return result, nil
}

func (c *sessionRuntimeClient) ensureMainViewIdentity() bool {
	if c.mainView.Session.SessionID == "" {
		c.mainView.Session.SessionID = c.sessionID
		c.hasMainView = true
		return true
	}
	c.hasMainView = true
	return false
}

func applyTranscriptMetadataToMainView(view *clientui.RuntimeMainView, message clientui.TranscriptMessage) bool {
	switch message.Kind() {
	case clientui.TranscriptMessageSessionStatus:
		applyTranscriptSessionStatusToRuntimeStatus(&view.Status, message.Payload().(clientui.TranscriptSessionStatus))
	case clientui.TranscriptMessageSessionIdentity:
		applyTranscriptSessionIdentityToRuntimeMainView(view, message.Payload().(clientui.TranscriptSessionIdentity))
	case clientui.TranscriptMessageContextUsage:
		view.Status.ContextUsage = runtimeContextUsageFromTranscript(message.Payload().(clientui.TranscriptContextUsage))
	case clientui.TranscriptMessageGoalStatus:
		view.Status.Goal = runtimeGoalFromTranscript(message.Payload().(clientui.TranscriptGoalStatus))
	default:
		return false
	}
	return true
}

func applyTranscriptHydrationMetadataToMainView(view *clientui.RuntimeMainView, hydration clientui.TranscriptHydration) {
	applyTranscriptSessionStatusToRuntimeStatus(&view.Status, hydration.SessionStatus)
	applyTranscriptSessionIdentityToRuntimeMainView(view, hydration.SessionIdentity)
	view.Status.ContextUsage = clientui.RuntimeContextUsage{}
	if hydration.ContextUsage != nil {
		view.Status.ContextUsage = runtimeContextUsageFromTranscript(*hydration.ContextUsage)
	}
	view.Status.Goal = nil
	if hydration.GoalStatus != nil {
		view.Status.Goal = runtimeGoalFromTranscript(*hydration.GoalStatus)
	}
}

func applyTranscriptSessionStatusToRuntimeStatus(status *clientui.RuntimeStatus, update clientui.TranscriptSessionStatus) {
	status.ReviewerFrequency = update.ReviewerFrequency
	status.ReviewerEnabled = update.ReviewerEnabled
	status.AutoCompactionEnabled = update.AutoCompactionEnabled
	status.QuestionsEnabled = update.QuestionsEnabled
	status.FastModeAvailable = update.FastModeAvailable
	status.FastModeEnabled = update.FastModeEnabled
	status.ThinkingLevel = update.ThinkingLevel
	status.CompactionMode = update.CompactionMode
	status.CompactionCount = update.CompactionCount
	status.PreviousSessionID = textutil.Pointer(update.PreviousSessionID)
	status.ParentAgentSessionID = textutil.Pointer(update.ParentAgentSessionID)
	status.NavigationTargetSessionID = textutil.Pointer(update.NavigationTargetSessionID)
	status.WorkflowSession = nil
	if update.Workflow != nil {
		status.WorkflowSession = &clientui.WorkflowSessionStatus{
			TaskID:     update.Workflow.TaskID,
			WorkflowID: update.Workflow.WorkflowID,
		}
	}
}

func applyTranscriptSessionIdentityToRuntimeMainView(view *clientui.RuntimeMainView, identity clientui.TranscriptSessionIdentity) {
	view.Session.SessionID = identity.SessionID.String()
	view.Session.SessionName = ""
	if identity.SessionName != nil {
		view.Session.SessionName = *identity.SessionName
	}
	view.Session.ConversationFreshness = identity.ConversationFreshness
	view.Status.ConversationFreshness = identity.ConversationFreshness
	view.Session.ExecutionTarget = clientui.SessionExecutionTarget{}
	if identity.ExecutionTarget != nil {
		view.Session.ExecutionTarget = clientui.NormalizeSessionExecutionTarget(*identity.ExecutionTarget)
	}
}

func runtimeContextUsageFromTranscript(usage clientui.TranscriptContextUsage) clientui.RuntimeContextUsage {
	projected := clientui.RuntimeContextUsage{
		UsedTokens:   usage.UsedTokens,
		WindowTokens: usage.WindowTokens,
	}
	if usage.CacheHitPercent != nil {
		projected.CacheHitPercent = *usage.CacheHitPercent
		projected.HasCacheHitPercentage = true
	}
	return projected
}

func runtimeGoalFromTranscript(status clientui.TranscriptGoalStatus) *clientui.RuntimeGoal {
	goal := &clientui.RuntimeGoal{Availability: status.Availability}
	if status.Goal != nil {
		goal.Goal = status.Goal.Goal
		goal.Suspended = status.Goal.Suspended
	}
	return goal
}
