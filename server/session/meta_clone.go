package session

func cloneMeta(in Meta) Meta {
	out := in
	out.InputDraftRecoveryBuffers = append([]InputDraftRecoveryBuffer(nil), in.InputDraftRecoveryBuffers...)
	out.Continuation = cloneContinuationContext(in.Continuation)
	if in.PendingModelRecovery != nil {
		pending := *in.PendingModelRecovery
		pending.OutstandingToolCallIDs = append([]string(nil), in.PendingModelRecovery.OutstandingToolCallIDs...)
		out.PendingModelRecovery = &pending
	}
	out.WorktreeReminder = CloneWorktreeReminderState(in.WorktreeReminder)
	if in.UsageState != nil {
		usage := *in.UsageState
		out.UsageState = &usage
	}
	if in.Goal != nil {
		goal := *in.Goal
		out.Goal = &goal
	}
	if in.WorkflowSession != nil {
		workflow := *in.WorkflowSession
		out.WorkflowSession = &workflow
	}
	out.Locked = cloneLockedContract(in.Locked)
	return out
}
