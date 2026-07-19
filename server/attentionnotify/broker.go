package attentionnotify

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"core/shared/clientui"
	"core/shared/invariant"
	"core/shared/serverapi"
)

const defaultBufferSize = 64

type RoutingKind string

const (
	RoutingWorkflowTask  RoutingKind = "workflow_task"
	RoutingSessionPrompt RoutingKind = "session_prompt"
)

type RoutingScope struct {
	Kind       RoutingKind
	ProjectID  string
	WorkflowID string
	TaskID     string
	SessionID  string
}

type Broker struct {
	mu                     sync.Mutex
	nextID                 uint64
	nextSeq                uint64
	nextSnapshotGeneration uint64
	bufferSize             int
	closed                 bool
	subscribers            map[uint64]*Subscription
	invariants             invariant.Policy
}

type Subscription struct {
	filter  deliveryFilter
	ch      chan attentionEnvelope
	onClose func()
	admit   func(attentionEnvelope) (bool, error)

	mu   sync.Mutex
	err  error
	done bool
}

type SnapshotPendingDescriptor struct {
	Notification clientui.AttentionNotification
	Occurrence   OccurrenceMetadata
}

type SnapshotSubscription struct {
	live                     *Subscription
	snapshot                 []attentionEnvelope
	sessionID                string
	generation               uint64
	openingOrdinaryWatermark OrdinaryOccurrenceWatermark
	observedTaskBatchKey     *TaskQuestionBatchKey
	invariants               invariant.Policy

	nextMu sync.Mutex
	mu     sync.Mutex
	next   int
	done   bool
	err    error
}

type attentionEnvelope struct {
	event      clientui.AttentionNotificationEvent
	occurrence OccurrenceMetadata
}

type AttentionProjectionDiscontinuity struct {
	Diagnostic invariant.Diagnostic
}

func (e AttentionProjectionDiscontinuity) Error() string {
	return fmt.Sprintf("attention projection discontinuity: %v", e.Diagnostic.Fields)
}

func (e AttentionProjectionDiscontinuity) Unwrap() error {
	return serverapi.ErrStreamGap
}

func (e AttentionProjectionDiscontinuity) RequiresSnapshotReopen() bool {
	return true
}

type deliveryFilter struct {
	desktopRoot bool
	sessionID   string
}

type Option func(*Broker)

func WithBufferSize(size int) Option {
	return func(b *Broker) {
		if size > 0 {
			b.bufferSize = size
		}
	}
}

func WithAttentionProjectionInvariantPanic(enabled bool) Option {
	return func(b *Broker) {
		mode := invariant.ModeDiagnostic
		if enabled {
			mode = invariant.ModePanic
		}
		b.invariants = invariant.NewPolicy(invariant.WithMode(mode))
	}
}

func NewBroker(options ...Option) *Broker {
	broker := &Broker{
		bufferSize:  defaultBufferSize,
		subscribers: map[uint64]*Subscription{},
		invariants:  invariant.NewPolicy(),
	}
	for _, option := range options {
		if option != nil {
			option(broker)
		}
	}
	return broker
}

func (b *Broker) SubscribeDesktop() (*Subscription, error) {
	return b.subscribe(deliveryFilter{desktopRoot: true})
}

func (b *Broker) SubscribeSession(sessionID string) (*Subscription, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("attention notification session subscription: %w", serverapi.ErrSessionIDRequired)
	}
	return b.subscribe(deliveryFilter{sessionID: sessionID})
}

