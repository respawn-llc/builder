package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

var ErrLifecyclePublicationClosed = errors.New("LifecyclePublication is closed")
var ErrLifecycleExactNotPublished = errors.New("Exact execution scope is not published")

type QueuedTaskLifecycleDelta struct {
	delta TaskLifecycleDelta
}

func NewQueuedTaskLifecycleDelta(
	taskID workflow.TaskID,
	queued []workflow.CurrentNodeReference,
) (QueuedTaskLifecycleDelta, error) {
	if taskID == "" {
		return QueuedTaskLifecycleDelta{}, errors.New("queued lifecycle delta Task id is required")
	}
	if len(queued) == 0 {
		return QueuedTaskLifecycleDelta{}, errors.New("queued lifecycle delta requires Current Nodes")
	}
	copied := make([]workflow.CurrentNodeReference, 0, len(queued))
	seen := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(queued))
	for _, reference := range queued {
		if err := reference.Validate(); err != nil {
			return QueuedTaskLifecycleDelta{}, err
		}
		if reference.TaskID != taskID {
			return QueuedTaskLifecycleDelta{}, errors.New("queued lifecycle delta Current Nodes must belong to its Task")
		}
		key, err := reference.Key()
		if err != nil {
			return QueuedTaskLifecycleDelta{}, err
		}
		if _, exists := seen[key]; exists {
			return QueuedTaskLifecycleDelta{}, errors.New("queued lifecycle delta contains a duplicate Current Node")
		}
		seen[key] = struct{}{}
		copied = append(copied, reference)
	}
	changes := make([]LifecycleRunDelta, 0, len(copied))
	for _, reference := range copied {
		changes = append(changes, LifecycleRunDelta{
			CurrentNode: reference,
			Expect:      LifecycleFieldAbsent,
			Next:        LifecycleFieldPresent,
		})
	}
	return QueuedTaskLifecycleDelta{delta: TaskLifecycleDelta{
		taskID: taskID,
		runs:   changes,
	}}, nil
}

func (d QueuedTaskLifecycleDelta) TaskID() workflow.TaskID {
	return d.delta.taskID
}

func (d QueuedTaskLifecycleDelta) QueuedCurrentNodes() []workflow.CurrentNodeReference {
	references := make([]workflow.CurrentNodeReference, 0, len(d.delta.runs))
	for _, change := range d.delta.runs {
		if change.Next == LifecycleFieldPresent {
			references = append(references, change.CurrentNode)
		}
	}
	return references
}

type LifecycleFieldPresence uint8

const (
	LifecycleFieldAbsent LifecycleFieldPresence = iota + 1
	LifecycleFieldPresent
)

type LifecycleRunDelta struct {
	CurrentNode workflow.CurrentNodeReference
	Expect      LifecycleFieldPresence
	Next        LifecycleFieldPresence
}

type LifecycleExactDelta struct {
	CurrentNode workflow.CurrentNodeReference
	ExpectScope *runtimeids.ExecutionScopeID
	Next        *LifecycleExactExecution
}

type TaskLifecycleDelta struct {
	taskID workflow.TaskID
	runs   []LifecycleRunDelta
	exact  []LifecycleExactDelta
}

func NewTaskLifecycleDelta(
	taskID workflow.TaskID,
	runs []LifecycleRunDelta,
	exact []LifecycleExactDelta,
) (TaskLifecycleDelta, error) {
	if taskID == "" {
		return TaskLifecycleDelta{}, errors.New("lifecycle delta Task id is required")
	}
	if len(runs) == 0 && len(exact) == 0 {
		return TaskLifecycleDelta{}, errors.New("lifecycle delta requires a field change")
	}
	copiedRuns := append([]LifecycleRunDelta(nil), runs...)
	copiedExact := append([]LifecycleExactDelta(nil), exact...)
	seenRuns := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(copiedRuns))
	for index := range copiedRuns {
		change := &copiedRuns[index]
		if err := validateLifecycleDeltaReference(taskID, change.CurrentNode); err != nil {
			return TaskLifecycleDelta{}, err
		}
		if !validLifecycleFieldPresence(change.Expect) || !validLifecycleFieldPresence(change.Next) {
			return TaskLifecycleDelta{}, errors.New("lifecycle Run delta has invalid field presence")
		}
		key, _ := change.CurrentNode.Key()
		if _, duplicate := seenRuns[key]; duplicate {
			return TaskLifecycleDelta{}, errors.New("lifecycle Run delta contains a duplicate Current Node")
		}
		seenRuns[key] = struct{}{}
	}
	seenExact := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(copiedExact))
	for index := range copiedExact {
		change := &copiedExact[index]
		if err := validateLifecycleDeltaReference(taskID, change.CurrentNode); err != nil {
			return TaskLifecycleDelta{}, err
		}
		if change.ExpectScope != nil && change.ExpectScope.IsZero() {
			return TaskLifecycleDelta{}, errors.New("lifecycle Exact delta expected scope is invalid")
		}
		if change.Next != nil {
			if err := validateLifecycleExactExecution(*change.Next); err != nil {
				return TaskLifecycleDelta{}, err
			}
			if !change.Next.CurrentNode.Equal(change.CurrentNode) {
				return TaskLifecycleDelta{}, errors.New("lifecycle Exact delta next execution references another Current Node")
			}
		}
		if change.ExpectScope != nil && change.Next != nil && *change.ExpectScope == change.Next.ScopeID {
			return TaskLifecycleDelta{}, errors.New("lifecycle Exact delta must change scope identity")
		}
		key, _ := change.CurrentNode.Key()
		if _, duplicate := seenExact[key]; duplicate {
			return TaskLifecycleDelta{}, errors.New("lifecycle Exact delta contains a duplicate Current Node")
		}
		seenExact[key] = struct{}{}
	}
	return TaskLifecycleDelta{taskID: taskID, runs: copiedRuns, exact: copiedExact}, nil
}

func validateLifecycleDeltaReference(taskID workflow.TaskID, reference workflow.CurrentNodeReference) error {
	if err := reference.Validate(); err != nil {
		return err
	}
	if reference.TaskID != taskID {
		return errors.New("lifecycle delta Current Node must belong to its Task")
	}
	return nil
}

func validLifecycleFieldPresence(value LifecycleFieldPresence) bool {
	return value == LifecycleFieldAbsent || value == LifecycleFieldPresent
}

func (d TaskLifecycleDelta) TaskID() workflow.TaskID {
	return d.taskID
}

func (d TaskLifecycleDelta) RunChanges() []LifecycleRunDelta {
	return append([]LifecycleRunDelta(nil), d.runs...)
}

func (d TaskLifecycleDelta) ExactChanges() []LifecycleExactDelta {
	return append([]LifecycleExactDelta(nil), d.exact...)
}

