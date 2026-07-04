package registry

import (
	"reflect"
	"sync"

	"core/shared/clientui"
)

type sessionFeedSequencer struct {
	mu       sync.Mutex
	broker   *transcriptSubscriptionBroker
	snapshot sessionFeedSnapshot
}

type sessionFeedSnapshot struct {
	runState            *clientui.RunState
	runtimeActivity     *clientui.RuntimeActivity
	inputReconciliation *clientui.RuntimeInputReconciliationSnapshot
	queuedMessages      map[string]clientui.TranscriptQueuedOrSteeredMessageState
	pendingPrompts      map[string]clientui.TranscriptPendingSessionPrompt
	inFlightTools       map[string]clientui.TranscriptToolStart
	sessionStatus       *clientui.TranscriptSessionStatus
	sessionIdentity     *clientui.TranscriptSessionIdentity
	compactionStatus    *clientui.TranscriptCompactionStatus
	contextUsage        *clientui.RuntimeContextUsage
	goalStatus          *clientui.TranscriptGoalStatus
	backgrounds         map[string]clientui.TranscriptBackgroundActivity
}

func newSessionFeedSequencer(broker *transcriptSubscriptionBroker) *sessionFeedSequencer {
	return &sessionFeedSequencer{
		broker: broker,
		snapshot: sessionFeedSnapshot{
			queuedMessages: make(map[string]clientui.TranscriptQueuedOrSteeredMessageState),
			pendingPrompts: make(map[string]clientui.TranscriptPendingSessionPrompt),
			inFlightTools:  make(map[string]clientui.TranscriptToolStart),
			backgrounds:    make(map[string]clientui.TranscriptBackgroundActivity),
		},
	}
}

func (s *sessionFeedSequencer) HasSubscribers() bool {
	return s != nil && s.broker != nil && s.broker.SubscriberCount() > 0
}

func (s *sessionFeedSequencer) Subscribe(base clientui.TranscriptHydration) (*transcriptSubscription, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hydration := base
	s.snapshot.applyToHydration(&hydration)
	return s.broker.Subscribe(clientui.TranscriptMessage{Kind: clientui.TranscriptMessageHydration, Hydration: &hydration})
}

func (s *sessionFeedSequencer) Publish(messages []clientui.TranscriptMessage) {
	if s == nil || len(messages) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	projected := make([]clientui.TranscriptMessage, 0, len(messages))
	for _, message := range messages {
		if message.Kind == "" {
			continue
		}
		if s.snapshot.shouldDrop(message) {
			continue
		}
		if message.Kind == clientui.TranscriptMessageSessionStatus && message.SessionStatus != nil && s.snapshot.sessionStatus != nil && reflect.DeepEqual(*message.SessionStatus, *s.snapshot.sessionStatus) {
			continue
		}
		s.snapshot.apply(message)
		projected = append(projected, message)
	}
	if len(projected) > 0 {
		s.broker.Publish(projected)
	}
}

func (s sessionFeedSnapshot) applyToHydration(hydration *clientui.TranscriptHydration) {
	if hydration == nil {
		return
	}
	hydration.RunState = cloneRunState(s.runState)
	hydration.RuntimeActivity = cloneRuntimeActivity(s.runtimeActivity)
	hydration.InputReconciliation = cloneInputReconciliation(s.inputReconciliation)
	if s.sessionStatus != nil {
		hydration.SessionStatus = *s.sessionStatus
	}
	if s.sessionIdentity != nil {
		hydration.SessionIdentity = *s.sessionIdentity
	}
	hydration.CompactionStatus = cloneCompactionStatus(s.compactionStatus)
	hydration.ContextUsage = cloneRuntimeContextUsage(s.contextUsage)
	hydration.GoalStatus = cloneGoalStatus(s.goalStatus)
	if len(s.inFlightTools) > 0 {
		merged := make(map[string]clientui.TranscriptToolStart, len(hydration.InFlightTools)+len(s.inFlightTools))
		for _, tool := range hydration.InFlightTools {
			if tool.ToolCallID == "" {
				continue
			}
			merged[tool.ToolCallID] = tool
		}
		for _, tool := range s.inFlightTools {
			if tool.ToolCallID == "" {
				continue
			}
			merged[tool.ToolCallID] = tool
		}
		hydration.InFlightTools = make([]clientui.TranscriptToolStart, 0, len(merged))
		for _, tool := range merged {
			hydration.InFlightTools = append(hydration.InFlightTools, tool)
		}
	}
	if len(s.backgrounds) > 0 {
		hydration.BackgroundActivities = make([]clientui.TranscriptBackgroundActivity, 0, len(s.backgrounds))
		for _, background := range s.backgrounds {
			hydration.BackgroundActivities = append(hydration.BackgroundActivities, background)
		}
	}
	if len(s.queuedMessages) > 0 {
		hydration.QueuedOrSteeredMessages = make([]clientui.TranscriptQueuedOrSteeredMessageState, 0, len(s.queuedMessages))
		for _, queued := range s.queuedMessages {
			hydration.QueuedOrSteeredMessages = append(hydration.QueuedOrSteeredMessages, queued)
		}
	}
	if len(s.pendingPrompts) > 0 {
		hydration.PendingSessionPrompts = make([]clientui.TranscriptPendingSessionPrompt, 0, len(s.pendingPrompts))
		for _, prompt := range s.pendingPrompts {
			if prompt.State == clientui.TranscriptPromptPending {
				hydration.PendingSessionPrompts = append(hydration.PendingSessionPrompts, prompt)
			}
		}
	}
}