func (b *Broker) SubscribeSessionSnapshot(sessionID string, descriptors []SnapshotPendingDescriptor, openingWatermark OrdinaryOccurrenceWatermark) (*SnapshotSubscription, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("attention notification session subscription: %w", serverapi.ErrSessionIDRequired)
	}
	if b == nil {
		return nil, fmt.Errorf("attention notification stream is unavailable: %w", serverapi.ErrStreamUnavailable)
	}
	for index, descriptor := range descriptors {
		event := clientui.AttentionNotificationEvent{
			Source:  clientui.AttentionNotificationSourceSnapshot,
			Type:    clientui.AttentionNotificationEventPending,
			Pending: &descriptor.Notification,
		}
		if err := serverapi.ValidateAttentionNotificationEvent(withSequenceForValidation(event)); err != nil {
			return nil, fmt.Errorf("attention snapshot descriptor %d: %w", index, err)
		}
	}
	live := &Subscription{filter: deliveryFilter{sessionID: sessionID}, ch: make(chan attentionEnvelope, b.bufferSize)}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		live.closeWithError(io.EOF)
		return &SnapshotSubscription{live: live, openingOrdinaryWatermark: openingWatermark, done: true}, nil
	}
	id := b.nextID
	b.nextID++
	b.nextSnapshotGeneration++
	generation := b.nextSnapshotGeneration
	policy := b.invariants
	snapshot := make([]attentionEnvelope, 0, len(descriptors)+1)
	var observedTaskBatchKey *TaskQuestionBatchKey
	var initialConflict *attentionEnvelope
	for _, descriptor := range descriptors {
		b.nextSeq++
		event := clientui.AttentionNotificationEvent{
			Sequence: b.nextSeq,
			Source:   clientui.AttentionNotificationSourceSnapshot,
			Type:     clientui.AttentionNotificationEventPending,
			Pending:  &descriptor.Notification,
		}
		envelope := attentionEnvelope{event: event, occurrence: descriptor.Occurrence.clone()}
		snapshot = append(snapshot, envelope)
		if key, ok := envelope.occurrence.TaskQuestionBatchKey(); ok {
			switch {
			case observedTaskBatchKey == nil:
				observedTaskBatchKey = taskQuestionBatchKeyPointer(key)
			case *observedTaskBatchKey != key && initialConflict == nil:
				conflict := envelope
				initialConflict = &conflict
			}
		}
	}
	b.nextSeq++
	snapshot = append(snapshot, attentionEnvelope{event: clientui.AttentionNotificationEvent{
		Sequence:  b.nextSeq,
		Source:    clientui.AttentionNotificationSourceSnapshot,
		Type:      clientui.AttentionNotificationEventSnapshotComplete,
		SessionID: sessionID,
	}})
	subscription := &SnapshotSubscription{
		live:                     live,
		snapshot:                 snapshot,
		sessionID:                sessionID,
		generation:               generation,
		openingOrdinaryWatermark: openingWatermark,
		observedTaskBatchKey:     observedTaskBatchKey,
		invariants:               policy,
	}
	live.admit = subscription.admitLiveEnvelope
	live.onClose = func() {
		b.mu.Lock()
		delete(b.subscribers, id)
		b.mu.Unlock()
	}
	b.subscribers[id] = live
	b.mu.Unlock()
	if initialConflict != nil {
		subscription.closeWithProjectionDiscontinuity(*initialConflict)
	}
	return subscription, nil
}

func (b *Broker) subscribe(filter deliveryFilter) (*Subscription, error) {
	if b == nil {
		return nil, fmt.Errorf("attention notification stream is unavailable: %w", serverapi.ErrStreamUnavailable)
	}
	sub := &Subscription{filter: filter, ch: make(chan attentionEnvelope, b.bufferSize)}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		sub.closeWithError(io.EOF)
		return sub, nil
	}
	id := b.nextID
	b.nextID++
	b.subscribers[id] = sub
	b.mu.Unlock()
	sub.onClose = func() {
		b.mu.Lock()
		delete(b.subscribers, id)
		b.mu.Unlock()
	}
	return sub, nil
}

func (b *Broker) PublishPending(scope RoutingScope, notification clientui.AttentionNotification) error {
	return b.PublishPendingWithOccurrence(scope, notification, OccurrenceMetadata{})
}

func (b *Broker) PublishPendingWithOccurrence(scope RoutingScope, notification clientui.AttentionNotification, occurrence OccurrenceMetadata) error {
	event := clientui.AttentionNotificationEvent{
		Source:  clientui.AttentionNotificationSourceLive,
		Type:    clientui.AttentionNotificationEventPending,
		Pending: &notification,
	}
	if err := serverapi.ValidateAttentionNotificationEvent(withSequenceForValidation(event)); err != nil {
		return err
	}
	return b.publish(scope, attentionEnvelope{event: event, occurrence: occurrence.clone()})
}

func (b *Broker) PublishResolved(scope RoutingScope, id clientui.AttentionNotificationID, kind clientui.AttentionNotificationKind, occurredAt time.Time) error {
	return b.PublishResolvedWithOccurrence(scope, id, kind, occurredAt, OccurrenceMetadata{})
}