type LifecycleExactExecutionPhase uint8

const (
	LifecycleExactExecutionRunning LifecycleExactExecutionPhase = iota + 1
	LifecycleExactExecutionFinalizing
)

type LifecycleAgentExecutionTarget struct {
	SessionID runtimeids.SessionID
}

type LifecycleScriptExecutionTarget struct {
	Path string
}

type LifecyclePendingPromptKind uint8

const (
	LifecyclePendingPromptQuestion LifecyclePendingPromptKind = iota + 1
	LifecyclePendingPromptSessionApproval
)

type LifecyclePendingPrompt struct {
	ID                     string
	Kind                   LifecyclePendingPromptKind
	CreatedAt              time.Time
	Question               string
	Suggestions            []string
	RecommendedOptionIndex *int
	ApprovalDecisions      []LifecycleApprovalDecision
}

type LifecyclePendingPromptReference struct {
	ID        string
	Kind      LifecyclePendingPromptKind
	CreatedAt time.Time
}

type LifecycleApprovalDecision string

const (
	LifecycleApprovalAllowOnce    LifecycleApprovalDecision = "allow_once"
	LifecycleApprovalAllowSession LifecycleApprovalDecision = "allow_session"
	LifecycleApprovalDeny         LifecycleApprovalDecision = "deny"
)

// LifecycleExactExecution is the immutable lifecycle observation of one
// published Exact Execution Scope. Pending prompt payloads live only in the
// paged Question projection.
type LifecycleExactExecution struct {
	ProjectID      string
	WorkflowID     runtimeids.WorkflowID
	CurrentNode    workflow.CurrentNodeReference
	ScopeID        runtimeids.ExecutionScopeID
	Agent          *LifecycleAgentExecutionTarget
	Script         *LifecycleScriptExecutionTarget
	Phase          LifecycleExactExecutionPhase
	PendingPrompts []LifecyclePendingPromptReference
}

// LifecycleExactRegistrationActivation commits only the staged execution
// owner's private running state. It must not call controller or publication
// behavior because PublishExactRegistration invokes it inside publication.
type LifecycleExactRegistrationActivation interface {
	Activate() error
}

func validateLifecycleExactExecution(exact LifecycleExactExecution) error {
	if strings.TrimSpace(exact.ProjectID) == "" {
		return errors.New("published Exact execution project id is required")
	}
	if exact.WorkflowID.IsZero() {
		return errors.New("published Exact execution workflow id is required")
	}
	if err := exact.CurrentNode.Validate(); err != nil {
		return err
	}
	if exact.ScopeID.IsZero() {
		return errors.New("published Exact execution scope id is required")
	}
	if (exact.Agent == nil) == (exact.Script == nil) {
		return errors.New("published Exact execution must have exactly one target")
	}
	if exact.Agent != nil && exact.Agent.SessionID.IsZero() {
		return errors.New("published Exact Agent execution session id is required")
	}
	if exact.Script != nil && strings.TrimSpace(exact.Script.Path) == "" {
		return errors.New("published Exact Script execution path is required")
	}
	switch exact.Phase {
	case LifecycleExactExecutionRunning, LifecycleExactExecutionFinalizing:
	default:
		return errors.New("published Exact execution phase is invalid")
	}
	for index, prompt := range exact.PendingPrompts {
		if err := validateLifecyclePendingPromptReference(prompt); err != nil {
			return err
		}
		if index != 0 && exact.PendingPrompts[index-1].ID >= prompt.ID {
			return errors.New("published Exact execution pending prompts are not sorted and unique")
		}
	}
	return nil
}

func validateLifecyclePendingPromptReference(prompt LifecyclePendingPromptReference) error {
	if strings.TrimSpace(prompt.ID) == "" || strings.TrimSpace(prompt.ID) != prompt.ID {
		return errors.New("published Exact execution pending prompt id is invalid")
	}
	switch prompt.Kind {
	case LifecyclePendingPromptQuestion, LifecyclePendingPromptSessionApproval:
	default:
		return errors.New("published Exact execution pending prompt kind is invalid")
	}
	if prompt.CreatedAt.IsZero() {
		return errors.New("published Exact execution pending prompt occurrence time is required")
	}
	return nil
}

func validateLifecyclePendingPrompt(prompt LifecyclePendingPrompt) error {
	if err := validateLifecyclePendingPromptReference(lifecyclePendingPromptReference(prompt)); err != nil {
		return err
	}
	switch prompt.Kind {
	case LifecyclePendingPromptQuestion:
		if len(prompt.ApprovalDecisions) != 0 {
			return errors.New("published Exact Question prompt has Approval decisions")
		}
	case LifecyclePendingPromptSessionApproval:
		if len(prompt.ApprovalDecisions) == 0 {
			return errors.New("published Exact Session Approval prompt has no decisions")
		}
		for _, decision := range prompt.ApprovalDecisions {
			switch decision {
			case LifecycleApprovalAllowOnce,
				LifecycleApprovalAllowSession,
				LifecycleApprovalDeny:
			default:
				return errors.New("published Exact Session Approval prompt has an invalid decision")
			}
		}
	}
	return nil
}

func lifecyclePendingPromptReference(prompt LifecyclePendingPrompt) LifecyclePendingPromptReference {
	return LifecyclePendingPromptReference{
		ID:        prompt.ID,
		Kind:      prompt.Kind,
		CreatedAt: prompt.CreatedAt,
	}
}

func cloneLifecycleExactExecution(exact LifecycleExactExecution) LifecycleExactExecution {
	cloned := exact
	if exact.Agent != nil {
		agent := *exact.Agent
		cloned.Agent = &agent
	}
	if exact.Script != nil {
		script := *exact.Script
		cloned.Script = &script
	}
	cloned.PendingPrompts = append([]LifecyclePendingPromptReference(nil), exact.PendingPrompts...)
	return cloned
}

type CurrentNodeInterruptionPredecessor uint8

const (
	CurrentNodeInterruptionFromReadyOrAdmitted CurrentNodeInterruptionPredecessor = iota + 1
	CurrentNodeInterruptionFromAdmitted
)

type lifecycleTaskEntry struct {
	runs  map[workflow.CurrentNodeReferenceKey]workflow.CurrentNodeReference
	exact map[workflow.CurrentNodeReferenceKey]LifecycleExactExecution
}

type lifecycleRoot map[workflow.TaskID]lifecycleTaskEntry

// LifecyclePublication is the sole owner of publishing a compatible durable
// lifecycle commit and immutable runtime root.
type LifecyclePublication struct {
	mu            sync.RWMutex
	store         *Store
	root          lifecycleRoot
	questionIndex *lifecycleQuestionIndex
	closed        bool
}

