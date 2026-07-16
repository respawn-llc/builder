package app

import "core/shared/clientui"

type runtimeActivitySnapshotPatch struct {
	Version             clientui.ReadModelVersion
	Activity            clientui.RuntimeActivity
	InputReconciliation clientui.RuntimeInputReconciliationSnapshot
}

func (c *sessionRuntimeClient) patchVersionedRuntimeActivity(snapshot runtimeActivitySnapshotPatch) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch decideRuntimeActivitySnapshotCache(c.mainView.Version, snapshot.Version) {
	case runtimeActivitySnapshotCacheApply:
		view := &c.mainView
		view.Version = snapshot.Version
		view.Activity = snapshot.Activity
		view.InputReconciliation = snapshot.InputReconciliation
		if view.Session.SessionID == "" {
			view.Session.SessionID = c.sessionID
		}
		c.hasMainView = true
	case runtimeActivitySnapshotCacheRefresh:
		c.readModelStale = true
	}
}

type runtimeActivitySnapshotCacheDecision uint8

const (
	runtimeActivitySnapshotCacheIgnore runtimeActivitySnapshotCacheDecision = iota
	runtimeActivitySnapshotCacheApply
	runtimeActivitySnapshotCacheRefresh
)

func decideRuntimeActivitySnapshotCache(current clientui.ReadModelVersion, incoming clientui.ReadModelVersion) runtimeActivitySnapshotCacheDecision {
	if incoming.Validate() != nil {
		return runtimeActivitySnapshotCacheIgnore
	}
	if current.Validate() != nil {
		return runtimeActivitySnapshotCacheApply
	}
	if incoming.Epoch != current.Epoch {
		return runtimeActivitySnapshotCacheRefresh
	}
	if incoming.Generation != current.Generation {
		if incoming.Generation > current.Generation {
			return runtimeActivitySnapshotCacheRefresh
		}
		return runtimeActivitySnapshotCacheIgnore
	}
	if incoming.Sequence <= current.Sequence {
		return runtimeActivitySnapshotCacheIgnore
	}
	return runtimeActivitySnapshotCacheApply
}

func (c *sessionRuntimeClient) observeTranscriptMessageState(message clientui.TranscriptMessage) {
	if c == nil {
		return
	}
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		switch message.Kind {
		case clientui.TranscriptMessageHydration:
			if message.Payload.Hydration != nil {
				applyTranscriptHydrationToMainView(view, *message.Payload.Hydration)
			}
		case clientui.TranscriptMessageRuntimeReadModelUpdate:
			if message.Payload.RuntimeReadModelUpdate != nil {
				applyTranscriptRuntimeReadModelToMainView(view, *message.Payload.RuntimeReadModelUpdate)
			}
		case clientui.TranscriptMessageSessionStatus:
			if message.Payload.SessionStatus != nil {
				applyTranscriptSessionStatusToRuntimeStatus(&view.Status, *message.Payload.SessionStatus)
			}
		case clientui.TranscriptMessageSessionIdentity:
			if message.Payload.SessionIdentity != nil {
				applyTranscriptSessionIdentityToRuntimeView(&view.Session, *message.Payload.SessionIdentity)
			}
		case clientui.TranscriptMessageContextUsage:
			if message.Payload.ContextUsage != nil {
				view.Status.ContextUsage = runtimeContextUsageFromTranscript(*message.Payload.ContextUsage)
			}
		case clientui.TranscriptMessageGoalStatus:
			if message.Payload.GoalStatus != nil {
				view.Status.Goal = runtimeGoalFromTranscript(*message.Payload.GoalStatus)
			}
		case clientui.TranscriptMessageCompactionStatus:
			if message.Payload.CompactionStatus != nil {
				view.Status.CompactionCount = message.Payload.CompactionStatus.Count
			}
		}
	})
}

func applyTranscriptHydrationToMainView(view *clientui.RuntimeMainView, hydration clientui.TranscriptHydration) {
	applyTranscriptRuntimeReadModelToMainView(view, hydration.RuntimeReadModelUpdate)
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

func applyTranscriptRuntimeReadModelToMainView(view *clientui.RuntimeMainView, update clientui.RuntimeReadModelUpdate) {
	view.Version = update.Version
	view.Activity = update.Activity
	view.InputReconciliation = update.InputReconciliation
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
	status.ParentSessionID = nil
	if update.ParentSessionID != nil {
		parentSessionID := update.ParentSessionID.String()
		status.ParentSessionID = &parentSessionID
	}
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
