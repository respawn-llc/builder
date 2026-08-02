package registry

import (
	"fmt"
	"reflect"
	"sort"
	"sync"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/transcript"
)

type sessionFeedSequencer struct {
	mu       sync.Mutex
	broker   *transcriptSubscriptionBroker
	snapshot sessionFeedSnapshot
}

type sessionFeedSnapshot struct {
	runtimeReadModel *clientui.RuntimeReadModelUpdate
	activeReasoning  *clientui.TranscriptReasoningUpdate
	activeReviewer   *clientui.TranscriptReviewerState
	activeCompaction *clientui.TranscriptCompactionStatus
	queuedMessages   queuedMessageStateLedger
	pendingPrompts   orderedFeedLedger[clientui.PromptID, clientui.TranscriptPrompt]
	inFlightTools    orderedFeedLedger[clientui.ToolCallID, clientui.TranscriptToolStart]
	sessionStatus    *clientui.TranscriptSessionStatus
	sessionIdentity  *clientui.TranscriptSessionIdentity
	contextUsage     *clientui.TranscriptContextUsage
	goalStatus       *clientui.TranscriptGoalStatus
	backgrounds      orderedFeedLedger[runtimeids.BackgroundActivityID, clientui.TranscriptBackgroundActivity]
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
	if err := s.snapshot.applyToHydration(&hydration); err != nil {
		return nil, err
	}
	event := clientui.NewTranscriptEvent(hydration)
	if err := event.Validate(); err != nil {
		return nil, fmt.Errorf("build canonical transcript hydration: %w", err)
	}
	return s.broker.Subscribe(event)
}

func (s *sessionFeedSequencer) Publish(events []clientui.TranscriptEvent) {
	if s == nil || len(events) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range events {
		if err := event.Validate(); err != nil {
			panic(fmt.Sprintf("publish invalid canonical transcript event before batch mutation: %v", err))
		}
	}
	published := make([]clientui.TranscriptEvent, 0, len(events))
	for _, event := range events {
		if s.snapshot.shouldDrop(event) {
			continue
		}
		s.snapshot.apply(event)
		published = append(published, event)
	}
	if len(published) > 0 {
		s.broker.Publish(published)
	}
}

func (s *sessionFeedSequencer) PublishRuntimeReadModel(update clientui.RuntimeReadModelUpdate) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishRuntimeReadModelLocked(update)
}

func (s *sessionFeedSequencer) CloseWithRuntimeReadModel(update clientui.RuntimeReadModelUpdate, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishRuntimeReadModelLocked(update)
	s.broker.Close(err)
}

func (s *sessionFeedSequencer) Close(err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broker.Close(err)
}

func (s *sessionFeedSequencer) publishRuntimeReadModelLocked(update clientui.RuntimeReadModelUpdate) {
	copied := s.snapshot.applyRuntimeReadModel(update)
	event := clientui.NewTranscriptEvent(copied)
	if err := event.Validate(); err != nil {
		panic(fmt.Sprintf("publish invalid canonical runtime read-model update: %+v: %v", update, err))
	}
	s.broker.Publish([]clientui.TranscriptEvent{event})
}