type preparedSQLLifecycleMutation struct {
	mu       sync.Mutex
	tx       *sql.Tx
	resolved bool
}

type lifecyclePreparedMutation interface {
	commit() error
	rollback() error
}

func newPreparedSQLLifecycleMutation(tx *sql.Tx) *preparedSQLLifecycleMutation {
	if tx == nil {
		panic("prepared lifecycle mutation requires a transaction")
	}
	return &preparedSQLLifecycleMutation{tx: tx}
}

func (m *preparedSQLLifecycleMutation) commit() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resolved {
		panic("prepared lifecycle mutation was resolved more than once")
	}
	m.resolved = true
	commitErr := m.tx.Commit()
	if commitErr != nil {
		rollbackErr := m.tx.Rollback()
		if errors.Is(rollbackErr, sql.ErrTxDone) {
			rollbackErr = nil
		}
		m.tx = nil
		return errors.Join(commitErr, rollbackErr)
	}
	m.tx = nil
	return nil
}

func (m *preparedSQLLifecycleMutation) rollback() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resolved {
		panic("prepared lifecycle mutation was resolved more than once")
	}
	m.resolved = true
	err := m.tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		err = nil
	}
	m.tx = nil
	return err
}

func (p *LifecyclePublication) publishPrepared(
	ctx context.Context,
	prepared lifecyclePreparedMutation,
	delta TaskLifecycleDelta,
) error {
	if p == nil || p.store == nil {
		return errors.Join(errors.New("LifecyclePublication is required"), prepared.rollback())
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.Join(ErrLifecyclePublicationClosed, prepared.rollback())
	}
	candidate := cloneLifecycleRoot(p.root)
	if err := applyTaskLifecycleDelta(candidate, delta); err != nil {
		return errors.Join(err, prepared.rollback())
	}
	if err := context.Cause(ctx); err != nil {
		return errors.Join(err, prepared.rollback())
	}
	if err := prepared.commit(); err != nil {
		return err
	}
	p.root = candidate
	return nil
}

func (p *LifecyclePublication) publishPreparedTaskSetDeletion(
	ctx context.Context,
	prepared lifecyclePreparedMutation,
	taskIDs []workflow.TaskID,
) error {
	if p == nil || p.store == nil {
		return errors.Join(errors.New("LifecyclePublication is required"), prepared.rollback())
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.Join(ErrLifecyclePublicationClosed, prepared.rollback())
	}
	candidate := cloneLifecycleRoot(p.root)
	for _, taskID := range taskIDs {
		if taskID == "" {
			return errors.Join(
				errors.New("Task deletion lifecycle root requires a Task id"),
				prepared.rollback(),
			)
		}
		if entry, exists := candidate[taskID]; exists &&
			(len(entry.runs) != 0 || len(entry.exact) != 0) {
			return errors.Join(
				errors.New("Task deletion requires an absent runtime lifecycle root"),
				prepared.rollback(),
			)
		}
		delete(candidate, taskID)
	}
	if err := context.Cause(ctx); err != nil {
		return errors.Join(err, prepared.rollback())
	}
	if err := prepared.commit(); err != nil {
		return err
	}
	p.root = candidate
	return nil
}

type TaskStartPublicationStage func(StartTaskResult) (TaskLifecycleDelta, func(error), error)
type CurrentNodeCompletionPublicationStage func(CurrentNodeCompletionResult) (TaskLifecycleDelta, func(error), error)
type PendingApprovalPublicationStage func(PendingApprovalApplyResult) (TaskLifecycleDelta, func(error), error)
type ManualMovePublicationStage func(ManualMoveResult) (TaskLifecycleDelta, func(error), error)

type PreparedCurrentNodeCompletionPublication interface {
	Result() CurrentNodeCompletionResult
	Publish(context.Context) (CurrentNodeCompletionResult, LifecyclePublicationOutcome, error)
	Rollback(error) error
}

type LifecyclePublicationOutcome struct {
	committed bool
}

func (o LifecyclePublicationOutcome) Committed() bool {
	return o.committed
}

func CommittedLifecyclePublicationOutcome() LifecyclePublicationOutcome {
	return LifecyclePublicationOutcome{committed: true}
}

type preparedCurrentNodeCompletionPublication struct {
	owner           *LifecyclePublication
	request         CurrentNodeCompletionRequest
	prepared        *preparedCurrentNodeCompletion
	delta           TaskLifecycleDelta
	rollbackRuntime func(error)
}

func NewTaskStartLifecycleDelta(result StartTaskResult) (TaskLifecycleDelta, error) {
	if len(result.Mutation.Created) == 0 {
		return TaskLifecycleDelta{}, errors.New("Task Start lifecycle delta requires created Current Nodes")
	}
	taskID := result.Mutation.Created[0].Reference.TaskID
	changes := make([]LifecycleRunDelta, 0, len(result.Mutation.Created))
	for _, currentNode := range result.Mutation.Created {
		if currentNode.Scheduling == nil {
			continue
		}
		if currentNode.Reference.TaskID != taskID {
			return TaskLifecycleDelta{}, errors.New("Task Start lifecycle delta cannot cross Tasks")
		}
		changes = append(changes, LifecycleRunDelta{
			CurrentNode: currentNode.Reference,
			Expect:      LifecycleFieldAbsent,
			Next:        LifecycleFieldPresent,
		})
	}
	if len(changes) == 0 {
		return TaskLifecycleDelta{}, errors.New("Task Start lifecycle delta requires executable Current Nodes")
	}
	return NewTaskLifecycleDelta(taskID, changes, nil)
}

func (p *LifecyclePublication) PublishTaskStart(
	ctx context.Context,
	taskID workflow.TaskID,
	stage TaskStartPublicationStage,
) (StartTaskResult, error) {
	if stage == nil {
		return StartTaskResult{}, errors.New("Task Start publication stage is required")
	}
	prepared, err := p.store.prepareStartTaskMutation(ctx, taskID)
	if err != nil {
		return StartTaskResult{}, err
	}
	delta, rollbackRuntime, err := stage(prepared.result)
	if err != nil {
		return StartTaskResult{}, errors.Join(err, prepared.rollback())
	}
	if rollbackRuntime == nil {
		rollbackRuntime = func(error) {}
	}
	if err := p.publishPrepared(ctx, prepared.preparedSQLLifecycleMutation, delta); err != nil {
		rollbackRuntime(err)
		return StartTaskResult{}, err
	}
	return prepared.result, nil
}

