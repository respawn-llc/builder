package session

func cloneMeta(in Meta) Meta {
	out := in
	if in.PreviousSessionID != nil {
		previousSessionID := *in.PreviousSessionID
		out.PreviousSessionID = &previousSessionID
	}
	if in.ParentAgentSessionID != nil {
		parentAgentSessionID := *in.ParentAgentSessionID
		out.ParentAgentSessionID = &parentAgentSessionID
	}
	out.Continuation = cloneContinuationContext(in.Continuation)
	out.WorktreeReminder = CloneWorktreeReminderState(in.WorktreeReminder)
	if in.UsageState != nil {
		usage := *in.UsageState
		out.UsageState = &usage
	}
	if in.Goal != nil {
		goal := *in.Goal
		out.Goal = &goal
	}
	out.Locked = cloneLockedContract(in.Locked)
	return out
}