func (s sessionFeedSnapshot) applyToHydration(hydration *clientui.TranscriptHydration) error {
	if hydration == nil {
		return fmt.Errorf("canonical transcript hydration is required")
	}
	if s.runtimeReadModel == nil {
		return fmt.Errorf("canonical transcript hydration is missing runtime read-model state")
	}
	hydration.RuntimeReadModelUpdate = cloneRuntimeReadModelUpdate(*s.runtimeReadModel)
	if s.sessionStatus != nil {
		hydration.SessionStatus = cloneTranscriptSessionStatus(*s.sessionStatus)
	}
	if s.sessionIdentity != nil {
		hydration.SessionIdentity = cloneTranscriptSessionIdentity(*s.sessionIdentity)
	}
	hydration.ActiveReasoning = cloneTranscriptReasoningUpdate(s.activeReasoning)
	hydration.ActiveStep = transcriptActiveStepFromRuntimeReadModel(*s.runtimeReadModel)
	hydration.ActiveReviewer = cloneTranscriptReviewerState(s.activeReviewer)
	hydration.ActiveCompaction = cloneTranscriptCompactionStatus(s.activeCompaction)
	if s.inFlightTools.len() > 0 {
		hydration.InFlightTools = cloneTranscriptToolStarts(s.inFlightTools.values())
	}
	if s.backgrounds.len() > 0 {
		hydration.BackgroundActivities = cloneTranscriptBackgroundActivities(s.backgrounds.values())
	}
	if queuedMessages := s.queuedMessages.values(); len(queuedMessages) > 0 {
		hydration.QueuedMessages = cloneTranscriptQueuedMessageStates(queuedMessages)
	}
	if s.pendingPrompts.len() > 0 {
		prompts := cloneTranscriptPrompts(s.pendingPrompts.values())
		sort.Slice(prompts, func(i, j int) bool {
			return pendingPromptOrderLess(
				prompts[i].CreatedAt,
				string(prompts[i].PromptID),
				prompts[j].CreatedAt,
				string(prompts[j].PromptID),
			)
		})
		hydration.PendingPrompts = prompts
	}
	hydration.ContextUsage = cloneTranscriptContextUsage(s.contextUsage)
	hydration.GoalStatus = cloneTranscriptGoalStatus(s.goalStatus)
	return nil
}

func cloneRuntimeReadModelUpdate(value clientui.RuntimeReadModelUpdate) clientui.RuntimeReadModelUpdate {
	copied := value
	if value.Activity.ActiveStep != nil {
		activeStep := *value.Activity.ActiveStep
		copied.Activity.ActiveStep = &activeStep
	}
	if value.InputReconciliation.Operations != nil {
		copied.InputReconciliation.Operations = make([]clientui.RuntimeInputReconciliation, len(value.InputReconciliation.Operations))
		for index, operation := range value.InputReconciliation.Operations {
			copied.InputReconciliation.Operations[index] = operation
			copied.InputReconciliation.Operations[index].Operation.QueueItemID = textutil.Pointer(operation.Operation.QueueItemID)
		}
	}
	return copied
}

func transcriptActiveStepFromRuntimeReadModel(update clientui.RuntimeReadModelUpdate) *clientui.TranscriptStepState {
	active := update.Activity.ActiveStep
	if active == nil {
		return nil
	}
	return &clientui.TranscriptStepState{
		RunID:      active.RunID,
		StepID:     active.StepID,
		Lifecycle:  clientui.StepLifecycleStarted,
		ActiveKind: active.ActiveKind,
		Status:     clientui.RunStatusRunning,
	}
}

func (s *sessionFeedSnapshot) applyRuntimeReadModel(update clientui.RuntimeReadModelUpdate) clientui.RuntimeReadModelUpdate {
	copied := cloneRuntimeReadModelUpdate(update)
	s.reconcileStepOwnedState(copied.Activity.ActiveStep)
	s.runtimeReadModel = &copied
	return copied
}

func (s *sessionFeedSnapshot) reconcileStepOwnedState(next *clientui.RuntimeActiveStep) {
	var current *clientui.RuntimeActiveStep
	if s.runtimeReadModel != nil {
		current = s.runtimeReadModel.Activity.ActiveStep
	}
	if current != nil &&
		next != nil &&
		current.RunID == next.RunID &&
		current.StepID == next.StepID &&
		current.ActiveKind == next.ActiveKind {
		return
	}
	s.activeReasoning = nil
	s.activeReviewer = nil
	s.activeCompaction = nil
	s.pendingPrompts = orderedFeedLedger[clientui.PromptID, clientui.TranscriptPrompt]{}
	s.inFlightTools = orderedFeedLedger[clientui.ToolCallID, clientui.TranscriptToolStart]{}
}