func (b *Broker) PublishResolvedWithOccurrence(scope RoutingScope, id clientui.AttentionNotificationID, kind clientui.AttentionNotificationKind, occurredAt time.Time, occurrence OccurrenceMetadata) error {
	resolvedID := id
	resolvedAt := occurredAt
	event := clientui.AttentionNotificationEvent{
		Source:     clientui.AttentionNotificationSourceLive,
		Type:       clientui.AttentionNotificationEventResolved,
		ID:         &resolvedID,
		Kind:       kind,
		OccurredAt: &resolvedAt,
	}
	if err := serverapi.ValidateAttentionNotificationEvent(withSequenceForValidation(event)); err != nil {
		return err
	}
	return b.publish(scope, attentionEnvelope{event: event, occurrence: occurrence.clone()})
}

func withSequenceForValidation(event clientui.AttentionNotificationEvent) clientui.AttentionNotificationEvent {
	event.Sequence = 1
	return event
}

func (b *Broker) publish(scope RoutingScope, envelope attentionEnvelope) error {
	if b == nil {
		return fmt.Errorf("attention notification stream is unavailable: %w", serverapi.ErrStreamUnavailable)
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return io.EOF
	}
	b.nextSeq++
	envelope.event.Sequence = b.nextSeq
	subs := make([]*Subscription, 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		if deliveryMatches(sub.filter, scope) {
			subs = append(subs, sub)
		}
	}
	b.mu.Unlock()
	for _, sub := range subs {
		if err := sub.publish(envelope); err != nil {
			sub.closeWithError(err)
		}
	}
	return nil
}

func (b *Broker) Close(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := make([]*Subscription, 0, len(b.subscribers))
	for id, sub := range b.subscribers {
		subs = append(subs, sub)
		delete(b.subscribers, id)
	}
	b.mu.Unlock()
	for _, sub := range subs {
		sub.closeWithError(err)
	}
}

func deliveryMatches(filter deliveryFilter, scope RoutingScope) bool {
	if filter.desktopRoot {
		return scope.Kind == RoutingWorkflowTask
	}
	if filter.sessionID == "" {
		return false
	}
	switch scope.Kind {
	case RoutingSessionPrompt:
		return scope.SessionID == filter.sessionID
	case RoutingWorkflowTask:
		return scope.SessionID == filter.sessionID
	default:
		return false
	}
}

func (s *Subscription) publish(envelope attentionEnvelope) error {
	if s == nil {
		return io.EOF
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		if s.err != nil {
			return s.err
		}
		return io.EOF
	}
	if s.admit != nil {
		deliver, err := s.admit(envelope)
		if err != nil {
			return err
		}
		if !deliver {
			return nil
		}
	}
	select {
	case s.ch <- envelope:
		return nil
	default:
		return serverapi.ErrStreamGap
	}
}

func (s *Subscription) Next(ctx context.Context) (clientui.AttentionNotificationEvent, error) {
	envelope, err := s.nextEnvelope(ctx)
	if err != nil {
		return clientui.AttentionNotificationEvent{}, err
	}
	return envelope.event, nil
}

func (s *Subscription) nextEnvelope(ctx context.Context) (attentionEnvelope, error) {
	if s == nil {
		return attentionEnvelope{}, io.EOF
	}
	select {
	case <-ctx.Done():
		return attentionEnvelope{}, ctx.Err()
	case envelope, ok := <-s.ch:
		if ok {
			return envelope, nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.err != nil {
			return attentionEnvelope{}, serverapi.NormalizeStreamError(s.err)
		}
		return attentionEnvelope{}, io.EOF
	}
}

func (s *Subscription) Close() error {
	if s == nil {
		return nil
	}
	s.closeWithError(io.EOF)
	return nil
}

func (s *Subscription) closeWithError(err error) {
	if s == nil {
		return
	}
	var onClose func()
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	s.err = err
	close(s.ch)
	onClose = s.onClose
	s.mu.Unlock()
	if onClose != nil {
		onClose()
	}
}

func (s *SnapshotSubscription) Next(ctx context.Context) (clientui.AttentionNotificationEvent, error) {
	if s == nil {
		return clientui.AttentionNotificationEvent{}, io.EOF
	}
	s.nextMu.Lock()
	defer s.nextMu.Unlock()
	for {
		s.mu.Lock()
		if s.done {
			err := s.err
			s.mu.Unlock()
			if err != nil {
				return clientui.AttentionNotificationEvent{}, serverapi.NormalizeStreamError(err)
			}
			return clientui.AttentionNotificationEvent{}, io.EOF
		}
		if s.next < len(s.snapshot) {
			event := s.snapshot[s.next].event
			s.next++
			s.mu.Unlock()
			return event, nil
		}
		live := s.live
		s.mu.Unlock()
		if live == nil {
			return clientui.AttentionNotificationEvent{}, io.EOF
		}
		envelope, err := live.nextEnvelope(ctx)
		if err != nil {
			return clientui.AttentionNotificationEvent{}, err
		}
		return envelope.event, nil
	}
}

func (s *SnapshotSubscription) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil
	}
	s.done = true
	live := s.live
	s.mu.Unlock()
	if live == nil {
		return nil
	}
	return live.Close()
}

