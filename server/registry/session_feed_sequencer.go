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
	activeStep       *clientui.TranscriptStepState
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
	message := clientui.TranscriptMessage{
		Kind:    clientui.TranscriptMessageHydration,
		Payload: clientui.TranscriptPayload{Hydration: &hydration},
	}
	if err := message.ValidatePayload(); err != nil {
		return nil, fmt.Errorf("build canonical transcript hydration: %w", err)
	}
	return s.broker.Subscribe(message)
}

func (s *sessionFeedSequencer) Publish(messages []clientui.TranscriptMessage) {
	if s == nil || len(messages) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	published := make([]clientui.TranscriptMessage, 0, len(messages))
	for _, message := range messages {
		if message.Kind == "" {
			continue
		}
		if err := message.ValidatePayload(); err != nil {
			panic(fmt.Sprintf("publish invalid canonical transcript message kind %q: %v", message.Kind, err))
		}
		if s.snapshot.shouldDrop(message) {
			continue
		}
		s.snapshot.apply(message)
		published = append(published, message)
	}
	if len(published) > 0 {
		s.broker.Publish(published)
	}
}

func (s *sessionFeedSequencer) PublishRuntimeReadModel(update clientui.RuntimeReadModelUpdate) {
	if s == nil {
		return
	}
	copied := cloneRuntimeReadModelUpdate(update)
	message := clientui.TranscriptMessage{
		Kind:    clientui.TranscriptMessageRuntimeReadModelUpdate,
		Payload: clientui.TranscriptPayload{RuntimeReadModelUpdate: &copied},
	}
	if err := message.ValidatePayload(); err != nil {
		panic(fmt.Sprintf("publish invalid canonical runtime read-model update: %+v: %v", update, err))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.runtimeReadModel = &copied
	s.broker.Publish([]clientui.TranscriptMessage{message})
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
	hydration.ActiveStep = cloneTranscriptStepState(s.activeStep)
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

func (s *sessionFeedSnapshot) apply(message clientui.TranscriptMessage) {
	payload := message.Payload
	switch message.Kind {
	case clientui.TranscriptMessageReasoningUpdate:
		s.activeReasoning = cloneTranscriptReasoningUpdate(payload.ReasoningUpdate)
	case clientui.TranscriptMessageReasoningReset:
		s.activeReasoning = nil
	case clientui.TranscriptMessageToolStart:
		tool := *payload.ToolStart
		s.inFlightTools.upsert(tool.ToolCallID, tool)
	case clientui.TranscriptMessageToolAbort:
		s.inFlightTools.delete(payload.ToolAbort.ToolCallID)
	case clientui.TranscriptMessageCommittedRow:
		if payload.CommittedRow.Tool != nil {
			s.inFlightTools.delete(payload.CommittedRow.Tool.ToolCallID)
		}
	case clientui.TranscriptMessageStepState:
		s.applyStepState(*payload.StepState)
	case clientui.TranscriptMessageReviewerState:
		if payload.ReviewerState.State == clientui.ReviewerStateRunning {
			s.activeReviewer = cloneTranscriptReviewerState(payload.ReviewerState)
		} else {
			s.activeReviewer = nil
		}
	case clientui.TranscriptMessageQueuedMessageState:
		s.queuedMessages.apply(*payload.QueuedMessageState)
	case clientui.TranscriptMessagePromptPending:
		prompt := cloneTranscriptPrompt(*payload.PromptPending)
		s.pendingPrompts.upsert(prompt.PromptID, prompt)
	case clientui.TranscriptMessagePromptResolved:
		s.pendingPrompts.delete(payload.PromptResolved.PromptID)
	case clientui.TranscriptMessageRuntimeReadModelUpdate:
		update := cloneRuntimeReadModelUpdate(*payload.RuntimeReadModelUpdate)
		s.runtimeReadModel = &update
	case clientui.TranscriptMessageSessionStatus:
		status := cloneTranscriptSessionStatus(*payload.SessionStatus)
		s.sessionStatus = &status
	case clientui.TranscriptMessageSessionIdentity:
		identity := cloneTranscriptSessionIdentity(*payload.SessionIdentity)
		s.sessionIdentity = &identity
	case clientui.TranscriptMessageCompactionStatus:
		if payload.CompactionStatus.State == clientui.CompactionStarted {
			s.activeCompaction = cloneTranscriptCompactionStatus(payload.CompactionStatus)
		} else {
			s.activeCompaction = nil
		}
	case clientui.TranscriptMessageContextUsage:
		s.contextUsage = cloneTranscriptContextUsage(payload.ContextUsage)
	case clientui.TranscriptMessageGoalStatus:
		s.goalStatus = cloneTranscriptGoalStatus(payload.GoalStatus)
	case clientui.TranscriptMessageBackgroundActivity:
		background := *payload.BackgroundActivity
		if background.Lifecycle == clientui.BackgroundLifecycleBackgrounded {
			s.backgrounds.upsert(background.ActivityID, background)
		} else {
			s.backgrounds.delete(background.ActivityID)
		}
	}
}

func (s *sessionFeedSnapshot) applyStepState(state clientui.TranscriptStepState) {
	switch state.Lifecycle {
	case clientui.StepLifecycleStarted:
		copied := state
		s.activeStep = &copied
	case clientui.StepLifecycleFinished:
		if s.activeStep != nil && s.activeStep.StepID != state.StepID {
			panic(fmt.Sprintf(
				"canonical transcript finished step %q while step %q is active",
				state.StepID.String(),
				s.activeStep.StepID.String(),
			))
		}
		s.activeStep = nil
	default:
		panic(fmt.Sprintf("canonical transcript has unknown step lifecycle %q", state.Lifecycle))
	}
}

func (s *sessionFeedSnapshot) shouldDrop(message clientui.TranscriptMessage) bool {
	switch message.Kind {
	case clientui.TranscriptMessageToolStart:
		tool := *message.Payload.ToolStart
		existing, ok := s.inFlightTools.get(tool.ToolCallID)
		return ok && reflect.DeepEqual(existing, tool)
	case clientui.TranscriptMessageSessionStatus:
		return s.sessionStatus != nil && reflect.DeepEqual(*s.sessionStatus, *message.Payload.SessionStatus)
	case clientui.TranscriptMessageSessionIdentity:
		return s.sessionIdentity != nil && reflect.DeepEqual(*s.sessionIdentity, *message.Payload.SessionIdentity)
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

func cloneTranscriptStepState(value *clientui.TranscriptStepState) *clientui.TranscriptStepState {
	if value == nil {
		return nil
	}
	copied := *value
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
	copied.ParentSessionID = textutil.Pointer(value.ParentSessionID)
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
