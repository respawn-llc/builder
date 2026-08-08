package attentionnotify

import (
	"fmt"
	"sync"
	"time"

	"core/shared/clientui"
)

type QuestionBatchTracker struct {
	mu     sync.Mutex
	broker *Broker

	byStep map[string]*questionBatch
}

type QuestionBatch struct {
	StepID         string
	Route          RoutingScope
	Target         clientui.AttentionNotificationTarget
	Preview        string
	PreparedAskIDs []string
	OccurredAt     time.Time
}

type questionBatch struct {
	QuestionBatch
	status   map[string]questionAskStatus
	revision uint64
	emitted  bool
	resolved bool
}

type questionAskStatus string

const (
	questionAskPending      questionAskStatus = "pending_candidate"
	questionAskMaterialized questionAskStatus = "materialized"
	questionAskCleared      questionAskStatus = "durably_cleared"
	questionAskSkipped      questionAskStatus = "skipped"
)

func NewQuestionBatchTracker(broker *Broker) *QuestionBatchTracker {
	return &QuestionBatchTracker{broker: broker, byStep: map[string]*questionBatch{}}
}

func (t *QuestionBatchTracker) Prepare(batch QuestionBatch) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.prepareLocked(batch)
}

func (t *QuestionBatchTracker) prepareLocked(batch QuestionBatch) error {
	if batch.StepID == "" {
		return fmt.Errorf("question batch step id is required")
	}
	if len(batch.PreparedAskIDs) == 0 {
		return fmt.Errorf("question batch prepared ask ids are required")
	}
	if _, ok := t.byStep[batch.StepID]; ok {
		existing := t.byStep[batch.StepID]
		if existing.Preview == "" {
			existing.Preview = batch.Preview
		}
		if !existing.emitted {
			existing.Route = batch.Route
			existing.Target = batch.Target
			existing.OccurredAt = batch.OccurredAt
		}
		return nil
	}
	status := make(map[string]questionAskStatus, len(batch.PreparedAskIDs))
	for _, askID := range batch.PreparedAskIDs {
		if askID == "" {
			return fmt.Errorf("question batch prepared ask id is required")
		}
		status[askID] = questionAskPending
	}
	t.byStep[batch.StepID] = &questionBatch{QuestionBatch: batch, status: status}
	return nil
}

func (t *QuestionBatchTracker) MarkMaterialized(stepID string, askID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	batch, err := t.batch(stepID, askID)
	if err != nil {
		return err
	}
	if batch.resolved {
		return nil
	}
	if batch.status[askID] == questionAskPending {
		batch.status[askID] = questionAskMaterialized
	}
	if batch.emitted || batch.status[askID] != questionAskMaterialized {
		return nil
	}
	return t.publishBatch(batch)
}

func (t *QuestionBatchTracker) EnqueueSnapshot(sub *Subscription, batch QuestionBatch, materializedAskIDs []string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.prepareLocked(batch); err != nil {
		return err
	}
	if len(materializedAskIDs) == 0 {
		return fmt.Errorf("question batch snapshot materialized ask ids are required")
	}
	current := t.byStep[batch.StepID]
	if current.resolved {
		return nil
	}
	for _, askID := range materializedAskIDs {
		if _, ok := current.status[askID]; !ok {
			return fmt.Errorf("question batch Step %q does not contain ask %q", batch.StepID, askID)
		}
		if current.status[askID] == questionAskPending {
			current.status[askID] = questionAskMaterialized
		}
	}
	current.emitted = true
	if current.revision == 0 {
		current.revision = 1
	}
	return t.broker.EnqueueInitial(sub, current.Route, clientui.AttentionNotificationEvent{
		Source:  clientui.AttentionNotificationSourceSnapshot,
		Type:    clientui.AttentionNotificationEventPending,
		Pending: current.notification(),
	})
}