func (p *LifecyclePublication) PublishCurrentNodeCompletion(
	ctx context.Context,
	req CurrentNodeCompletionRequest,
	stage CurrentNodeCompletionPublicationStage,
) (CurrentNodeCompletionResult, LifecyclePublicationOutcome, error) {
	prepared, err := p.PrepareCurrentNodeCompletion(ctx, req, stage)
	if err != nil {
		return CurrentNodeCompletionResult{}, LifecyclePublicationOutcome{}, err
	}
	return prepared.Publish(ctx)
}

func (p *LifecyclePublication) PrepareCurrentNodeCompletion(
	ctx context.Context,
	req CurrentNodeCompletionRequest,
	stage CurrentNodeCompletionPublicationStage,
) (PreparedCurrentNodeCompletionPublication, error) {
	if stage == nil {
		return nil, errors.New("Current Node completion publication stage is required")
	}
	prepared, err := p.store.prepareCurrentNodeCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	delta, rollbackRuntime, err := stage(prepared.result)
	if err != nil {
		return nil, errors.Join(err, prepared.rollback())
	}
	if rollbackRuntime == nil {
		rollbackRuntime = func(error) {}
	}
	return &preparedCurrentNodeCompletionPublication{
		owner:           p,
		request:         req,
		prepared:        prepared,
		delta:           delta,
		rollbackRuntime: rollbackRuntime,
	}, nil
}

func (p *LifecyclePublication) PreviewCurrentNodeCompletion(
	ctx context.Context,
	req CurrentNodeCompletionRequest,
) (CurrentNodeCompletionResult, error) {
	prepared, err := p.store.prepareCurrentNodeCompletion(ctx, req)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	result := prepared.result
	if err := prepared.rollback(); err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	return result, nil
}

func (p *preparedCurrentNodeCompletionPublication) Result() CurrentNodeCompletionResult {
	if p == nil || p.prepared == nil {
		return CurrentNodeCompletionResult{}
	}
	return p.prepared.result
}

func (p *preparedCurrentNodeCompletionPublication) Publish(
	ctx context.Context,
) (CurrentNodeCompletionResult, LifecyclePublicationOutcome, error) {
	if p == nil || p.owner == nil || p.prepared == nil {
		return CurrentNodeCompletionResult{}, LifecyclePublicationOutcome{}, errors.New("prepared Current Node completion publication is required")
	}
	if err := p.owner.publishPrepared(ctx, p.prepared.preparedSQLLifecycleMutation, p.delta); err != nil {
		p.rollbackRuntime(err)
		return CurrentNodeCompletionResult{}, LifecyclePublicationOutcome{}, err
	}
	outcome := LifecyclePublicationOutcome{committed: true}
	if p.prepared.publishEvent {
		if err := p.owner.store.publishCurrentNodeTaskEvent(
			ctx,
			p.request.Source.TaskID,
			serverapi.WorkflowProjectEventActionCompleted,
		); err != nil {
			return p.prepared.result, outcome, err
		}
	}
	return p.prepared.result, outcome, nil
}

func (p *preparedCurrentNodeCompletionPublication) Rollback(cause error) error {
	if p == nil || p.prepared == nil {
		return errors.New("prepared Current Node completion publication is required")
	}
	p.rollbackRuntime(cause)
	return p.prepared.rollback()
}

func (p *LifecyclePublication) PublishPendingApproval(
	ctx context.Context,
	approvalID workflow.ApprovalID,
	stage PendingApprovalPublicationStage,
) (PendingApprovalApplyResult, error) {
	if stage == nil {
		return PendingApprovalApplyResult{}, errors.New("pending Approval publication stage is required")
	}
	prepared, err := p.store.preparePendingApprovalApply(ctx, approvalID)
	if err != nil {
		return PendingApprovalApplyResult{}, err
	}
	delta, rollbackRuntime, err := stage(prepared.result)
	if err != nil {
		return PendingApprovalApplyResult{}, errors.Join(err, prepared.rollback())
	}
	if rollbackRuntime == nil {
		rollbackRuntime = func(error) {}
	}
	if err := p.publishPrepared(ctx, prepared, delta); err != nil {
		rollbackRuntime(err)
		return PendingApprovalApplyResult{}, err
	}
	return prepared.result, nil
}

func (p *LifecyclePublication) PublishManualMove(
	ctx context.Context,
	prepared ManualMovePreparation,
	executionTarget *ExecutionTargetCandidate,
	stage ManualMovePublicationStage,
) (ManualMoveResult, error) {
	if stage == nil {
		return ManualMoveResult{}, errors.New("Manual Move publication stage is required")
	}
	apply, err := p.store.prepareManualMoveApply(ctx, prepared, executionTarget)
	if err != nil {
		return ManualMoveResult{}, err
	}
	if apply.mutation == nil {
		return apply.result, nil
	}
	delta, rollbackRuntime, err := stage(apply.result)
	if err != nil {
		return ManualMoveResult{}, errors.Join(err, apply.mutation.rollback())
	}
	if rollbackRuntime == nil {
		rollbackRuntime = func(error) {}
	}
	if err := p.publishPrepared(ctx, apply.mutation, delta); err != nil {
		rollbackRuntime(err)
		return ManualMoveResult{}, err
	}
	return apply.result, nil
}

func (p *LifecyclePublication) PublishCurrentNodeAdmission(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
) error {
	prepared, err := p.store.prepareCurrentNodeAdmission(ctx, reference)
	if err != nil {
		return err
	}
	delta, err := NewTaskLifecycleDelta(reference.TaskID, []LifecycleRunDelta{{
		CurrentNode: reference,
		Expect:      LifecycleFieldPresent,
		Next:        LifecycleFieldPresent,
	}}, nil)
	if err != nil {
		return errors.Join(err, prepared.rollback())
	}
	return p.publishPrepared(ctx, prepared, delta)
}

func (p *LifecyclePublication) PublishExactRegistration(
	ctx context.Context,
	exact LifecycleExactExecution,
	activation LifecycleExactRegistrationActivation,
) error {
	if len(exact.PendingPrompts) != 0 {
		return errors.New("Exact registration pending prompts must use typed prompt publication")
	}
	if err := validateLifecycleExactExecution(exact); err != nil {
		return err
	}
	if activation == nil {
		return errors.New("Exact registration activation is required")
	}
	if p == nil || p.store == nil {
		return errors.New("LifecyclePublication is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	delta, err := NewTaskLifecycleDelta(exact.CurrentNode.TaskID, []LifecycleRunDelta{{
		CurrentNode: exact.CurrentNode,
		Expect:      LifecycleFieldPresent,
		Next:        LifecycleFieldPresent,
	}}, []LifecycleExactDelta{{
		CurrentNode: exact.CurrentNode,
		ExpectScope: nil,
		Next:        &exact,
	}})
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrLifecyclePublicationClosed
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	candidate := cloneLifecycleRoot(p.root)
	if err := applyTaskLifecycleDelta(candidate, delta); err != nil {
		return err
	}
	if err := activation.Activate(); err != nil {
		return err
	}
	p.root = candidate
	return nil
}

