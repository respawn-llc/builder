package registry

import (
	"fmt"
	"reflect"
	"sync"

	"core/server/runtimefeed"
	"core/shared/clientui"
)

type sessionFeedSequencer struct {
	mu       sync.Mutex
	broker   *transcriptSubscriptionBroker
	snapshot sessionFeedSnapshot
}

type sessionFeedSnapshot struct {
	runState         *clientui.RunState
	runtimeReadModel *runtimefeed.RuntimeReadModelUpdate
	queuedMessages   queuedMessageStateLedger
	pendingPrompts   orderedFeedLedger[string, clientui.TranscriptPendingSessionPrompt]
	inFlightTools    orderedFeedLedger[string, clientui.TranscriptToolStart]
	sessionStatus    *clientui.TranscriptSessionStatus
	sessionIdentity  *clientui.TranscriptSessionIdentity
	compactionStatus *clientui.TranscriptCompactionStatus
	contextUsage     *clientui.RuntimeContextUsage
	goalStatus       *clientui.TranscriptGoalStatus
	backgrounds      orderedFeedLedger[string, clientui.TranscriptBackgroundActivity]
}

func newSessionFeedSequencer(broker *transcriptSubscriptionBroker) *sessionFeedSequencer {
	return &sessionFeedSequencer{broker: broker}
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

func (s *sessionFeedSequencer) PublishRuntimeReadModel(update runtimefeed.RuntimeReadModelUpdate) {
	if s == nil {
		return
	}
	message := runtimefeed.TranscriptMessage{
		Kind: runtimefeed.TranscriptMessageRuntimeReadModelUpdate,
		Payload: runtimefeed.TranscriptPayload{
			RuntimeReadModelUpdate: &update,
		},
	}
	if err := message.ValidatePayload(); err != nil {
		panic(fmt.Sprintf("publish invalid canonical runtime read-model update: %+v: %v", update, err))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := cloneRuntimeReadModelUpdate(update)
	s.snapshot.runtimeReadModel = &copied
	s.broker.Publish(projectProtocol59TranscriptReadModel(copied).messages())
}

func (s sessionFeedSnapshot) applyToHydration(hydration *clientui.TranscriptHydration) {
	if hydration == nil {
		return
	}
	hydration.RunState = cloneRunState(s.runState)
	if s.runtimeReadModel != nil {
		projectProtocol59TranscriptReadModel(*s.runtimeReadModel).applyToHydration(hydration)
	}
	if s.sessionStatus != nil {
		hydration.SessionStatus = *s.sessionStatus
	}
	if s.sessionIdentity != nil {
		hydration.SessionIdentity = *s.sessionIdentity
	}
	hydration.CompactionStatus = cloneCompactionStatus(s.compactionStatus)
	hydration.ContextUsage = cloneRuntimeContextUsage(s.contextUsage)
	hydration.GoalStatus = cloneGoalStatus(s.goalStatus)
	if s.inFlightTools.len() > 0 {
		hydration.InFlightTools = s.inFlightTools.values()
	}
	if s.backgrounds.len() > 0 {
		hydration.BackgroundActivities = s.backgrounds.values()
	}
	if queuedMessages := s.queuedMessages.values(); len(queuedMessages) > 0 {
		hydration.QueuedOrSteeredMessages = queuedMessages
	}
	if s.pendingPrompts.len() > 0 {
		hydration.PendingSessionPrompts = make([]clientui.TranscriptPendingSessionPrompt, 0, s.pendingPrompts.len())
		for _, prompt := range s.pendingPrompts.values() {
			if prompt.State == clientui.TranscriptPromptPending {
				hydration.PendingSessionPrompts = append(hydration.PendingSessionPrompts, prompt)
			}
		}
	}
}

func cloneRuntimeReadModelUpdate(value runtimefeed.RuntimeReadModelUpdate) runtimefeed.RuntimeReadModelUpdate {
	copied := value
	if value.Activity.ActiveStep != nil {
		activeStep := *value.Activity.ActiveStep
		copied.Activity.ActiveStep = &activeStep
	}
	if value.InputReconciliation.Operations != nil {
		copied.InputReconciliation.Operations = make([]runtimefeed.RuntimeInputReconciliation, len(value.InputReconciliation.Operations))
		for index, operation := range value.InputReconciliation.Operations {
			copied.InputReconciliation.Operations[index] = operation
			if operation.Operation.QueueItemID != nil {
				queueItemID := *operation.Operation.QueueItemID
				copied.InputReconciliation.Operations[index].Operation.QueueItemID = &queueItemID
			}
		}
	}
	return copied
}

func (s *sessionFeedSnapshot) apply(message clientui.TranscriptMessage) {
	switch message.Kind {
	case clientui.TranscriptMessageToolStart:
		if message.ToolStart == nil || message.ToolStart.ToolCallID == "" {
			return
		}
		s.inFlightTools.upsert(message.ToolStart.ToolCallID, *message.ToolStart)
	case clientui.TranscriptMessageToolAbort:
		if message.ToolAbort == nil || message.ToolAbort.ToolCallID == "" {
			return
		}
		s.inFlightTools.delete(message.ToolAbort.ToolCallID)
	case clientui.TranscriptMessageCommittedRow:
		if message.CommittedRow == nil || message.CommittedRow.Tool == nil || message.CommittedRow.Tool.ToolCallID == "" {
			return
		}
		s.inFlightTools.delete(message.CommittedRow.Tool.ToolCallID)
	case clientui.TranscriptMessageRunState:
		s.runState = cloneRunState(message.RunState)
	case clientui.TranscriptMessageQueuedOrSteeredMessageState:
		if message.QueuedOrSteeredMessageState == nil {
			return
		}
		state := *message.QueuedOrSteeredMessageState
		if state.QueueItemID == "" && state.ClientRequestID == "" {
			return
		}
		s.queuedMessages.apply(state)
	case clientui.TranscriptMessagePendingSessionPrompt:
		if message.PendingSessionPrompt == nil || message.PendingSessionPrompt.ID == "" {
			return
		}
		if message.PendingSessionPrompt.State == clientui.TranscriptPromptResolved {
			s.pendingPrompts.delete(message.PendingSessionPrompt.ID)
			return
		}
		s.pendingPrompts.upsert(message.PendingSessionPrompt.ID, *message.PendingSessionPrompt)
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
		if message.BackgroundActivity.Removed {
			s.backgrounds.delete(message.BackgroundActivity.ID)
			return
		}
		s.backgrounds.upsert(message.BackgroundActivity.ID, *message.BackgroundActivity)
	}
}

func transcriptQueueStateHydrates(status clientui.QueuedUserMessageStatus) bool {
	return status == clientui.QueuedUserMessageAccepted
}

type queuedMessageStateLedger struct {
	entries           orderedFeedList[clientui.TranscriptQueuedOrSteeredMessageState]
	byQueueItemID     map[string]*orderedFeedNode[clientui.TranscriptQueuedOrSteeredMessageState]
	byClientRequestID map[string]*orderedFeedNode[clientui.TranscriptQueuedOrSteeredMessageState]
}

func (l *queuedMessageStateLedger) apply(state clientui.TranscriptQueuedOrSteeredMessageState) {
	node := l.find(state)
	if !transcriptQueueStateHydrates(state.Status) {
		if node != nil {
			l.remove(node)
		}
		return
	}
	if node != nil {
		l.unindex(node)
		node.value = state
		l.index(node)
		return
	}
	node = l.entries.append(state)
	l.index(node)
}

func (l *queuedMessageStateLedger) values() []clientui.TranscriptQueuedOrSteeredMessageState {
	return l.entries.values()
}

func (l *queuedMessageStateLedger) find(state clientui.TranscriptQueuedOrSteeredMessageState) *orderedFeedNode[clientui.TranscriptQueuedOrSteeredMessageState] {
	if state.QueueItemID != "" {
		if node := l.byQueueItemID[state.QueueItemID]; node != nil {
			return node
		}
	}
	if state.ClientRequestID == "" {
		return nil
	}
	node := l.byClientRequestID[state.ClientRequestID]
	if node == nil {
		return nil
	}
	if state.QueueItemID != "" && node.value.QueueItemID != "" && state.QueueItemID != node.value.QueueItemID {
		panic(fmt.Sprintf(
			"queued transcript state identity conflict: client_request_id=%q existing_queue_item_id=%q incoming_queue_item_id=%q",
			state.ClientRequestID,
			node.value.QueueItemID,
			state.QueueItemID,
		))
	}
	return node
}

func (l *queuedMessageStateLedger) index(node *orderedFeedNode[clientui.TranscriptQueuedOrSteeredMessageState]) {
	if l.byQueueItemID == nil {
		l.byQueueItemID = make(map[string]*orderedFeedNode[clientui.TranscriptQueuedOrSteeredMessageState])
	}
	if l.byClientRequestID == nil {
		l.byClientRequestID = make(map[string]*orderedFeedNode[clientui.TranscriptQueuedOrSteeredMessageState])
	}
	if node.value.QueueItemID != "" {
		if existing := l.byQueueItemID[node.value.QueueItemID]; existing != nil && existing != node {
			panic(fmt.Sprintf("duplicate queued transcript queue_item_id=%q", node.value.QueueItemID))
		}
		l.byQueueItemID[node.value.QueueItemID] = node
	}
	if node.value.ClientRequestID != "" {
		if existing := l.byClientRequestID[node.value.ClientRequestID]; existing != nil && existing != node {
			panic(fmt.Sprintf("duplicate queued transcript client_request_id=%q", node.value.ClientRequestID))
		}
		l.byClientRequestID[node.value.ClientRequestID] = node
	}
}

func (l *queuedMessageStateLedger) unindex(node *orderedFeedNode[clientui.TranscriptQueuedOrSteeredMessageState]) {
	if node.value.QueueItemID != "" && l.byQueueItemID[node.value.QueueItemID] == node {
		delete(l.byQueueItemID, node.value.QueueItemID)
	}
	if node.value.ClientRequestID != "" && l.byClientRequestID[node.value.ClientRequestID] == node {
		delete(l.byClientRequestID, node.value.ClientRequestID)
	}
}

func (l *queuedMessageStateLedger) remove(node *orderedFeedNode[clientui.TranscriptQueuedOrSteeredMessageState]) {
	l.unindex(node)
	l.entries.remove(node)
}

func (s *sessionFeedSnapshot) shouldDrop(message clientui.TranscriptMessage) bool {
	switch message.Kind {
	case clientui.TranscriptMessageToolStart:
		if message.ToolStart == nil || message.ToolStart.ToolCallID == "" {
			return true
		}
		existing, ok := s.inFlightTools.get(message.ToolStart.ToolCallID)
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