func (s *sessionFeedSnapshot) apply(message clientui.TranscriptMessage) {
	switch message.Kind {
	case clientui.TranscriptMessageToolStart:
		if message.ToolStart == nil || message.ToolStart.ToolCallID == "" {
			return
		}
		if s.inFlightTools == nil {
			s.inFlightTools = make(map[string]clientui.TranscriptToolStart)
		}
		s.inFlightTools[message.ToolStart.ToolCallID] = *message.ToolStart
	case clientui.TranscriptMessageToolAbort:
		if message.ToolAbort == nil || message.ToolAbort.ToolCallID == "" {
			return
		}
		delete(s.inFlightTools, message.ToolAbort.ToolCallID)
	case clientui.TranscriptMessageCommittedRow:
		if message.CommittedRow == nil || message.CommittedRow.Tool == nil || message.CommittedRow.Tool.ToolCallID == "" {
			return
		}
		delete(s.inFlightTools, message.CommittedRow.Tool.ToolCallID)
	case clientui.TranscriptMessageRunState:
		s.runState = cloneRunState(message.RunState)
	case clientui.TranscriptMessageRuntimeActivity:
		s.runtimeActivity = cloneRuntimeActivity(message.RuntimeActivity)
	case clientui.TranscriptMessageInputReconciliation:
		s.inputReconciliation = cloneInputReconciliation(message.InputReconciliation)
	case clientui.TranscriptMessageQueuedOrSteeredMessageState:
		if message.QueuedOrSteeredMessageState == nil {
			return
		}
		if s.queuedMessages == nil {
			s.queuedMessages = make(map[string]clientui.TranscriptQueuedOrSteeredMessageState)
		}
		key := message.QueuedOrSteeredMessageState.QueueItemID
		if key == "" {
			key = message.QueuedOrSteeredMessageState.ClientRequestID
		}
		if key == "" {
			return
		}
		if !transcriptQueueStateHydrates(message.QueuedOrSteeredMessageState.Status) {
			delete(s.queuedMessages, key)
			return
		}
		s.queuedMessages[key] = *message.QueuedOrSteeredMessageState
	case clientui.TranscriptMessagePendingSessionPrompt:
		if message.PendingSessionPrompt == nil || message.PendingSessionPrompt.ID == "" {
			return
		}
		if s.pendingPrompts == nil {
			s.pendingPrompts = make(map[string]clientui.TranscriptPendingSessionPrompt)
		}
		if message.PendingSessionPrompt.State == clientui.TranscriptPromptResolved {
			delete(s.pendingPrompts, message.PendingSessionPrompt.ID)
			return
		}
		s.pendingPrompts[message.PendingSessionPrompt.ID] = *message.PendingSessionPrompt
	case clientui.TranscriptMessageSessionStatus:
		if message.SessionStatus == nil {
			return
		}
		copied := *message.SessionStatus
		s.sessionStatus = &copied
	case clientui.TranscriptMessageSessionIdentity:
		if message.SessionIdentity == nil {
			return
		}
		copied := *message.SessionIdentity
		s.sessionIdentity = &copied
	case clientui.TranscriptMessageCompactionStatus:
		s.compactionStatus = cloneCompactionStatus(message.CompactionStatus)
	case clientui.TranscriptMessageContextUsage:
		s.contextUsage = cloneRuntimeContextUsage(message.ContextUsage)
	case clientui.TranscriptMessageGoalStatus:
		s.goalStatus = cloneGoalStatus(message.GoalStatus)
	case clientui.TranscriptMessageBackgroundActivity:
		if message.BackgroundActivity == nil || message.BackgroundActivity.ID == "" {
			return
		}
		if s.backgrounds == nil {
			s.backgrounds = make(map[string]clientui.TranscriptBackgroundActivity)
		}
		if message.BackgroundActivity.Removed > 0 {
			delete(s.backgrounds, message.BackgroundActivity.ID)
			return
		}
		s.backgrounds[message.BackgroundActivity.ID] = *message.BackgroundActivity
	}
}

func transcriptQueueStateHydrates(status clientui.QueuedUserMessageStatus) bool {
	return status == clientui.QueuedUserMessageAccepted
}

func (s *sessionFeedSnapshot) shouldDrop(message clientui.TranscriptMessage) bool {
	switch message.Kind {
	case clientui.TranscriptMessageToolStart:
		if message.ToolStart == nil || message.ToolStart.ToolCallID == "" {
			return true
		}
		existing, ok := s.inFlightTools[message.ToolStart.ToolCallID]
		return ok && reflect.DeepEqual(existing, *message.ToolStart)
	default:
		return false
	}
}

func cloneRunState(value *clientui.RunState) *clientui.RunState {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneRuntimeActivity(value *clientui.RuntimeActivity) *clientui.RuntimeActivity {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneInputReconciliation(value *clientui.RuntimeInputReconciliationSnapshot) *clientui.RuntimeInputReconciliationSnapshot {
	if value == nil {
		return nil
	}
	copied := *value
	if value.Operations != nil {
		copied.Operations = append([]clientui.RuntimeInputReconciliation(nil), value.Operations...)
	}
	return &copied
}

func cloneCompactionStatus(value *clientui.TranscriptCompactionStatus) *clientui.TranscriptCompactionStatus {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneRuntimeContextUsage(value *clientui.RuntimeContextUsage) *clientui.RuntimeContextUsage {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneGoalStatus(value *clientui.TranscriptGoalStatus) *clientui.TranscriptGoalStatus {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