func (t *QuestionBatchTracker) MarkSkipped(stepID string, askID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	batch, err := t.batch(stepID, askID)
	if err != nil {
		return err
	}
	if batch.resolved {
		return nil
	}
	if batch.status[askID] == questionAskPending {
		batch.status[askID] = questionAskSkipped
	}
	return t.resolveIfComplete(batch)
}

func (t *QuestionBatchTracker) MarkDurablyCleared(stepID string, askID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	batch, err := t.batch(stepID, askID)
	if err != nil {
		return err
	}
	if batch.resolved {
		return nil
	}
	if batch.status[askID] == questionAskMaterialized {
		batch.status[askID] = questionAskCleared
	}
	return t.resolveIfComplete(batch)
}

func (t *QuestionBatchTracker) batch(stepID string, askID string) (*questionBatch, error) {
	if t == nil {
		return nil, ErrBatchNotFound
	}
	batch, ok := t.byStep[stepID]
	if !ok {
		return nil, ErrBatchNotFound
	}
	if _, ok := batch.status[askID]; !ok {
		return nil, fmt.Errorf("question batch Step %q does not contain ask %q", stepID, askID)
	}
	return batch, nil
}

func (t *QuestionBatchTracker) publishBatch(batch *questionBatch) error {
	nextRevision := batch.revision + 1
	previousRevision := batch.revision
	batch.revision = nextRevision
	notification := *batch.notification()
	batch.revision = previousRevision
	if err := t.broker.PublishPending(batch.Route, notification); err != nil {
		return err
	}
	batch.revision = nextRevision
	batch.emitted = true
	return nil
}

func (b *questionBatch) notification() *clientui.AttentionNotification {
	return &clientui.AttentionNotification{
		ID: clientui.AttentionNotificationID{
			Kind: clientui.AttentionNotificationKindQuestion,
			UUID: b.StepID,
		},
		Kind:       clientui.AttentionNotificationKindQuestion,
		OccurredAt: b.OccurredAt,
		Revision:   b.revision,
		Question:   b.questionState(),
		Target:     b.Target,
	}
}

func (t *QuestionBatchTracker) resolveIfComplete(batch *questionBatch) error {
	if !batch.complete() || batch.resolved {
		return nil
	}
	if !batch.emitted {
		batch.resolved = true
		delete(t.byStep, batch.StepID)
		return nil
	}
	id := clientui.AttentionNotificationID{
		Kind: clientui.AttentionNotificationKindQuestion,
		UUID: batch.StepID,
	}
	if err := t.broker.PublishResolved(batch.Route, id, clientui.AttentionNotificationKindQuestion, time.Now().UTC()); err != nil {
		return err
	}
	batch.resolved = true
	delete(t.byStep, batch.StepID)
	return nil
}

func (b *questionBatch) complete() bool {
	for _, status := range b.status {
		if status != questionAskCleared && status != questionAskSkipped {
			return false
		}
	}
	return true
}

func (b *questionBatch) displayCount() int {
	count := 0
	for _, askID := range b.PreparedAskIDs {
		if b.status[askID] != questionAskSkipped {
			count++
		}
	}
	return count
}

func (b *questionBatch) questionState() *clientui.AttentionNotificationQuestionState {
	materialized := make([]string, 0)
	unresolved := make([]string, 0)
	skipped := make([]string, 0)
	for _, askID := range b.PreparedAskIDs {
		switch b.status[askID] {
		case questionAskMaterialized:
			materialized = append(materialized, askID)
			unresolved = append(unresolved, askID)
		case questionAskCleared:
			materialized = append(materialized, askID)
		case questionAskSkipped:
			skipped = append(skipped, askID)
		}
	}
	return &clientui.AttentionNotificationQuestionState{
		PreparedAskIDs:          append([]string(nil), b.PreparedAskIDs...),
		MaterializedAskIDs:      materialized,
		CurrentUnresolvedAskIDs: unresolved,
		SkippedAskIDs:           skipped,
		Preview:                 b.Preview,
		DisplayCount:            b.displayCount(),
		MaterializedCount:       len(materialized),
	}
}
