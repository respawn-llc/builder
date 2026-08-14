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
	out.ChatSettings = cloneChatSettingsOverrides(in.ChatSettings)
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
	out.Locked = cloneLockedContract(in.Locked)
	out.ActiveWorkflowAssignment = cloneMessageRecord(in.ActiveWorkflowAssignment)
	return out
}

func cloneMessageRecord(in *MessageRecord) *MessageRecord {
	if in == nil {
		return nil
	}
	out, err := normalizeMessageRecord(*in)
	if err != nil {
		panic(err)
	}
	return &out
}