func (p *LifecyclePublication) PublishExactPromptPending(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
	prompt LifecyclePendingPrompt,
) error {
	if scopeID.IsZero() {
		return errors.New("published Exact prompt scope id is required")
	}
	if err := validateLifecyclePendingPrompt(prompt); err != nil {
		return err
	}
	return p.patchExact(ctx, scopeID, &prompt, func(exact *LifecycleExactExecution) error {
		for _, current := range exact.PendingPrompts {
			if current.ID == prompt.ID {
				return fmt.Errorf("published Exact prompt %q is already pending", prompt.ID)
			}
		}
		exact.PendingPrompts = append(exact.PendingPrompts, lifecyclePendingPromptReference(prompt))
		sort.Slice(exact.PendingPrompts, func(i, j int) bool {
			return exact.PendingPrompts[i].ID < exact.PendingPrompts[j].ID
		})
		return validateLifecycleExactExecution(*exact)
	})
}

func (p *LifecyclePublication) PublishExactPromptResolved(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
	promptID string,
) error {
	promptID = strings.TrimSpace(promptID)
	if scopeID.IsZero() || promptID == "" {
		return errors.New("resolved Exact prompt scope and prompt id are required")
	}
	return p.patchExact(ctx, scopeID, nil, func(exact *LifecycleExactExecution) error {
		for index, prompt := range exact.PendingPrompts {
			if prompt.ID != promptID {
				continue
			}
			exact.PendingPrompts = append(
				exact.PendingPrompts[:index:index],
				exact.PendingPrompts[index+1:]...,
			)
			return nil
		}
		return fmt.Errorf("published Exact prompt %q is not pending", promptID)
	})
}

func (p *LifecyclePublication) PublishExactFinalizing(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
) error {
	if scopeID.IsZero() {
		return errors.New("finalizing Exact execution scope id is required")
	}
	return p.patchExact(ctx, scopeID, nil, func(exact *LifecycleExactExecution) error {
		if exact.Phase != LifecycleExactExecutionRunning {
			return fmt.Errorf("Exact execution scope %s is not running", scopeID)
		}
		exact.Phase = LifecycleExactExecutionFinalizing
		return nil
	})
}

