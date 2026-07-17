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
	switch message.Kind {
	case clientui.TranscriptMessageHydration:
		hydration := message.Payload.Hydration
		candidate := runtimeTupleFromReadModelUpdate(hydration.RuntimeReadModelUpdate)
		decision := decideRuntimeTuple(c.mainView.Version, candidate.Version, runtimeTupleIngressHydration)
		if decision == runtimeTupleIgnore && !runtimeTupleMatchesView(candidate, c.mainView) {
			return runtimeTupleMergeResult{}, hydrationRuntimeTupleError(c.mainView, candidate)
		}
		if decision == runtimeTupleApply {
			applyRuntimeTuple(&c.mainView, candidate)
		}
		applyTranscriptHydrationMetadataToMainView(&c.mainView, *hydration)
		c.ensureMainViewIdentity()
		c.advanceMetadataRevision()
		result = runtimeTupleMergeResult{decision: decision, view: c.mainView, project: true}
	case clientui.TranscriptMessageRuntimeReadModelUpdate:
		candidate := runtimeTupleFromReadModelUpdate(*message.Payload.RuntimeReadModelUpdate)
		decision := decideRuntimeTuple(c.mainView.Version, candidate.Version, runtimeTupleIngressIncremental)
		if decision == runtimeTupleApply {
			applyRuntimeTuple(&c.mainView, candidate)
			if c.ensureMainViewIdentity() {
				c.advanceMetadataRevision()
			}
		}
		result = runtimeTupleMergeResult{decision: decision, view: c.mainView, project: decision == runtimeTupleApply}
	default:
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
	switch message.Kind {
	case clientui.TranscriptMessageSessionStatus:
		applyTranscriptSessionStatusToRuntimeStatus(&view.Status, *message.Payload.SessionStatus)
	case clientui.TranscriptMessageSessionIdentity:
		applyTranscriptSessionIdentityToRuntimeView(&view.Session, *message.Payload.SessionIdentity)
	case clientui.TranscriptMessageContextUsage:
		view.Status.ContextUsage = runtimeContextUsageFromTranscript(*message.Payload.ContextUsage)
	case clientui.TranscriptMessageGoalStatus:
		view.Status.Goal = runtimeGoalFromTranscript(*message.Payload.GoalStatus)
	case clientui.TranscriptMessageCompactionStatus:
		view.Status.CompactionCount = message.Payload.CompactionStatus.Count
	default:
		return false
	}
	return true
}

func applyTranscriptHydrationMetadataToMainView(view *clientui.RuntimeMainView, hydration clientui.TranscriptHydration) {
	applyTranscriptSessionStatusToRuntimeStatus(&view.Status, hydration.SessionStatus)
	applyTranscriptSessionIdentityToRuntimeView(&view.Session, hydration.SessionIdentity)
	view.Status.ContextUsage = clientui.RuntimeContextUsage{}
	if hydration.ContextUsage != nil {
		view.Status.ContextUsage = runtimeContextUsageFromTranscript(*hydration.ContextUsage)
	}
	view.Status.Goal = nil
	if hydration.GoalStatus != nil {
		view.Status.Goal = runtimeGoalFromTranscript(*hydration.GoalStatus)
	}
	if hydration.ActiveCompaction != nil {
		view.Status.CompactionCount = hydration.ActiveCompaction.Count
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
	status.PreviousSessionID = textutil.Pointer(update.PreviousSessionID)
	status.ParentAgentSessionID = textutil.Pointer(update.ParentAgentSessionID)
	status.NavigationTargetSessionID = textutil.Pointer(update.NavigationTargetSessionID)
	status.WorkflowActive = false
	status.WorkflowSession = nil
	if update.Workflow != nil {
		status.WorkflowActive = update.Workflow.Active
		status.WorkflowSession = &clientui.WorkflowSessionStatus{
			RunID:      update.Workflow.RunID,
			TaskID:     update.Workflow.TaskID,
			WorkflowID: update.Workflow.WorkflowID,
		}
	}
}

func applyTranscriptSessionIdentityToRuntimeView(view *clientui.RuntimeSessionView, identity clientui.TranscriptSessionIdentity) {
	view.SessionID = identity.SessionID.String()
	view.SessionName = ""
	if identity.SessionName != nil {
		view.SessionName = *identity.SessionName
	}
	view.ConversationFreshness = identity.ConversationFreshness
	view.ExecutionTarget = clientui.SessionExecutionTarget{}
	if identity.ExecutionTarget != nil {
		view.ExecutionTarget = clientui.NormalizeSessionExecutionTarget(*identity.ExecutionTarget)
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
	if status.Goal == nil {
		return nil
	}
	return &clientui.RuntimeGoal{
		ID:        status.Goal.ID,
		Objective: status.Goal.Objective,
		Status:    status.Goal.Status,
		Suspended: status.Goal.Suspended,
	}
}