func (s *sessionFeedSnapshot) apply(event clientui.TranscriptEvent) {
	switch event.Kind() {
	case clientui.TranscriptMessageReasoningUpdate:
		payload := event.Payload().(clientui.TranscriptReasoningUpdate)
		s.activeReasoning = cloneTranscriptReasoningUpdate(&payload)
	case clientui.TranscriptMessageReasoningReset:
		s.activeReasoning = nil
	case clientui.TranscriptMessageToolStart:
		tool := event.Payload().(clientui.TranscriptToolStart)
		s.inFlightTools.upsert(tool.ToolCallID, tool)
	case clientui.TranscriptMessageToolAbort:
		s.inFlightTools.delete(event.Payload().(clientui.TranscriptToolAbort).ToolCallID)
	case clientui.TranscriptMessageCommittedRow:
		payload := event.Payload().(clientui.TranscriptCommittedRow)
		if payload.Tool != nil {
			s.inFlightTools.delete(payload.Tool.ToolCallID)
		}
	case clientui.TranscriptMessageReviewerState:
		payload := event.Payload().(clientui.TranscriptReviewerState)
		if payload.State == clientui.ReviewerStateRunning {
			s.activeReviewer = cloneTranscriptReviewerState(&payload)
		} else {
			s.activeReviewer = nil
		}
	case clientui.TranscriptMessageQueuedMessageState:
		payload := event.Payload().(clientui.TranscriptQueuedMessageState)
		s.queuedMessages.apply(payload)
	case clientui.TranscriptMessagePrompt:
		payload := event.Payload().(clientui.TranscriptPrompt)
		if payload.Status == clientui.TranscriptPromptStatusPending {
			prompt := cloneTranscriptPrompt(payload)
			s.pendingPrompts.upsert(prompt.PromptID, prompt)
		} else {
			s.pendingPrompts.delete(payload.PromptID)
		}
	case clientui.TranscriptMessageRuntimeReadModelUpdate:
		s.applyRuntimeReadModel(event.Payload().(clientui.RuntimeReadModelUpdate))
	case clientui.TranscriptMessageSessionStatus:
		payload := event.Payload().(clientui.TranscriptSessionStatus)
		status := cloneTranscriptSessionStatus(payload)
		s.sessionStatus = &status
	case clientui.TranscriptMessageSessionIdentity:
		payload := event.Payload().(clientui.TranscriptSessionIdentity)
		identity := cloneTranscriptSessionIdentity(payload)
		s.sessionIdentity = &identity
	case clientui.TranscriptMessageCompactionStatus:
		payload := event.Payload().(clientui.TranscriptCompactionStatus)
		if payload.State == clientui.CompactionStarted {
			s.activeCompaction = cloneTranscriptCompactionStatus(&payload)
		} else {
			s.activeCompaction = nil
		}
	case clientui.TranscriptMessageContextUsage:
		payload := event.Payload().(clientui.TranscriptContextUsage)
		s.contextUsage = cloneTranscriptContextUsage(&payload)
	case clientui.TranscriptMessageGoalStatus:
		payload := event.Payload().(clientui.TranscriptGoalStatus)
		s.goalStatus = cloneTranscriptGoalStatus(&payload)
	case clientui.TranscriptMessageBackgroundActivity:
		background := event.Payload().(clientui.TranscriptBackgroundActivity)
		if background.Lifecycle == clientui.BackgroundLifecycleBackgrounded {
			s.backgrounds.upsert(background.ActivityID, background)
		} else {
			s.backgrounds.delete(background.ActivityID)
		}
	}
}

func (s *sessionFeedSnapshot) shouldDrop(event clientui.TranscriptEvent) bool {
	switch event.Kind() {
	case clientui.TranscriptMessageToolStart:
		tool := event.Payload().(clientui.TranscriptToolStart)
		existing, ok := s.inFlightTools.get(tool.ToolCallID)
		return ok && reflect.DeepEqual(existing, tool)
	case clientui.TranscriptMessageSessionStatus:
		payload := event.Payload().(clientui.TranscriptSessionStatus)
		return s.sessionStatus != nil && reflect.DeepEqual(*s.sessionStatus, payload)
	case clientui.TranscriptMessageSessionIdentity:
		payload := event.Payload().(clientui.TranscriptSessionIdentity)
		return s.sessionIdentity != nil && reflect.DeepEqual(*s.sessionIdentity, payload)
	default:
		return false
	}
}