func (p *LifecyclePublication) patchExact(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
	insertedPrompt *LifecyclePendingPrompt,
	patch func(*LifecycleExactExecution) error,
) error {
	if p == nil || p.store == nil {
		return errors.New("LifecyclePublication is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrLifecyclePublicationClosed
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	candidate := cloneLifecycleRoot(p.root)
	for taskID, entry := range candidate {
		for key, exact := range entry.exact {
			if exact.ScopeID != scopeID {
				continue
			}
			cloned := cloneLifecycleExactExecution(exact)
			if err := patch(&cloned); err != nil {
				return err
			}
			before := cloneLifecycleTaskEntry(entry)
			entry.exact[key] = cloned
			var insertedPayloads map[lifecycleQuestionPayloadKey]LifecyclePendingPrompt
			if insertedPrompt != nil {
				insertedPayloads = map[lifecycleQuestionPayloadKey]LifecyclePendingPrompt{
					{scopeID: scopeID, promptID: insertedPrompt.ID}: cloneLifecyclePendingPrompt(*insertedPrompt),
				}
			}
			if err := p.questionIndex.replaceTaskQuestions(
				ctx,
				taskID,
				before,
				entry,
				insertedPayloads,
			); err != nil {
				return err
			}
			candidate[taskID] = entry
			p.root = candidate
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrLifecycleExactNotPublished, scopeID)
}

func (p *LifecyclePublication) PublishCurrentNodeInterruption(
	ctx context.Context,
	references []workflow.CurrentNodeReference,
	predecessor CurrentNodeInterruptionPredecessor,
	expectedRun LifecycleFieldPresence,
	reason workflow.CurrentNodeInterruptionReason,
	detail workflow.CurrentNodeInterruptionDetail,
	expectedExact []LifecycleExactExecution,
) (LifecyclePublicationOutcome, error) {
	if !validLifecycleFieldPresence(expectedRun) {
		return LifecyclePublicationOutcome{}, errors.New("interruption expected Run presence is invalid")
	}
	prepared, err := p.store.prepareCurrentNodeInterruption(ctx, references, predecessor, reason, detail)
	if err != nil {
		return LifecyclePublicationOutcome{}, err
	}
	expectedByKey := make(map[workflow.CurrentNodeReferenceKey]runtimeids.ExecutionScopeID, len(expectedExact))
	for _, exact := range expectedExact {
		key, keyErr := exact.CurrentNode.Key()
		if keyErr != nil {
			return LifecyclePublicationOutcome{}, errors.Join(keyErr, prepared.rollback())
		}
		if exact.ScopeID.IsZero() {
			return LifecyclePublicationOutcome{}, errors.Join(errors.New("interruption expected Exact Execution Scope id is invalid"), prepared.rollback())
		}
		expectedByKey[key] = exact.ScopeID
	}
	runChanges := make([]LifecycleRunDelta, 0, len(references))
	exactChanges := make([]LifecycleExactDelta, 0, len(references))
	for _, reference := range references {
		key, keyErr := reference.Key()
		if keyErr != nil {
			return LifecyclePublicationOutcome{}, errors.Join(keyErr, prepared.rollback())
		}
		runChanges = append(runChanges, LifecycleRunDelta{
			CurrentNode: reference,
			Expect:      expectedRun,
			Next:        LifecycleFieldAbsent,
		})
		exactChange := LifecycleExactDelta{CurrentNode: reference}
		if scopeID, exists := expectedByKey[key]; exists {
			scope := scopeID
			exactChange.ExpectScope = &scope
		}
		exactChanges = append(exactChanges, exactChange)
	}
	delta, err := NewTaskLifecycleDelta(references[0].TaskID, runChanges, exactChanges)
	if err != nil {
		return LifecyclePublicationOutcome{}, errors.Join(err, prepared.rollback())
	}
	if err := p.publishPrepared(ctx, prepared.preparedSQLLifecycleMutation, delta); err != nil {
		return LifecyclePublicationOutcome{}, err
	}
	outcome := LifecyclePublicationOutcome{committed: true}
	err = p.store.publishCurrentNodeTaskEvent(
		ctx,
		references[0].TaskID,
		serverapi.WorkflowProjectEventActionInterrupted,
	)
	return outcome, err
}

func (p *LifecyclePublication) Publish(
	ctx context.Context,
	delta TaskLifecycleDelta,
) error {
	if p == nil || p.store == nil {
		return errors.New("LifecyclePublication is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrLifecyclePublicationClosed
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	candidate := cloneLifecycleRoot(p.root)
	if err := applyTaskLifecycleDelta(candidate, delta); err != nil {
		return err
	}
	p.root = candidate
	return nil
}

func (p *LifecyclePublication) PublishTaskDeletion(
	ctx context.Context,
	taskID workflow.TaskID,
) (DeleteTaskResult, error) {
	if p == nil || p.store == nil {
		return DeleteTaskResult{}, errors.New("LifecyclePublication is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	prepared, err := p.store.prepareTaskDeletion(ctx, taskID)
	if err != nil {
		return DeleteTaskResult{}, err
	}
	if err := p.publishPreparedTaskSetDeletion(
		ctx,
		prepared.preparedSQLLifecycleMutation,
		[]workflow.TaskID{taskID},
	); err != nil {
		return DeleteTaskResult{}, err
	}
	return prepared.result, nil
}

func (p *LifecyclePublication) PublishWorkflowDeletion(
	ctx context.Context,
	req WorkflowDeleteRequest,
) (WorkflowDeleteResult, error) {
	if p == nil || p.store == nil {
		return WorkflowDeleteResult{}, errors.New("LifecyclePublication is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result, prepared, err := p.store.prepareWorkflowDeletion(ctx, req)
	if err != nil || prepared == nil {
		return result, err
	}
	if err := p.publishPreparedTaskSetDeletion(
		ctx,
		prepared.preparedSQLLifecycleMutation,
		prepared.taskIDs,
	); err != nil {
		return WorkflowDeleteResult{}, err
	}
	return prepared.result, nil
}

func (p *LifecyclePublication) PublishProjectDeletion(
	ctx context.Context,
	req ProjectDeleteRequest,
) ([]serverapi.ProjectDeleteBlocker, error) {
	if p == nil || p.store == nil {
		return nil, errors.New("LifecyclePublication is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	blockers, prepared, err := p.store.prepareProjectDeletion(ctx, req)
	if err != nil || prepared == nil {
		return blockers, err
	}
	defer prepared.releaseRuntimeBlocker()
	if err := p.publishPreparedTaskSetDeletion(ctx, prepared, prepared.taskIDs); err != nil {
		return nil, err
	}
	if err := prepared.finalize(); err != nil {
		return nil, err
	}
	return nil, nil
}

func NewLifecyclePublication(store *Store) (*LifecyclePublication, error) {
	if store == nil {
		return nil, errors.New("lifecycle publication store is required")
	}
	questionIndex, err := openLifecycleQuestionIndex(
		context.Background(),
		store.metadata.PersistenceRoot(),
	)
	if err != nil {
		return nil, err
	}
	return &LifecyclePublication{
		store:         store,
		root:          make(lifecycleRoot),
		questionIndex: questionIndex,
	}, nil
}

func (p *LifecyclePublication) PublishResume(
	ctx context.Context,
	delta QueuedTaskLifecycleDelta,
) ([]InterruptedCurrentNodeAttentionProjection, error) {
	if p == nil || p.store == nil {
		return nil, errors.New("LifecyclePublication is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if delta.delta.taskID == "" || len(delta.delta.runs) == 0 {
		return nil, errors.New("queued lifecycle delta is required")
	}
	p.mu.RLock()
	closed := p.closed
	p.mu.RUnlock()
	if closed {
		return nil, ErrLifecyclePublicationClosed
	}
	references := delta.QueuedCurrentNodes()
	prepared, err := p.store.prepareTaskResume(ctx, references)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errors.Join(ErrLifecyclePublicationClosed, prepared.rollback())
	}
	candidate := cloneLifecycleRoot(p.root)
	if err := applyTaskLifecycleDelta(candidate, delta.delta); err != nil {
		return nil, errors.Join(err, prepared.rollback())
	}
	if err := context.Cause(ctx); err != nil {
		return nil, errors.Join(err, prepared.rollback())
	}
	if err := prepared.commit(); err != nil {
		return nil, err
	}
	p.root = candidate
	return prepared.interruptedAttention(), nil
}

func cloneLifecycleRoot(source lifecycleRoot) lifecycleRoot {
	cloned := make(lifecycleRoot, len(source))
	for taskID, entry := range source {
		cloned[taskID] = cloneLifecycleTaskEntry(entry)
	}
	return cloned
}

func cloneLifecycleTaskEntry(source lifecycleTaskEntry) lifecycleTaskEntry {
	cloned := lifecycleTaskEntry{
		runs:  make(map[workflow.CurrentNodeReferenceKey]workflow.CurrentNodeReference, len(source.runs)),
		exact: make(map[workflow.CurrentNodeReferenceKey]LifecycleExactExecution, len(source.exact)),
	}
	for key, reference := range source.runs {
		cloned.runs[key] = reference
	}
	for key, exact := range source.exact {
		cloned.exact[key] = cloneLifecycleExactExecution(exact)
	}
	return cloned
}

func applyTaskLifecycleDelta(root lifecycleRoot, delta TaskLifecycleDelta) error {
	if delta.taskID == "" {
		return errors.New("lifecycle delta Task id is required")
	}
	before := root[delta.taskID]
	entry := cloneLifecycleTaskEntry(before)
	for _, change := range delta.runs {
		key, err := change.CurrentNode.Key()
		if err != nil {
			return err
		}
		_, present := entry.runs[key]
		if present != (change.Expect == LifecycleFieldPresent) {
			return fmt.Errorf("lifecycle Run predecessor conflict for Current Node %v", change.CurrentNode)
		}
		if change.Next == LifecycleFieldPresent {
			entry.runs[key] = change.CurrentNode
		} else {
			delete(entry.runs, key)
		}
	}
	for _, change := range delta.exact {
		key, err := change.CurrentNode.Key()
		if err != nil {
			return err
		}
		current, present := entry.exact[key]
		switch {
		case change.ExpectScope == nil && present:
			return fmt.Errorf("lifecycle Exact predecessor conflict for Current Node %v: expected absent, found %s", change.CurrentNode, current.ScopeID)
		case change.ExpectScope != nil && (!present || current.ScopeID != *change.ExpectScope):
			return fmt.Errorf("lifecycle Exact predecessor conflict for Current Node %v: expected %s", change.CurrentNode, *change.ExpectScope)
		}
		if change.Next == nil {
			delete(entry.exact, key)
		} else {
			entry.exact[key] = cloneLifecycleExactExecution(*change.Next)
		}
	}
	if len(entry.runs) == 0 && len(entry.exact) == 0 {
		delete(root, delta.taskID)
	} else {
		root[delta.taskID] = entry
	}
	return validateLifecycleQuestionFactsUnchanged(delta.taskID, before, entry)
}

func cloneCurrentNodeReferences(source []workflow.CurrentNodeReference) []workflow.CurrentNodeReference {
	return append([]workflow.CurrentNodeReference(nil), source...)
}

type preparedTaskResume struct {
	*preparedSQLLifecycleMutation
	attention []InterruptedCurrentNodeAttentionProjection
}

func (s *Store) prepareTaskResume(
	ctx context.Context,
	references []workflow.CurrentNodeReference,
) (*preparedTaskResume, error) {
	taskID := references[0].TaskID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	rollbackOnError := func(cause error) (*preparedTaskResume, error) {
		rollbackErr := tx.Rollback()
		if errors.Is(rollbackErr, sql.ErrTxDone) {
			rollbackErr = nil
		}
		return nil, errors.Join(cause, rollbackErr)
	}
	q := s.queries.WithTx(tx)
	prepared := &preparedTaskResume{
		preparedSQLLifecycleMutation: newPreparedSQLLifecycleMutation(tx),
	}
	for _, reference := range references {
		if err := reference.Validate(); err != nil {
			return rollbackOnError(err)
		}
		if reference.TaskID != taskID {
			return rollbackOnError(errors.New("task Resume Current Nodes must belong to one Task"))
		}
		projection, found, err := prepareCurrentNodeResume(ctx, q, reference)
		if err != nil {
			return rollbackOnError(err)
		}
		if found {
			prepared.attention = append(prepared.attention, projection)
		}
	}
	return prepared, nil
}

func prepareCurrentNodeResume(
	ctx context.Context,
	q *sqlitegen.Queries,
	reference workflow.CurrentNodeReference,
) (InterruptedCurrentNodeAttentionProjection, bool, error) {
	branchKey, branchScoped := reference.TransitionBranchKey()
	var branchValue any
	if branchScoped {
		branchValue = string(branchKey)
	}
	locked, err := q.AcquireCurrentNodeResumeWriteLock(ctx, sqlitegen.AcquireCurrentNodeResumeWriteLockParams{
		TaskID:              string(reference.TaskID),
		NodeID:              string(reference.NodeID),
		TransitionBranchKey: branchValue,
	})
	if err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	if locked != 1 {
		return InterruptedCurrentNodeAttentionProjection{}, false, sql.ErrNoRows
	}
	currentNode, err := currentNodeForReference(ctx, q, reference)
	if err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	if err := ensureCurrentNodeSessionAssociation(ctx, q, currentNode); err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	projection, found, err := pendingInterruptedCurrentNodeAttentionProjection(ctx, q, reference)
	if err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	var resumed int64
	if branchScoped {
		resumed, err = q.ResumeBranchCurrentNode(ctx, sqlitegen.ResumeBranchCurrentNodeParams{
			TaskID:              string(reference.TaskID),
			NodeID:              string(reference.NodeID),
			TransitionBranchKey: sql.NullString{String: string(branchKey), Valid: true},
		})
	} else {
		resumed, err = q.ResumeSerialCurrentNode(ctx, sqlitegen.ResumeSerialCurrentNodeParams{
			TaskID: string(reference.TaskID),
			NodeID: string(reference.NodeID),
		})
	}
	if err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	if resumed != 1 {
		return InterruptedCurrentNodeAttentionProjection{}, false, sql.ErrNoRows
	}
	return projection, found, nil
}

func (p *preparedTaskResume) interruptedAttention() []InterruptedCurrentNodeAttentionProjection {
	return append([]InterruptedCurrentNodeAttentionProjection(nil), p.attention...)
}

type LifecycleCapture interface {
	CurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
	TaskIDs() []workflow.TaskID
	QueuedCurrentNodes(workflow.TaskID) []workflow.CurrentNodeReference
	ExactExecutions(workflow.TaskID) []LifecycleExactExecution
	WithQueries(func(*sqlitegen.Queries) error) error
	Close() error
}

type LifecycleBoundedReadCapture interface {
	CurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
	QueuedCurrentNodes(workflow.TaskID) []workflow.CurrentNodeReference
	ExactExecutions(workflow.TaskID) []LifecycleExactExecution
	PendingQuestions(context.Context, LifecycleQuestionCursor, int) ([]LifecyclePendingQuestion, error)
	PendingQuestionsForTask(context.Context, workflow.TaskID) ([]LifecyclePendingQuestion, error)
}

// LifecycleCapture pins one immutable runtime root and its matching durable
// SQLite read snapshot until Close.
type lifecycleCapture struct {
	durable          *lifecycleReadSnapshot
	questionSnapshot *lifecycleQuestionReadSnapshot
	root             lifecycleRoot
	token            string
	release          func()
	mu               sync.Mutex
	closed           bool
}

func (p *LifecyclePublication) Capture(ctx context.Context) (LifecycleCapture, error) {
	return p.capture(ctx)
}

func (p *LifecyclePublication) CaptureQuery(
	ctx context.Context,
	operation func(string, *sqlitegen.Queries) error,
) (err error) {
	if operation == nil {
		return errors.New("lifecycle query operation is required")
	}
	capture, err := p.capture(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := capture.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	token, err := capture.stateToken()
	if err != nil {
		return err
	}
	return capture.WithQueries(func(queries *sqlitegen.Queries) error {
		return operation(token, queries)
	})
}

func (p *LifecyclePublication) CaptureBoundedRead(
	ctx context.Context,
	operation func(string, LifecycleBoundedReadCapture, *sqlitegen.Queries) error,
) (err error) {
	if operation == nil {
		return errors.New("bounded lifecycle read operation is required")
	}
	capture, err := p.capture(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := capture.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	token, err := capture.stateToken()
	if err != nil {
		return err
	}
	capture.mu.Lock()
	if capture.closed || capture.durable == nil || capture.durable.queries == nil {
		capture.mu.Unlock()
		return errors.New("lifecycle capture is closed")
	}
	queries := capture.durable.queries
	capture.mu.Unlock()
	return operation(token, capture, queries)
}

func (p *LifecyclePublication) capture(ctx context.Context) (*lifecycleCapture, error) {
	if p == nil || p.store == nil {
		return nil, errors.New("LifecyclePublication is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return nil, ErrLifecyclePublicationClosed
	}
	durable, err := p.store.beginLifecycleRead(ctx)
	if err != nil {
		p.mu.RUnlock()
		return nil, err
	}
	questionSnapshot, err := p.questionIndex.beginRead(ctx)
	if err != nil {
		p.mu.RUnlock()
		return nil, errors.Join(err, durable.close())
	}
	root := p.root
	token, release, err := sqlitegen.RegisterLifecycleTaskStateResolver(func(taskID string) (sqlitegen.LifecycleTaskQueryState, error) {
		return lifecycleTaskState(root, taskID)
	})
	if err != nil {
		p.mu.RUnlock()
		return nil, errors.Join(err, durable.close(), questionSnapshot.close())
	}
	capture := &lifecycleCapture{
		durable:          durable,
		questionSnapshot: questionSnapshot,
		root:             root,
		token:            token,
		release:          release,
	}
	p.mu.RUnlock()
	return capture, nil
}

func lifecycleTaskState(root lifecycleRoot, rawTaskID string) (sqlitegen.LifecycleTaskQueryState, error) {
	taskID := workflow.TaskID(strings.TrimSpace(rawTaskID))
	if taskID == "" || string(taskID) != rawTaskID {
		return sqlitegen.LifecycleTaskQueryState{}, errors.New("lifecycle Task state id is invalid")
	}
	entry, owned := root[taskID]
	if !owned {
		return sqlitegen.LifecycleTaskQueryState{}, nil
	}
	queued := make([]workflow.CurrentNodeReference, 0, len(entry.runs))
	for _, reference := range entry.runs {
		queued = append(queued, reference)
	}
	executions := make([]LifecycleTaskExecutionStatus, 0, len(entry.exact))
	for _, exact := range entry.exact {
		execution := LifecycleTaskExecutionStatus{CurrentNode: exact.CurrentNode}
		for _, prompt := range exact.PendingPrompts {
			switch prompt.Kind {
			case LifecyclePendingPromptQuestion:
				execution.WaitingQuestion = true
			case LifecyclePendingPromptSessionApproval:
				execution.WaitingApproval = true
			default:
				return sqlitegen.LifecycleTaskQueryState{}, fmt.Errorf("lifecycle Task %q has invalid pending prompt kind %d", taskID, prompt.Kind)
			}
		}
		executions = append(executions, execution)
	}
	status, err := DeriveLifecycleTaskStatus(taskID, queued, executions)
	if err != nil {
		return sqlitegen.LifecycleTaskQueryState{}, err
	}
	flags := sqlitegen.LifecycleTaskStateOwned
	if status.HasRunning {
		flags |= sqlitegen.LifecycleTaskStateRunning
	}
	if status.HasQueued {
		flags |= sqlitegen.LifecycleTaskStateQueued
	}
	if status.WaitingQuestion {
		flags |= sqlitegen.LifecycleTaskStateWaitingQuestion
	}
	if status.WaitingApproval {
		flags |= sqlitegen.LifecycleTaskStateWaitingApproval
	}
	return sqlitegen.LifecycleTaskQueryState{Present: true, Flags: flags}, nil
}

func (p *LifecyclePublication) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.root = nil
	err := p.questionIndex.close()
	p.questionIndex = nil
	return err
}

func (c *lifecycleCapture) CurrentNodes(
	ctx context.Context,
	taskID workflow.TaskID,
) ([]workflow.CurrentNode, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.durable == nil {
		return nil, errors.New("lifecycle capture is closed")
	}
	return c.durable.currentNodes(ctx, taskID)
}

func (c *lifecycleCapture) TaskIDs() []workflow.TaskID {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	taskIDs := make([]workflow.TaskID, 0, len(c.root))
	for taskID := range c.root {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Slice(taskIDs, func(i, j int) bool {
		return taskIDs[i] < taskIDs[j]
	})
	return taskIDs
}

func (c *lifecycleCapture) QueuedCurrentNodes(taskID workflow.TaskID) []workflow.CurrentNodeReference {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	entry := c.root[taskID]
	queued := make([]workflow.CurrentNodeReference, 0, len(entry.runs))
	for key, reference := range entry.runs {
		if _, running := entry.exact[key]; !running {
			queued = append(queued, reference)
		}
	}
	return queued
}

func (c *lifecycleCapture) ExactExecutions(taskID workflow.TaskID) []LifecycleExactExecution {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	entry := c.root[taskID]
	exact := make([]LifecycleExactExecution, 0, len(entry.exact))
	for _, execution := range entry.exact {
		exact = append(exact, cloneLifecycleExactExecution(execution))
	}
	return exact
}

func (c *lifecycleCapture) PendingQuestions(
	ctx context.Context,
	cursor LifecycleQuestionCursor,
	limit int,
) ([]LifecyclePendingQuestion, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("lifecycle capture is closed")
	}
	return lifecycleQuestionPage(ctx, c.questionSnapshot, c.root, cursor, limit)
}

func (c *lifecycleCapture) PendingQuestionsForTask(
	ctx context.Context,
	taskID workflow.TaskID,
) ([]LifecyclePendingQuestion, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("lifecycle capture is closed")
	}
	return lifecycleQuestionsForTask(ctx, c.questionSnapshot, c.root, taskID)
}

func (c *lifecycleCapture) stateToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.token == "" {
		return "", errors.New("lifecycle capture is closed")
	}
	return c.token, nil
}

func (c *lifecycleCapture) WithQueries(operation func(*sqlitegen.Queries) error) error {
	if operation == nil {
		return errors.New("lifecycle capture query operation is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.durable == nil || c.durable.queries == nil {
		return errors.New("lifecycle capture is closed")
	}
	return operation(c.durable.queries)
}

func (c *lifecycleCapture) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	err := errors.Join(c.durable.close(), c.questionSnapshot.close())
	if c.release != nil {
		c.release()
	}
	c.durable = nil
	c.questionSnapshot = nil
	c.root = nil
	c.token = ""
	c.release = nil
	return err
}

type lifecycleReadSnapshot struct {
	mu      sync.Mutex
	tx      *sql.Tx
	queries *sqlitegen.Queries
}

func (s *Store) beginLifecycleRead(ctx context.Context) (*lifecycleReadSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin lifecycle read snapshot: %w", err)
	}
	q := s.queries.WithTx(tx)
	if _, err := q.AnchorLifecycleReadSnapshot(ctx); err != nil {
		return nil, errors.Join(err, tx.Rollback())
	}
	return &lifecycleReadSnapshot{tx: tx, queries: q}, nil
}

func (s *lifecycleReadSnapshot) currentNodes(
	ctx context.Context,
	taskID workflow.TaskID,
) ([]workflow.CurrentNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tx == nil || s.queries == nil {
		return nil, errors.New("lifecycle read snapshot is closed")
	}
	byTask, err := ListCurrentNodesByTaskWithQueries(ctx, s.queries, []workflow.TaskID{taskID})
	if err != nil {
		return nil, err
	}
	return byTask[taskID], nil
}

func (s *lifecycleReadSnapshot) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tx == nil {
		return nil
	}
	err := s.tx.Rollback()
	s.tx = nil
	s.queries = nil
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}