func (s *SnapshotSubscription) admitLiveEnvelope(envelope attentionEnvelope) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		err := s.err
		if err != nil {
			return false, err
		}
		return false, io.EOF
	}
	if ordinary, ok := envelope.occurrence.OrdinaryOrdinal(); ok && ordinary <= OrdinaryOccurrenceOrdinal(s.openingOrdinaryWatermark) {
		return false, nil
	}
	incomingKey, hasIncomingKey := envelope.occurrence.TaskQuestionBatchKey()
	if !hasIncomingKey {
		return true, nil
	}
	if s.observedTaskBatchKey == nil {
		if envelope.event.Type != clientui.AttentionNotificationEventResolved {
			s.observedTaskBatchKey = taskQuestionBatchKeyPointer(incomingKey)
		}
		return true, nil
	}
	if *s.observedTaskBatchKey == incomingKey {
		if envelope.event.Type == clientui.AttentionNotificationEventResolved {
			s.observedTaskBatchKey = nil
			return true, nil
		}
		return false, nil
	}
	return false, s.projectionDiscontinuityLocked(envelope)
}

func (s *SnapshotSubscription) closeWithProjectionDiscontinuity(envelope attentionEnvelope) error {
	s.mu.Lock()
	if s.done {
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return serverapi.NormalizeStreamError(err)
		}
		return io.EOF
	}
	err := s.projectionDiscontinuityLocked(envelope)
	live := s.live
	s.mu.Unlock()
	if live != nil {
		live.closeWithError(err)
	}
	return err
}

func (s *SnapshotSubscription) projectionDiscontinuityLocked(envelope attentionEnvelope) error {
	incomingKey, incomingOK := envelope.occurrence.TaskQuestionBatchKey()
	if !incomingOK || s.observedTaskBatchKey == nil {
		panic("attention projection discontinuity requires two task batch keys")
	}
	diagnostic := s.invariants.Violation(invariant.AttentionProjectionDiagnostic(invariant.AttentionProjectionDiagnosticInput{
		Operation:              "attention_snapshot_second_task_batch_key",
		SessionID:              s.sessionID,
		SubscriptionGeneration: strconv.FormatUint(s.generation, 10),
		OpeningWatermark:       strconv.FormatUint(uint64(s.openingOrdinaryWatermark), 10),
		ObservedTaskBatchKey:   taskQuestionBatchKeyString(*s.observedTaskBatchKey),
		IncomingTaskBatchKey:   taskQuestionBatchKeyString(incomingKey),
		EventSource:            string(envelope.event.Source),
		EventRevision:          attentionEventRevision(envelope.event),
	}))
	err := AttentionProjectionDiscontinuity{Diagnostic: diagnostic}
	s.done = true
	s.err = err
	return err
}

func taskQuestionBatchKeyPointer(key TaskQuestionBatchKey) *TaskQuestionBatchKey {
	out := key
	return &out
}

func taskQuestionBatchKeyString(key TaskQuestionBatchKey) string {
	return hex.EncodeToString(key[:])
}

func attentionEventRevision(event clientui.AttentionNotificationEvent) string {
	if event.Pending == nil {
		return ""
	}
	return strconv.FormatUint(event.Pending.Revision, 10)
}

func (s *SnapshotSubscription) OpeningOrdinaryWatermark() OrdinaryOccurrenceWatermark {
	if s == nil {
		return 0
	}
	return s.openingOrdinaryWatermark
}

var _ serverapi.AttentionNotificationSubscription = (*Subscription)(nil)
var _ serverapi.AttentionNotificationSubscription = (*SnapshotSubscription)(nil)

var ErrBatchNotFound = errors.New("question batch is not registered")