func transcriptQueueStateHydrates(status clientui.QueuedUserMessageStatus) bool {
	return status == clientui.QueuedUserMessageAccepted
}

type queuedMessageStateLedger struct {
	entries           orderedFeedList[clientui.TranscriptQueuedMessageState]
	byQueueItemID     map[runtimeids.QueueItemID]*orderedFeedNode[clientui.TranscriptQueuedMessageState]
	byClientRequestID map[runtimeids.RuntimeClientRequestID]*orderedFeedNode[clientui.TranscriptQueuedMessageState]
}

func (l *queuedMessageStateLedger) apply(state clientui.TranscriptQueuedMessageState) {
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

func (l *queuedMessageStateLedger) values() []clientui.TranscriptQueuedMessageState {
	return l.entries.values()
}

func (l *queuedMessageStateLedger) find(state clientui.TranscriptQueuedMessageState) *orderedFeedNode[clientui.TranscriptQueuedMessageState] {
	if node := l.byQueueItemID[state.QueueItemID]; node != nil {
		return node
	}
	node := l.byClientRequestID[state.ClientRequestID]
	if node == nil {
		return nil
	}
	if node.value.QueueItemID != state.QueueItemID {
		panic(fmt.Sprintf(
			"queued transcript state identity conflict: client_request_id=%q existing_queue_item_id=%q incoming_queue_item_id=%q",
			state.ClientRequestID.String(),
			node.value.QueueItemID.String(),
			state.QueueItemID.String(),
		))
	}
	return node
}

func (l *queuedMessageStateLedger) index(node *orderedFeedNode[clientui.TranscriptQueuedMessageState]) {
	if l.byQueueItemID == nil {
		l.byQueueItemID = make(map[runtimeids.QueueItemID]*orderedFeedNode[clientui.TranscriptQueuedMessageState])
	}
	if l.byClientRequestID == nil {
		l.byClientRequestID = make(map[runtimeids.RuntimeClientRequestID]*orderedFeedNode[clientui.TranscriptQueuedMessageState])
	}
	if existing := l.byQueueItemID[node.value.QueueItemID]; existing != nil && existing != node {
		panic(fmt.Sprintf("duplicate queued transcript queue_item_id=%q", node.value.QueueItemID.String()))
	}
	if existing := l.byClientRequestID[node.value.ClientRequestID]; existing != nil && existing != node {
		panic(fmt.Sprintf("duplicate queued transcript client_request_id=%q", node.value.ClientRequestID.String()))
	}
	l.byQueueItemID[node.value.QueueItemID] = node
	l.byClientRequestID[node.value.ClientRequestID] = node
}

func (l *queuedMessageStateLedger) unindex(node *orderedFeedNode[clientui.TranscriptQueuedMessageState]) {
	if l.byQueueItemID[node.value.QueueItemID] == node {
		delete(l.byQueueItemID, node.value.QueueItemID)
	}
	if l.byClientRequestID[node.value.ClientRequestID] == node {
		delete(l.byClientRequestID, node.value.ClientRequestID)
	}
}

func (l *queuedMessageStateLedger) remove(node *orderedFeedNode[clientui.TranscriptQueuedMessageState]) {
	l.unindex(node)
	l.entries.remove(node)
}

func cloneTranscriptReasoningUpdate(value *clientui.TranscriptReasoningUpdate) *clientui.TranscriptReasoningUpdate {
	if value == nil {
		return nil
	}
	copied := *value
	if value.CurrentStatus != nil {
		status := *value.CurrentStatus
		copied.CurrentStatus = &status
	}
	return &copied
}

func cloneTranscriptReviewerState(value *clientui.TranscriptReviewerState) *clientui.TranscriptReviewerState {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneTranscriptCompactionStatus(value *clientui.TranscriptCompactionStatus) *clientui.TranscriptCompactionStatus {
	if value == nil {
		return nil
	}
	copied := *value
	if value.Diagnostic != nil {
		diagnostic := *value.Diagnostic
		copied.Diagnostic = &diagnostic
	}
	return &copied
}

func cloneTranscriptContextUsage(value *clientui.TranscriptContextUsage) *clientui.TranscriptContextUsage {
	if value == nil {
		return nil
	}
	copied := *value
	copied.CacheHitPercent = textutil.Pointer(value.CacheHitPercent)
	return &copied
}

func cloneTranscriptGoalStatus(value *clientui.TranscriptGoalStatus) *clientui.TranscriptGoalStatus {
	if value == nil {
		return nil
	}
	copied := *value
	if value.Goal != nil {
		goal := *value.Goal
		copied.Goal = &goal
	}
	return &copied
}

func cloneTranscriptSessionStatus(value clientui.TranscriptSessionStatus) clientui.TranscriptSessionStatus {
	copied := value
	copied.PreviousSessionID = textutil.Pointer(value.PreviousSessionID)
	copied.ParentAgentSessionID = textutil.Pointer(value.ParentAgentSessionID)
	copied.NavigationTargetSessionID = textutil.Pointer(value.NavigationTargetSessionID)
	if value.Workflow != nil {
		workflow := *value.Workflow
		copied.Workflow = &workflow
	}
	return copied
}

func cloneTranscriptSessionIdentity(value clientui.TranscriptSessionIdentity) clientui.TranscriptSessionIdentity {
	copied := value
	copied.SessionName = textutil.Pointer(value.SessionName)
	if value.ExecutionTarget != nil {
		target := clientui.NormalizeSessionExecutionTarget(*value.ExecutionTarget)
		copied.ExecutionTarget = &target
	}
	return copied
}

func cloneTranscriptPrompt(value clientui.TranscriptPrompt) clientui.TranscriptPrompt {
	copied := value
	copied.Suggestions = append([]string(nil), value.Suggestions...)
	copied.RecommendedOptionIndex = textutil.Pointer(value.RecommendedOptionIndex)
	copied.ApprovalOptions = append([]clientui.ApprovalDecision(nil), value.ApprovalOptions...)
	if value.Tool != nil {
		tool := *value.Tool
		copied.Tool = &tool
	}
	return copied
}

func cloneTranscriptPrompts(values []clientui.TranscriptPrompt) []clientui.TranscriptPrompt {
	out := make([]clientui.TranscriptPrompt, len(values))
	for index, value := range values {
		out[index] = cloneTranscriptPrompt(value)
	}
	return out
}

func cloneTranscriptToolStarts(values []clientui.TranscriptToolStart) []clientui.TranscriptToolStart {
	out := make([]clientui.TranscriptToolStart, len(values))
	for index, value := range values {
		out[index] = value
		if value.Presentation != nil {
			presentation := transcript.NormalizeToolCallMeta(*value.Presentation)
			out[index].Presentation = &presentation
		}
	}
	return out
}

func cloneTranscriptQueuedMessageStates(values []clientui.TranscriptQueuedMessageState) []clientui.TranscriptQueuedMessageState {
	out := make([]clientui.TranscriptQueuedMessageState, len(values))
	for index, value := range values {
		out[index] = value
		out[index].FailureReason = textutil.Pointer(value.FailureReason)
		out[index].Text = textutil.Pointer(value.Text)
	}
	return out
}

func cloneTranscriptBackgroundActivities(values []clientui.TranscriptBackgroundActivity) []clientui.TranscriptBackgroundActivity {
	out := make([]clientui.TranscriptBackgroundActivity, len(values))
	for index, value := range values {
		out[index] = value
		out[index].LogPath = textutil.Pointer(value.LogPath)
		out[index].Preview = textutil.Pointer(value.Preview)
		out[index].ExitCode = textutil.Pointer(value.ExitCode)
		if value.Diagnostic != nil {
			diagnostic := *value.Diagnostic
			out[index].Diagnostic = &diagnostic
		}
	}
	return out
}
