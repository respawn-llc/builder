package workflowsvc

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"core/prompts"
	"core/server/requestmemo"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowattention"
	"core/server/workflowscript"
	"core/server/workflowstore"
	"core/server/workflowview"
	"core/server/worktree"
	"core/shared/clientui"
	"core/shared/serverapi"
	"github.com/google/uuid"
)

type Service struct {
	store               *workflowstore.Store
	view                *workflowview.Service
	roleResolver        workflow.RoleResolver
	taskWorktrees       taskWorktreeEnsurer
	taskWorktreeCleanup taskWorktreeDeleter
	runtimeCancel       taskRuntimeCanceler
	schedulerWake       schedulerNotifier
	events              *workflowProjectEventBroker
	prompts             pendingPromptResponder
	approve             transitionApprover
	attentionFinalizer  workflowAttentionFinalizer
	targetResolver      taskExecutionTargetResolver
	targetWorktrees     taskExecutionTargetWorktreeMaterializer
	questionMemo        *requestmemo.Memo[taskQuestionAnswerMemoRequest, struct{}]
}

type taskWorktreeEnsurer interface {
	EnsureTaskWorktree(ctx context.Context, req TaskWorktreeEnsureRequest) error
}

type taskExecutionTargetResolver interface {
	ResolveExecutionTarget(ctx context.Context, workspaceRoot string, policy workflow.ExecutionPolicyMode, customRef *string) (worktree.ExecutionTargetResolution, error)
}

type taskExecutionTargetWorktreeMaterializer interface {
	PlanExecutionTargetWorktreeRoot(workspaceID string, taskShortID string) (string, error)
	ProvisionExecutionTargetWorktree(context.Context, worktree.ProvisionExecutionTargetWorktreeRequest) (worktree.ProvisionExecutionTargetWorktreeResponse, error)
	ReprovisionExecutionTargetWorktree(context.Context, worktree.ProvisionExecutionTargetWorktreeRequest) (worktree.ProvisionExecutionTargetWorktreeResponse, error)
	InspectExecutionTargetWorktree(context.Context, worktree.InspectExecutionTargetWorktreeRequest) (worktree.ExecutionTargetWorktreeInspection, error)
	RunExecutionTargetSetup(context.Context, worktree.RunExecutionTargetSetupRequest) error
}

type TaskWorktreeEnsureRequest struct {
	TaskID           workflow.TaskID
	SetupOperationID serverapi.WorktreeSetupOperationID
}

type taskWorktreeEnsureError struct {
	err error
}

func (e taskWorktreeEnsureError) Error() string {
	return e.err.Error()
}

func (e taskWorktreeEnsureError) Unwrap() error {
	return e.err
}

type taskWorktreeDeleter interface {
	EnsureTaskWorktreeDeletable(ctx context.Context, taskID string) error
	DeleteTaskWorktree(ctx context.Context, taskID string) error
}

type taskRuntimeCanceler interface {
	CancelTaskRuns(ctx context.Context, taskID workflow.TaskID) error
}

type taskRuntimeRunCanceler interface {
	CancelRun(ctx context.Context, runID workflow.RunID) error
}

type taskRuntimeRunCancelRequester interface {
	RequestCancelRun(runID workflow.RunID) bool
}

type schedulerNotifier interface {
	Notify()
}

type transitionApprover func(ctx context.Context, transitionID workflow.TransitionID) (workflowstore.CompleteRunResult, error)

type pendingPromptResponder interface {
	SubmitPromptResponse(sessionID string, resp askquestion.AskQuestionResponse, err error) error
}

type workflowAttentionFinalizer interface {
	FinalizeTransition(context.Context, workflowattention.TransitionResult)
	FinalizeInterruptedRun(context.Context, workflow.RunID)
}

type workflowInterruptedRunFinalizer interface {
	ResolveInterruptedRun(context.Context, workflow.RunID)
}

const workflowAttentionResolutionPageSize = 200
const workflowAttentionFinalizationTimeout = 5 * time.Second
const executionTargetRecoveryFencePageSize = 200
const executionTargetRecoveryWorkerCapacity = 1

type executionTargetRecoveryCoordinator struct {
	service *Service
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}

	mu       sync.Mutex
	closeErr error
}

type taskQuestionAnswerMemoRequest struct {
	TaskID               string
	RunID                string
	AskID                string
	ErrorMessage         string
	Answer               string
	SelectedOptionNumber int
	FreeformAnswer       string
	ApprovalDecision     clientui.ApprovalDecision
	ApprovalCommentary   string
}

type Option func(*Service)

func WithTaskWorktreeEnsurer(ensurer taskWorktreeEnsurer) Option {
	return func(s *Service) {
		s.taskWorktrees = ensurer
	}
}

func WithTaskExecutionTargetResolver(resolver taskExecutionTargetResolver) Option {
	return func(s *Service) {
		s.targetResolver = resolver
	}
}

func WithTaskExecutionTargetWorktreeMaterializer(materializer taskExecutionTargetWorktreeMaterializer) Option {
	return func(s *Service) {
		s.targetWorktrees = materializer
	}
}

func WithTaskWorktreeDeleter(deleter taskWorktreeDeleter) Option {
	return func(s *Service) {
		s.taskWorktreeCleanup = deleter
	}
}

func WithTaskRuntimeCanceler(canceler taskRuntimeCanceler) Option {
	return func(s *Service) {
		s.runtimeCancel = canceler
	}
}

func WithSchedulerNotifier(notifier schedulerNotifier) Option {
	return func(s *Service) {
		s.schedulerWake = notifier
	}
}

func WithPromptResponder(responder pendingPromptResponder) Option {
	return func(s *Service) {
		s.prompts = responder
	}
}

func WithWorkflowAttentionFinalizer(finalizer workflowAttentionFinalizer) Option {
	return func(s *Service) {
		s.attentionFinalizer = finalizer
	}
}

func New(store *workflowstore.Store, view *workflowview.Service, roleResolver workflow.RoleResolver, opts ...Option) (*Service, error) {
	if store == nil {
		return nil, errors.New("workflow store is required")
	}
	if view == nil {
		return nil, errors.New("workflow view is required")
	}
	events := newWorkflowProjectEventBroker()
	store.SetWorkflowEventPublisher(events)
	service := &Service{store: store, view: view, roleResolver: roleResolver, events: events, approve: store.ApproveTransition, targetResolver: worktree.NewGitInspector(nil), questionMemo: requestmemo.New[taskQuestionAnswerMemoRequest, struct{}]()}
	for _, opt := range opts {
		opt(service)
	}
	return service, nil
}

// FenceExecutionTargetRecovery establishes the database-only startup fence for
// orphaned target materialization before scheduler admission begins.
func (s *Service) FenceExecutionTargetRecovery(ctx context.Context) error {
	for {
		taskIDs, err := s.store.FenceExecutionTargetRecovery(ctx, executionTargetRecoveryFencePageSize)
		if err != nil {
			return err
		}
		if len(taskIDs) == 0 {
			return nil
		}
		for _, taskID := range taskIDs {
			detail, err := s.view.GetTask(ctx, string(taskID))
			if err != nil {
				return fmt.Errorf("get fenced execution target task %s: %w", taskID, err)
			}
			s.publishWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, "task", "updated", string(taskID))
		}
	}
}

// StartExecutionTargetRecovery starts the bounded phase-two owner for durable
// recovery claims. It only advances target recovery facts; it never recreates
// an initiating start, move, or approval action.
func (s *Service) StartExecutionTargetRecovery(ctx context.Context) (*executionTargetRecoveryCoordinator, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("workflow service store is required")
	}
	if s.targetWorktrees == nil {
		return nil, errors.New("execution target worktree materializer is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	recoveryContext, cancel := context.WithCancel(ctx)
	coordinator := &executionTargetRecoveryCoordinator{
		service: s,
		ctx:     recoveryContext,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	go coordinator.run()
	return coordinator, nil
}

func (c *executionTargetRecoveryCoordinator) Close() error {
	if c == nil {
		return nil
	}
	c.cancel()
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}

func (c *executionTargetRecoveryCoordinator) run() {
	defer close(c.done)
	for {
		if err := c.ctx.Err(); err != nil {
			return
		}
		targets, err := c.service.store.ListQueuedExecutionTargetRecoveries(c.ctx, executionTargetRecoveryWorkerCapacity)
		if err != nil {
			if c.ctx.Err() != nil {
				return
			}
			c.recordCloseError(fmt.Errorf("list queued execution target recoveries: %w", err))
			return
		}
		if len(targets) == 0 {
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-c.ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
			continue
		}
		target := targets[0]
		if target.ActiveClaim == nil {
			c.recordCloseError(errors.New("queued execution target recovery is missing its claim"))
			return
		}
		claimed, err := c.service.store.ClaimQueuedExecutionTargetRecovery(c.ctx, target.TaskID, *target.ActiveClaim)
		if errors.Is(err, workflowstore.ErrTaskExecutionTargetClaimChanged) {
			continue
		}
		if err != nil {
			c.recordCloseError(fmt.Errorf("claim execution target recovery: %w", err))
			return
		}
		if claimed.ActiveClaim == nil {
			c.recordCloseError(errors.New("claimed execution target recovery is missing its claim"))
			return
		}
		if err := c.recover(claimed, *claimed.ActiveClaim); err != nil {
			if requeueErr := c.requeue(claimed.TaskID, *claimed.ActiveClaim); requeueErr != nil {
				c.recordCloseError(requeueErr)
				return
			}
			if c.ctx.Err() != nil {
				return
			}
			if errors.Is(err, workflowstore.ErrTaskExecutionTargetClaimChanged) {
				continue
			}
			c.recordCloseError(err)
			return
		}
	}
}

func (c *executionTargetRecoveryCoordinator) recover(target workflow.ExecutionTarget, claim workflow.ExecutionTargetClaim) error {
	if claim.Phase != workflow.ExecutionTargetClaimRecovering {
		return errors.New("execution target recovery requires a recovering claim")
	}
	recovery, err := c.service.store.ExecutionTargetRecoveryContext(c.ctx, target.TaskID)
	if err != nil {
		return fmt.Errorf("load execution target recovery context: %w", err)
	}
	if recovery.Target.ActiveClaim == nil || *recovery.Target.ActiveClaim != claim {
		return workflowstore.ErrTaskExecutionTargetClaimChanged
	}
	switch recovery.Target.State {
	case workflow.ExecutionTargetStateInitialProvisioning:
		if recovery.Target.ResolvedSource == nil || recovery.Target.IntendedWorktreeRoot == nil {
			return errors.New("initial execution target recovery is missing immutable Git facts")
		}
		inspection, err := c.service.targetWorktrees.InspectExecutionTargetWorktree(c.ctx, worktree.InspectExecutionTargetWorktreeRequest{
			SourceWorkspaceRoot: recovery.SourceWorkspace.Root,
			WorktreeRoot:        *recovery.Target.IntendedWorktreeRoot,
			TaskShortID:         recovery.TaskShortID,
			ResolvedCommit:      recovery.Target.ResolvedSource.Commit,
		})
		if err != nil {
			if c.ctx.Err() == nil {
				return c.markManualRecovery(recovery.Target, claim, workflow.ExecutionTargetRecoveryCauseInspectionFailed)
			}
			return err
		}
		switch inspection.Kind {
		case worktree.ExecutionTargetWorktreeInspectionNoSideEffects:
			if err := c.service.store.DeleteInitialExecutionTargetRecovery(c.ctx, recovery.Target.TaskID, claim); err != nil {
				return fmt.Errorf("delete unprovisioned initial execution target recovery: %w", err)
			}
			c.service.publishExecutionTargetRecoveryUpdate(c.ctx, recovery.Target.TaskID)
			return nil
		case worktree.ExecutionTargetWorktreeInspectionExact:
			if inspection.BranchName != recovery.TaskShortID {
				return c.markManualRecovery(recovery.Target, claim, workflow.ExecutionTargetRecoveryCauseAmbiguousProvisioning)
			}
			return c.attachAndRecoverInitialSetup(recovery, claim, inspection)
		case worktree.ExecutionTargetWorktreeInspectionAmbiguous:
			return c.markManualRecovery(recovery.Target, claim, workflow.ExecutionTargetRecoveryCauseAmbiguousProvisioning)
		default:
			return c.markManualRecovery(recovery.Target, claim, workflow.ExecutionTargetRecoveryCauseInspectionFailed)
		}
	case workflow.ExecutionTargetStateLocked:
		return c.recoverLockedTarget(recovery, claim)
	default:
		return c.markManualRecovery(recovery.Target, claim, workflow.ExecutionTargetRecoveryCauseUnsupportedState)
	}
}

func (c *executionTargetRecoveryCoordinator) attachAndRecoverInitialSetup(recovery workflowstore.ExecutionTargetRecoveryContext, claim workflow.ExecutionTargetClaim, inspection worktree.ExecutionTargetWorktreeInspection) error {
	if recovery.Target.IntendedWorktreeRoot == nil {
		return errors.New("initial execution target recovery is missing intended worktree root")
	}
	if strings.TrimSpace(inspection.ExactBranchObservation) == "" || inspection.LinkedWorktreeOwnership == nil {
		return c.markManualRecovery(recovery.Target, claim, workflow.ExecutionTargetRecoveryCauseAmbiguousProvisioning)
	}
	target := recovery.Target
	target.State = workflow.ExecutionTargetStateLocked
	target.IntendedWorktreeRoot = nil
	target.ExactBranchObservation = stringPointer(inspection.ExactBranchObservation)
	target.LinkedWorktreeOwnership = inspection.LinkedWorktreeOwnership
	attached, err := c.service.store.AttachManagedExecutionTargetWorktree(c.ctx, workflowstore.AttachManagedExecutionTargetWorktreeRequest{
		Target:        target,
		ExpectedClaim: claim,
		WorkspaceID:   recovery.SourceWorkspace.ID,
		WorktreeRoot:  *recovery.Target.IntendedWorktreeRoot,
		CreatedBranch: true,
	})
	if err != nil {
		return fmt.Errorf("attach exact recovered execution target worktree: %w", err)
	}
	c.service.publishExecutionTargetRecoveryUpdate(c.ctx, target.TaskID)
	return c.recoverAttachedSetup(recovery, target, claim, attached, inspection.BranchName)
}

func (c *executionTargetRecoveryCoordinator) recoverAttachedSetup(recovery workflowstore.ExecutionTargetRecoveryContext, target workflow.ExecutionTarget, claim workflow.ExecutionTargetClaim, attached workflow.ExecutionWorktree, branchName string) error {
	switch target.SetupState {
	case workflow.ExecutionTargetSetupPending:
		target.SetupState = workflow.ExecutionTargetSetupRunning
		if err := c.service.store.UpdateTaskExecutionTargetLifecycle(c.ctx, target, claim); err != nil {
			return fmt.Errorf("mark recovered execution target setup running: %w", err)
		}
		c.service.publishExecutionTargetRecoveryUpdate(c.ctx, target.TaskID)
		err := c.service.targetWorktrees.RunExecutionTargetSetup(c.ctx, worktree.RunExecutionTargetSetupRequest{
			SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
			SourceWorkspaceRoot: recovery.SourceWorkspace.Root,
			WorktreeRoot:        attached.Root,
			BranchName:          branchName,
			ProjectID:           recovery.ProjectID,
			WorkspaceID:         recovery.SourceWorkspace.ID,
			WorktreeID:          attached.ID,
			CreatedBranch:       true,
		})
		if err != nil {
			if c.ctx.Err() != nil {
				return err
			}
			target.SetupState = workflow.ExecutionTargetSetupFailed
			target.ActiveClaim = nil
			if updateErr := c.service.store.UpdateTaskExecutionTargetLifecycle(c.ctx, target, claim); updateErr != nil {
				return errors.Join(err, fmt.Errorf("record recovered execution target setup failure: %w", updateErr))
			}
			c.service.publishExecutionTargetRecoveryUpdate(c.ctx, target.TaskID)
			return nil
		}
		target.SetupState = workflow.ExecutionTargetSetupSucceeded
	case workflow.ExecutionTargetSetupRunning:
		target.SetupState = workflow.ExecutionTargetSetupFailed
	case workflow.ExecutionTargetSetupSucceeded, workflow.ExecutionTargetSetupFailed:
	default:
		return c.markManualRecovery(target, claim, workflow.ExecutionTargetRecoveryCauseUnsupportedState)
	}
	target.ActiveClaim = nil
	if err := c.service.store.UpdateTaskExecutionTargetLifecycle(c.ctx, target, claim); err != nil {
		return fmt.Errorf("complete recovered execution target setup: %w", err)
	}
	c.service.publishExecutionTargetRecoveryUpdate(c.ctx, target.TaskID)
	return nil
}

func (c *executionTargetRecoveryCoordinator) recoverLockedTarget(recovery workflowstore.ExecutionTargetRecoveryContext, claim workflow.ExecutionTargetClaim) error {
	if recovery.Target.ResolvedSource == nil {
		return c.markManualRecovery(recovery.Target, claim, workflow.ExecutionTargetRecoveryCauseUnsupportedState)
	}
	root, err := c.service.store.ResolveTaskExecutionRoot(c.ctx, recovery.Target.TaskID)
	if err != nil {
		if errors.Is(err, workflowstore.ErrTaskExecutionRootUnavailable) {
			return c.markManualRecovery(recovery.Target, claim, workflow.ExecutionTargetRecoveryCauseMissingManagedRoot)
		}
		return fmt.Errorf("resolve locked execution target recovery root: %w", err)
	}
	if root.ManagedWorktree == nil {
		return c.markManualRecovery(recovery.Target, claim, workflow.ExecutionTargetRecoveryCauseMissingManagedRoot)
	}
	inspection, err := c.service.targetWorktrees.InspectExecutionTargetWorktree(c.ctx, worktree.InspectExecutionTargetWorktreeRequest{
		SourceWorkspaceRoot: recovery.SourceWorkspace.Root,
		WorktreeRoot:        root.ManagedWorktree.Root,
		TaskShortID:         recovery.TaskShortID,
		ResolvedCommit:      recovery.Target.ResolvedSource.Commit,
		ExpectedOwnership:   recovery.Target.LinkedWorktreeOwnership,
		ExpectedBranchTip:   recovery.Target.ExactBranchObservation,
	})
	if err != nil {
		if c.ctx.Err() == nil {
			return c.markManualRecovery(recovery.Target, claim, workflow.ExecutionTargetRecoveryCauseInspectionFailed)
		}
		return err
	}
	if inspection.Kind != worktree.ExecutionTargetWorktreeInspectionExact || inspection.BranchName != recovery.TaskShortID {
		if inspection.Kind == worktree.ExecutionTargetWorktreeInspectionExactMissingRoot {
			return c.reprovisionLockedTarget(recovery, claim, root.ManagedWorktree.Root)
		}
		cause := workflow.ExecutionTargetRecoveryCauseAmbiguousWorktree
		if inspection.Kind == worktree.ExecutionTargetWorktreeInspectionNoSideEffects {
			cause = workflow.ExecutionTargetRecoveryCauseMissingManagedRoot
		}
		return c.markManualRecovery(recovery.Target, claim, cause)
	}
	return c.recoverAttachedSetup(recovery, recovery.Target, claim, *root.ManagedWorktree, recovery.TaskShortID)
}

func (c *executionTargetRecoveryCoordinator) reprovisionLockedTarget(recovery workflowstore.ExecutionTargetRecoveryContext, claim workflow.ExecutionTargetClaim, worktreeRoot string) error {
	if recovery.Target.ExactBranchObservation == nil || recovery.Target.LinkedWorktreeOwnership == nil {
		return c.markManualRecovery(recovery.Target, claim, workflow.ExecutionTargetRecoveryCauseAmbiguousWorktree)
	}
	provisioningGeneration := uuid.NewString()
	target := recovery.Target
	target.State = workflow.ExecutionTargetStateLockedReprovisioning
	target.IntendedWorktreeRoot = stringPointer(worktreeRoot)
	target.ProvisioningGeneration = &provisioningGeneration
	target.SetupProvisioningGeneration = &provisioningGeneration
	target.SetupState = workflow.ExecutionTargetSetupPending
	if err := c.service.store.UpdateTaskExecutionTargetLifecycle(c.ctx, target, claim); err != nil {
		return err
	}
	provisioned, err := c.service.targetWorktrees.ReprovisionExecutionTargetWorktree(c.ctx, worktree.ProvisionExecutionTargetWorktreeRequest{
		WorkspaceID:         recovery.SourceWorkspace.ID,
		SourceWorkspaceRoot: recovery.SourceWorkspace.Root,
		TaskShortID:         recovery.TaskShortID,
		ResolvedCommit:      *recovery.Target.ExactBranchObservation,
		WorktreeRoot:        worktreeRoot,
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(provisioned.ExactBranchObservation) == "" || provisioned.LinkedWorktreeOwnership == nil {
		return c.markManualRecovery(target, claim, workflow.ExecutionTargetRecoveryCauseAmbiguousWorktree)
	}
	target.State = workflow.ExecutionTargetStateLocked
	target.IntendedWorktreeRoot = nil
	target.ExactBranchObservation = stringPointer(provisioned.ExactBranchObservation)
	target.LinkedWorktreeOwnership = provisioned.LinkedWorktreeOwnership
	attached, err := c.service.store.AttachManagedExecutionTargetWorktree(c.ctx, workflowstore.AttachManagedExecutionTargetWorktreeRequest{
		Target:        target,
		ExpectedClaim: claim,
		WorkspaceID:   recovery.SourceWorkspace.ID,
		WorktreeRoot:  provisioned.WorktreeRoot,
		CreatedBranch: false,
	})
	if err != nil {
		return err
	}
	return c.recoverAttachedSetup(recovery, target, claim, attached, recovery.TaskShortID)
}

func (c *executionTargetRecoveryCoordinator) markManualRecovery(target workflow.ExecutionTarget, claim workflow.ExecutionTargetClaim, cause workflow.ExecutionTargetRecoveryCause) error {
	if err := c.service.store.MarkExecutionTargetManualRecovery(c.ctx, target, claim, cause); err != nil {
		return fmt.Errorf("mark execution target manual recovery: %w", err)
	}
	c.service.publishExecutionTargetRecoveryUpdate(c.ctx, target.TaskID)
	return nil
}

func (s *Service) publishExecutionTargetRecoveryUpdate(ctx context.Context, taskID workflow.TaskID) {
	detail, err := s.view.GetTask(ctx, string(taskID))
	if err != nil {
		slog.Warn("get task after execution target recovery transition failed", "task_id", taskID, "error", err)
		return
	}
	s.publishWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, "task", "updated", string(taskID))
}

func (c *executionTargetRecoveryCoordinator) requeue(taskID workflow.TaskID, claim workflow.ExecutionTargetClaim) error {
	requeueContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.service.store.RequeueExecutionTargetRecovery(requeueContext, taskID, claim)
	if errors.Is(err, workflowstore.ErrTaskExecutionTargetClaimChanged) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("requeue execution target recovery: %w", err)
	}
	return nil
}

func (c *executionTargetRecoveryCoordinator) recordCloseError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closeErr == nil {
		c.closeErr = err
	}
}

func (s *Service) CreateWorkflow(ctx context.Context, req serverapi.WorkflowCreateRequest) (serverapi.WorkflowCreateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowCreateResponse{}, err
	}
	created, err := s.store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: req.Name, Description: req.Description, ExecutionPolicy: workflowExecutionPolicyFromAPI(req.ExecutionPolicy)})
	if err != nil {
		return serverapi.WorkflowCreateResponse{}, err
	}
	return serverapi.WorkflowCreateResponse{Workflow: workflowRecord(created)}, nil
}

func (s *Service) CreateAndLinkWorkflowToProject(ctx context.Context, request serverapi.WorkflowCreateAndLinkProjectRequest) (serverapi.WorkflowCreateAndLinkProjectResponse, error) {
	if err := request.Validate(); err != nil {
		return serverapi.WorkflowCreateAndLinkProjectResponse{}, err
	}
	created, link, err := s.store.CreateAndLinkWorkflow(ctx, workflowstore.CreateAndLinkWorkflowRequest{
		Name:            request.Name,
		Description:     request.Description,
		ExecutionPolicy: workflowExecutionPolicyFromAPI(request.ExecutionPolicy),
		ProjectID:       request.ProjectID,
		DefaultPolicy:   workflowStoreDefaultPolicy(request.DefaultPolicy),
	})
	if err != nil {
		return serverapi.WorkflowCreateAndLinkProjectResponse{}, err
	}
	s.publishWorkflowEvent(ctx, request.ProjectID, string(created.ID), "workflow_link", "linked", link.ID)
	return serverapi.WorkflowCreateAndLinkProjectResponse{Workflow: workflowRecord(created), Link: projectWorkflowLink(link)}, nil
}

func (s *Service) publishWorkflowEvent(ctx context.Context, projectID string, workflowID string, resource string, action string, changedIDs ...string) {
	if err := s.store.PublishWorkflowEvent(ctx, workflowstore.WorkflowEventRecord{ProjectID: projectID, WorkflowID: workflowID, Resource: resource, Action: action, ChangedIDs: changedIDs}); err != nil {
		slog.Warn("publish workflow event failed", "project_id", strings.TrimSpace(projectID), "workflow_id", strings.TrimSpace(workflowID), "resource", strings.TrimSpace(resource), "action", strings.TrimSpace(action), "changed_ids", changedIDs, "error", err)
	}
}

func (s *Service) publishLinkedWorkflowEvent(ctx context.Context, workflowID string, resource string, action string, changedIDs ...string) {
	s.publishWorkflowEvent(ctx, "", workflowID, resource, action, changedIDs...)
	links, err := s.store.ListWorkflowProjectLinks(ctx, workflow.WorkflowID(workflowID))
	if err != nil {
		slog.Warn("list workflow project links for event failed", "workflow_id", strings.TrimSpace(workflowID), "resource", strings.TrimSpace(resource), "action", strings.TrimSpace(action), "changed_ids", changedIDs, "error", err)
		return
	}
	seen := map[string]bool{}
	for _, link := range links {
		projectID := strings.TrimSpace(link.ProjectID)
		if projectID == "" || seen[projectID] {
			continue
		}
		seen[projectID] = true
		s.publishWorkflowEvent(ctx, projectID, workflowID, resource, action, changedIDs...)
	}
}

func (s *Service) UpdateWorkflow(ctx context.Context, req serverapi.WorkflowUpdateRequest) (serverapi.WorkflowGetResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowGetResponse{}, err
	}
	if err := s.store.UpdateWorkflowInfoWithExecutionPolicy(ctx, workflow.WorkflowID(req.WorkflowID), req.Name, req.Description, workflowExecutionPolicyFromAPI(req.ExecutionPolicy)); err != nil {
		return serverapi.WorkflowGetResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, "workflow", "updated", req.WorkflowID)
	return s.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: req.WorkflowID})
}

func (s *Service) ListWorkflows(ctx context.Context, req serverapi.WorkflowListRequest) (serverapi.WorkflowListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowListResponse{}, err
	}
	rows, err := s.store.ListWorkflows(ctx, workflowstore.ListWorkflowsRequest{PageSize: req.PageSize, PageToken: req.PageToken, Query: req.Query, ExactName: req.ExactName})
	if err != nil {
		return serverapi.WorkflowListResponse{}, err
	}
	out := make([]serverapi.WorkflowRecord, 0, len(rows.Workflows))
	for _, row := range rows.Workflows {
		out = append(out, workflowRecord(row))
	}
	return serverapi.WorkflowListResponse{Workflows: out, NextPageToken: rows.NextPageToken}, nil
}

func (s *Service) GetWorkflow(ctx context.Context, req serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowGetResponse{}, err
	}
	def, _, err := s.view.GetDefinition(ctx, req.WorkflowID)
	if err != nil {
		return serverapi.WorkflowGetResponse{}, err
	}
	return serverapi.WorkflowGetResponse{Definition: def}, nil
}

func (s *Service) AddWorkflowNode(ctx context.Context, req serverapi.WorkflowNodeAddRequest) (serverapi.WorkflowNodeAddResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowNodeAddResponse{}, err
	}
	revision, err := s.store.AddNode(ctx, workflowstore.NodeRecord{ID: workflow.NodeID(req.NodeID), WorkflowID: workflow.WorkflowID(req.WorkflowID), Key: workflow.ModelKey(req.Key), Kind: workflow.NodeKind(req.Kind), DisplayName: req.DisplayName, GroupKey: req.GroupKey, SubagentRole: req.SubagentRole, PromptTemplate: req.PromptTemplate, CompletionMode: req.CompletionMode, ScriptPath: optionalStringValue(req.ScriptPath), InputFields: inputFields(req.InputFields), JoinInputProviders: joinInputProviders(req.JoinInputProviders)})
	if err != nil {
		return serverapi.WorkflowNodeAddResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, "workflow", "node_added", req.NodeID)
	return serverapi.WorkflowNodeAddResponse{Version: revision}, nil
}

func (s *Service) UpdateWorkflowNode(ctx context.Context, req serverapi.WorkflowNodeUpdateRequest) (serverapi.WorkflowNodeUpdateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowNodeUpdateResponse{}, err
	}
	revision, err := s.store.UpdateNode(ctx, workflowstore.NodeRecord{ID: workflow.NodeID(req.NodeID), WorkflowID: workflow.WorkflowID(req.WorkflowID), Key: workflow.ModelKey(req.Key), Kind: workflow.NodeKind(req.Kind), DisplayName: req.DisplayName, GroupKey: req.GroupKey, SubagentRole: req.SubagentRole, PromptTemplate: req.PromptTemplate, CompletionMode: req.CompletionMode, ScriptPath: optionalStringValue(req.ScriptPath), InputFields: inputFields(req.InputFields), JoinInputProviders: joinInputProviders(req.JoinInputProviders)})
	if err != nil {
		return serverapi.WorkflowNodeUpdateResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, "workflow", "node_updated", req.NodeID)
	return serverapi.WorkflowNodeUpdateResponse{Version: revision}, nil
}

func (s *Service) AddWorkflowNodeGroup(ctx context.Context, req serverapi.WorkflowNodeGroupAddRequest) (serverapi.WorkflowNodeGroupResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowNodeGroupResponse{}, err
	}
	group, revision, err := s.store.AddNodeGroup(ctx, workflowstore.NodeGroupRecord{ID: req.GroupID, WorkflowID: workflow.WorkflowID(req.WorkflowID), Key: workflow.ModelKey(req.GroupKey), DisplayName: req.DisplayName, SortOrder: int64(req.SortOrder)})
	if err != nil {
		return serverapi.WorkflowNodeGroupResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, "workflow", "node_group_added", group.ID)
	return serverapi.WorkflowNodeGroupResponse{Group: workflowNodeGroup(group), Version: revision}, nil
}

func (s *Service) UpdateWorkflowNodeGroup(ctx context.Context, req serverapi.WorkflowNodeGroupUpdateRequest) (serverapi.WorkflowNodeGroupResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowNodeGroupResponse{}, err
	}
	group, revision, err := s.store.UpdateNodeGroup(ctx, workflowstore.NodeGroupRecord{ID: req.GroupID, WorkflowID: workflow.WorkflowID(req.WorkflowID), Key: workflow.ModelKey(req.GroupKey), DisplayName: req.DisplayName, SortOrder: int64(req.SortOrder)})
	if err != nil {
		return serverapi.WorkflowNodeGroupResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, "workflow", "node_group_updated", group.ID)
	return serverapi.WorkflowNodeGroupResponse{Group: workflowNodeGroup(group), Version: revision}, nil
}

func (s *Service) DeleteWorkflowNodeGroup(ctx context.Context, req serverapi.WorkflowNodeGroupDeleteRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if _, err := s.store.DeleteNodeGroup(ctx, workflow.WorkflowID(req.WorkflowID), req.GroupID); err != nil {
		return err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, "workflow", "node_group_deleted", req.GroupID)
	return nil
}

func (s *Service) AddWorkflowTransitionGroup(ctx context.Context, req serverapi.WorkflowTransitionGroupAddRequest) (serverapi.WorkflowTransitionGroupAddResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTransitionGroupAddResponse{}, err
	}
	revision, err := s.store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID(req.GroupID), WorkflowID: workflow.WorkflowID(req.WorkflowID), SourceNodeID: workflow.NodeID(req.SourceNodeID), TransitionID: workflow.TransitionID(req.TransitionID), DisplayName: req.DisplayName, Description: req.Description})
	if err != nil {
		return serverapi.WorkflowTransitionGroupAddResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, "workflow", "transition_group_added", req.GroupID)
	return serverapi.WorkflowTransitionGroupAddResponse{Version: revision}, nil
}

func (s *Service) UpdateWorkflowTransitionGroup(ctx context.Context, req serverapi.WorkflowTransitionGroupUpdateRequest) (serverapi.WorkflowTransitionGroupUpdateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTransitionGroupUpdateResponse{}, err
	}
	revision, err := s.store.UpdateTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID(req.GroupID), WorkflowID: workflow.WorkflowID(req.WorkflowID), SourceNodeID: workflow.NodeID(req.SourceNodeID), TransitionID: workflow.TransitionID(req.TransitionID), DisplayName: req.DisplayName, Description: req.Description})
	if err != nil {
		return serverapi.WorkflowTransitionGroupUpdateResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, "workflow", "transition_group_updated", req.GroupID)
	return serverapi.WorkflowTransitionGroupUpdateResponse{Version: revision}, nil
}

func (s *Service) AddWorkflowEdge(ctx context.Context, req serverapi.WorkflowEdgeAddRequest) (serverapi.WorkflowEdgeAddResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowEdgeAddResponse{}, err
	}
	revision, err := s.store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID(req.EdgeID), WorkflowID: workflow.WorkflowID(req.WorkflowID), TransitionGroupID: workflow.TransitionGroupID(req.TransitionGroupID), Key: workflow.ModelKey(req.Key), TargetNodeID: workflow.NodeID(req.TargetNodeID), RequiresApproval: req.RequiresApproval, ContextMode: workflow.ContextMode(req.ContextMode), ContextSource: workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(req.ContextSource.Kind), NodeKey: workflow.ModelKey(req.ContextSource.NodeKey)}), PromptTemplate: req.PromptTemplate, Parameters: domainParameters(req.Parameters)})
	if err != nil {
		return serverapi.WorkflowEdgeAddResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, "workflow", "edge_added", req.EdgeID)
	return serverapi.WorkflowEdgeAddResponse{Version: revision}, nil
}

func (s *Service) UpdateWorkflowEdge(ctx context.Context, req serverapi.WorkflowEdgeUpdateRequest) (serverapi.WorkflowEdgeUpdateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowEdgeUpdateResponse{}, err
	}
	revision, err := s.store.UpdateEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID(req.EdgeID), WorkflowID: workflow.WorkflowID(req.WorkflowID), TransitionGroupID: workflow.TransitionGroupID(req.TransitionGroupID), Key: workflow.ModelKey(req.Key), TargetNodeID: workflow.NodeID(req.TargetNodeID), RequiresApproval: req.RequiresApproval, ContextMode: workflow.ContextMode(req.ContextMode), ContextSource: workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(req.ContextSource.Kind), NodeKey: workflow.ModelKey(req.ContextSource.NodeKey)}), PromptTemplate: req.PromptTemplate, Parameters: domainParameters(req.Parameters)})
	if err != nil {
		return serverapi.WorkflowEdgeUpdateResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, "workflow", "edge_updated", req.EdgeID)
	return serverapi.WorkflowEdgeUpdateResponse{Version: revision}, nil
}

func (s *Service) LinkWorkflowToProject(ctx context.Context, request serverapi.WorkflowLinkProjectRequest) (serverapi.WorkflowLinkProjectResponse, error) {
	if err := request.Validate(); err != nil {
		return serverapi.WorkflowLinkProjectResponse{}, err
	}
	link, err := s.store.LinkWorkflowWithDefaultPolicy(ctx, request.ProjectID, workflow.WorkflowID(request.WorkflowID), workflowStoreDefaultPolicy(request.DefaultPolicy))
	if err != nil {
		return serverapi.WorkflowLinkProjectResponse{}, err
	}
	s.publishWorkflowEvent(ctx, request.ProjectID, request.WorkflowID, "workflow_link", "linked", link.ID)
	return serverapi.WorkflowLinkProjectResponse{Link: projectWorkflowLink(link)}, nil
}

func (s *Service) ListProjectWorkflowLinks(ctx context.Context, req serverapi.WorkflowListProjectLinksRequest) (serverapi.WorkflowListProjectLinksResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowListProjectLinksResponse{}, err
	}
	links, err := s.store.ListProjectWorkflowLinks(ctx, req.ProjectID)
	if err != nil {
		return serverapi.WorkflowListProjectLinksResponse{}, err
	}
	out := make([]serverapi.ProjectWorkflowLink, 0, len(links))
	for _, link := range links {
		out = append(out, projectWorkflowLink(link))
	}
	return serverapi.WorkflowListProjectLinksResponse{Links: out}, nil
}

func (s *Service) SetDefaultProjectWorkflowLink(ctx context.Context, req serverapi.WorkflowSetDefaultProjectLinkRequest) (serverapi.WorkflowSetDefaultProjectLinkResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowSetDefaultProjectLinkResponse{}, err
	}
	link, err := s.store.SetDefaultProjectWorkflowLink(ctx, req.ProjectID, workflow.WorkflowID(req.WorkflowID))
	if err != nil {
		return serverapi.WorkflowSetDefaultProjectLinkResponse{}, err
	}
	s.publishWorkflowEvent(ctx, req.ProjectID, req.WorkflowID, "workflow_link", "default_changed", link.ID)
	return serverapi.WorkflowSetDefaultProjectLinkResponse{Link: projectWorkflowLink(link)}, nil
}

func (s *Service) UnlinkWorkflowFromProject(ctx context.Context, req serverapi.WorkflowUnlinkProjectRequest) (serverapi.WorkflowUnlinkProjectResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowUnlinkProjectResponse{}, err
	}
	result, err := s.store.UnlinkProjectWorkflow(ctx, req.LinkID, req.ReplacementDefaultLinkID)
	resp := workflowUnlinkProjectResponse(result)
	if err != nil {
		return resp, err
	}
	if result.Unlinked {
		s.publishWorkflowEvent(ctx, result.ProjectID, string(result.WorkflowID), "workflow_link", "unlinked", req.LinkID)
	}
	return resp, nil
}

func (s *Service) PreviewWorkflowDelete(ctx context.Context, req serverapi.WorkflowDeletePreviewRequest) (serverapi.WorkflowDeletePreviewResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowDeletePreviewResponse{}, err
	}
	impact, err := s.store.PreviewWorkflowDelete(ctx, workflow.WorkflowID(req.WorkflowID))
	if err != nil {
		return serverapi.WorkflowDeletePreviewResponse{}, err
	}
	return serverapi.WorkflowDeletePreviewResponse{Impact: workflowDeleteImpact(impact)}, nil
}

func (s *Service) DeleteWorkflow(ctx context.Context, req serverapi.WorkflowDeleteRequest) (serverapi.WorkflowDeleteResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowDeleteResponse{}, err
	}
	links, err := s.store.ListWorkflowProjectLinks(ctx, workflow.WorkflowID(req.WorkflowID))
	if err != nil {
		return serverapi.WorkflowDeleteResponse{}, err
	}
	var interruptedRunIDs []workflow.RunID
	if req.Confirmed {
		interruptedRunIDs = s.workflowInterruptedRunAttentionIDsByWorkflow(ctx, req.WorkflowID, links)
	}
	result, err := s.store.DeleteWorkflow(ctx, workflowstore.WorkflowDeleteRequest{
		WorkflowID:           workflow.WorkflowID(req.WorkflowID),
		Confirmed:            req.Confirmed,
		ExpectedVersion:      req.ExpectedVersion,
		ExpectedProjectCount: req.ExpectedProjectCount,
		ExpectedLinkCount:    req.ExpectedLinkCount,
		ExpectedTaskCount:    req.ExpectedTaskCount,
		CleanupArtifacts:     req.CleanupArtifacts,
	})
	if err != nil {
		return serverapi.WorkflowDeleteResponse{}, err
	}
	resp := workflowDeleteResponse(result)
	if !resp.Deleted {
		return resp, nil
	}
	s.finalizeWorkflowApprovalProjections(ctx, result.ResolvedApprovalTransitionProjections)
	s.resolveWorkflowInterruptedRunAttention(ctx, interruptedRunIDs)
	s.publishWorkflowEvent(ctx, "", req.WorkflowID, "workflow", "deleted", req.WorkflowID)
	seen := map[string]bool{}
	for _, link := range links {
		projectID := strings.TrimSpace(link.ProjectID)
		if projectID == "" || seen[projectID] {
			continue
		}
		seen[projectID] = true
		s.publishWorkflowEvent(ctx, projectID, req.WorkflowID, "workflow", "deleted", req.WorkflowID)
	}
	return resp, nil
}

func (s *Service) ValidateWorkflow(ctx context.Context, req serverapi.WorkflowValidateRequest) (serverapi.WorkflowValidateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowValidateResponse{}, err
	}
	def, _, err := s.store.GetDefinition(ctx, workflow.WorkflowID(req.WorkflowID))
	if err != nil {
		return serverapi.WorkflowValidateResponse{}, err
	}
	mode := workflow.ValidationContext(req.Mode)
	if mode == "" {
		mode = workflow.ValidationContextDraft
	}
	result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: mode, RoleResolver: s.roleResolver})
	resp := workflowValidationResponse(def.ID, result)
	if mode == workflow.ValidationContextExecution {
		resp.Errors = append(resp.Errors, scriptPathValidationErrors(def, "")...)
		resp.Valid = workflowValidationErrorsValid(resp.Errors)
	}
	return resp, nil
}

func (s *Service) ValidateWorkflowScriptPath(ctx context.Context, req serverapi.WorkflowScriptPathValidateRequest) (serverapi.WorkflowValidateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowValidateResponse{}, err
	}
	def, _, err := s.store.GetDefinition(ctx, workflow.WorkflowID(req.WorkflowID))
	if err != nil {
		return serverapi.WorkflowValidateResponse{}, err
	}
	diagnostics := workflowscript.Validate(workflowscript.ValidationRequest{RawPath: req.ScriptPath})
	errors := make([]serverapi.WorkflowValidationError, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		errors = append(errors, scriptPathValidationError(def.ID, workflow.NodeID(req.NodeID), diagnostic))
	}
	return serverapi.WorkflowValidateResponse{
		Valid:  workflowValidationErrorsValid(errors),
		Errors: errors,
	}, nil
}

func (s *Service) ValidateWorkflowGraphDraft(ctx context.Context, req serverapi.WorkflowGraphValidateDraftRequest) (serverapi.WorkflowGraphValidateDraftResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowGraphValidateDraftResponse{}, err
	}
	def, err := s.workflowGraphDraftDefinition(ctx, req.WorkflowID, req.Metadata, req.Graph)
	if err != nil {
		return serverapi.WorkflowGraphValidateDraftResponse{}, err
	}
	return serverapi.WorkflowGraphValidateDraftResponse{
		Results:       s.workflowGraphValidationResultsForDefinition(def, req.Modes),
		DerivedWiring: workflowview.DerivedWiring(def),
	}, nil
}

func (s *Service) DeriveWorkflowGraphWiring(ctx context.Context, req serverapi.WorkflowGraphDeriveWiringRequest) (serverapi.WorkflowGraphDeriveWiringResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowGraphDeriveWiringResponse{}, err
	}
	def, err := s.workflowGraphDraftDefinition(ctx, req.WorkflowID, nil, req.Graph)
	if err != nil {
		return serverapi.WorkflowGraphDeriveWiringResponse{}, err
	}
	return serverapi.WorkflowGraphDeriveWiringResponse{
		DerivedWiring: workflowview.DerivedWiring(def),
	}, nil
}

func (s *Service) PreviewWorkflowGraphSave(ctx context.Context, req serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowGraphSavePreviewResponse{}, err
	}
	validationResults, err := s.workflowGraphValidationResults(ctx, req.WorkflowID, req.Metadata, req.Graph, workflowGraphSaveValidationModes())
	if err != nil {
		return serverapi.WorkflowGraphSavePreviewResponse{}, err
	}
	result, err := s.store.PreviewWorkflowGraphSave(ctx, workflowGraphStoreSaveRequest(req.WorkflowID, req.ExpectedVersion, req.Metadata, req.Graph, nil))
	if err != nil {
		return serverapi.WorkflowGraphSavePreviewResponse{}, err
	}
	return workflowGraphSavePreviewResponse(result, validationResults), nil
}

func (s *Service) SaveWorkflowGraph(ctx context.Context, req serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowGraphSaveResponse{}, err
	}
	validationResults, err := s.workflowGraphValidationResults(ctx, req.WorkflowID, req.Metadata, req.Graph, workflowGraphSaveValidationModes())
	if err != nil {
		return serverapi.WorkflowGraphSaveResponse{}, err
	}
	result, err := s.store.SaveWorkflowGraph(ctx, workflowGraphStoreSaveRequest(req.WorkflowID, req.ExpectedVersion, req.Metadata, req.Graph, req.Confirmation))
	if err != nil {
		return serverapi.WorkflowGraphSaveResponse{}, err
	}
	resp := workflowGraphSaveResponse(result, validationResults)
	if !result.Saved {
		return resp, nil
	}
	saved, err := s.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: req.WorkflowID})
	if err != nil {
		return serverapi.WorkflowGraphSaveResponse{}, err
	}
	resp.Definition = &saved.Definition
	resp.CurrentVersion = saved.Definition.Workflow.Version
	if result.Changed {
		s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, "workflow", "graph_saved", req.WorkflowID)
	}
	return resp, nil
}

func (s *Service) CreateWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCreateRequest) (serverapi.WorkflowTaskCreateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskCreateResponse{}, err
	}
	task, err := s.store.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: req.ProjectID, WorkflowID: workflow.WorkflowID(req.WorkflowID), Title: req.Title, Body: req.Body, SourceURL: req.SourceURL, SourceWorkspaceID: req.SourceWorkspaceID})
	if err != nil {
		return serverapi.WorkflowTaskCreateResponse{}, err
	}
	s.publishWorkflowEvent(ctx, task.ProjectID, string(task.WorkflowID), "task", "created", string(task.ID))
	detail, err := s.view.GetTask(ctx, string(task.ID))
	if err != nil {
		return serverapi.WorkflowTaskCreateResponse{}, err
	}
	return serverapi.WorkflowTaskCreateResponse{Task: detail.Summary}, nil
}

func (s *Service) UpdateWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskUpdateRequest) (serverapi.WorkflowTaskUpdateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskUpdateResponse{}, err
	}
	task, err := s.store.UpdateTask(ctx, workflowstore.UpdateTaskRequest{TaskID: workflow.TaskID(req.TaskID), Title: req.Title, Body: req.Body, SourceWorkspaceID: req.SourceWorkspaceID})
	if err != nil {
		return serverapi.WorkflowTaskUpdateResponse{}, err
	}
	s.publishWorkflowEvent(ctx, task.ProjectID, string(task.WorkflowID), "task", "updated", string(task.ID))
	detail, err := s.view.GetTask(ctx, string(task.ID))
	if err != nil {
		return serverapi.WorkflowTaskUpdateResponse{}, err
	}
	return serverapi.WorkflowTaskUpdateResponse{Task: detail.Summary}, nil
}

func (s *Service) StartWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskInitiatingActionResult{}, err
	}
	negotiation, err := s.negotiateTaskStartExecutionTarget(ctx, req)
	if err != nil {
		return serverapi.WorkflowTaskInitiatingActionResult{}, err
	}
	if negotiation != nil {
		if negotiation.Started != nil {
			if s.schedulerWake != nil {
				s.schedulerWake.Notify()
			}
			if detail, detailErr := s.view.GetTask(ctx, req.TaskID); detailErr == nil {
				s.publishWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, "task", "started", req.TaskID, negotiation.Started.RunID)
			}
		}
		return *negotiation, nil
	}
	started, err := s.startTaskAutomation(ctx, TaskAutomationStartRequest{TaskID: req.TaskID, SetupOperationID: req.SetupOperationID})
	if err != nil {
		return serverapi.WorkflowTaskInitiatingActionResult{}, err
	}
	if detail, detailErr := s.view.GetTask(ctx, req.TaskID); detailErr == nil {
		s.publishWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, "task", "started", req.TaskID, string(started.RunID))
	}
	return serverapi.WorkflowTaskInitiatingActionResult{
		Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeStarted,
		Started: &serverapi.WorkflowTaskStartResponse{TransitionID: started.TransitionID, PlacementID: string(started.PlacementID), RunID: string(started.RunID)},
	}, nil
}

func (s *Service) negotiateTaskStartExecutionTarget(ctx context.Context, req serverapi.WorkflowTaskStartRequest) (*serverapi.WorkflowTaskInitiatingActionResult, error) {
	prepared, err := s.store.PrepareTaskStartExecutionTargetNegotiation(ctx, workflow.TaskID(req.TaskID))
	if err != nil {
		return nil, err
	}
	materialization, err := s.materializeTaskExecutionTargetForAction(ctx, prepared, req.SelectionGeneration, req.Selection, req.SetupOperationID)
	if err != nil {
		return nil, err
	}
	if materialization.conflict != nil {
		return materialization.conflict, nil
	}
	if materialization.selectionRequired != nil {
		return materialization.selectionRequired, nil
	}
	if materialization.none {
		var started workflowstore.StartTaskResult
		if materialization.manualReplacement != nil {
			started, err = s.store.ReplaceManualExecutionTargetAndStart(ctx, noneExecutionTarget(prepared.TaskID), *materialization.manualReplacement)
		} else {
			started, err = s.store.StartTaskWithExecutionTarget(ctx, noneExecutionTarget(prepared.TaskID))
		}
		if err != nil {
			return nil, err
		}
		return &serverapi.WorkflowTaskInitiatingActionResult{
			Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeStarted,
			Started: &serverapi.WorkflowTaskStartResponse{
				TransitionID: started.TransitionID,
				PlacementID:  string(started.PlacementID),
				RunID:        string(started.RunID),
			},
		}, nil
	}
	return nil, nil
}

type taskExecutionTargetActionMaterialization struct {
	none              bool
	selectionRequired *serverapi.WorkflowTaskInitiatingActionResult
	conflict          *serverapi.WorkflowTaskInitiatingActionResult
	manualReplacement *workflow.ExecutionTargetNegotiation
}

func (s *Service) materializeTaskExecutionTargetForAction(ctx context.Context, prepared workflowstore.TaskExecutionTargetNegotiationPreparation, selectionGeneration *string, requestedSelection *serverapi.WorkflowTaskExecutionTargetSelection, setupOperationID serverapi.WorktreeSetupOperationID) (taskExecutionTargetActionMaterialization, error) {
	selection, implicitPolicySelection, err := executionTargetSelectionForRequest(prepared.ExecutionPolicy, requestedSelection)
	if err != nil {
		return taskExecutionTargetActionMaterialization{}, err
	}
	matchesNoneSelection, err := s.matchesNoneExecutionTargetSelection(ctx, prepared, selectionGeneration, selection)
	if err != nil {
		if executionTargetNegotiationConflict(err) {
			return taskExecutionTargetActionMaterialization{conflict: taskExecutionTargetNegotiationConflictResult(prepared.TaskID)}, nil
		}
		return taskExecutionTargetActionMaterialization{}, err
	}
	if matchesNoneSelection {
		return taskExecutionTargetActionMaterialization{none: true, manualReplacement: manualExecutionTargetReplacement(prepared)}, nil
	}
	matchesSelection, err := s.matchesExecutionTargetSelection(ctx, prepared, selectionGeneration, selection)
	if err != nil {
		if executionTargetNegotiationConflict(err) {
			return taskExecutionTargetActionMaterialization{conflict: taskExecutionTargetNegotiationConflictResult(prepared.TaskID)}, nil
		}
		return taskExecutionTargetActionMaterialization{}, err
	}
	if matchesSelection && selection != nil {
		policy, policyErr := executionTargetPolicyForSelection(selection.Mode, selection.CustomRef)
		if policyErr != nil {
			return taskExecutionTargetActionMaterialization{}, policyErr
		}
		if err := s.materializeManagedExecutionTarget(ctx, prepared, policy, setupOperationID, manualExecutionTargetReplacement(prepared)); err != nil {
			if implicitPolicySelection && executionTargetResolutionRequiresSelection(err) {
				selectionRequired, selectionErr := s.requireTaskExecutionTargetSelection(ctx, prepared)
				if selectionErr != nil {
					if executionTargetNegotiationConflict(selectionErr) {
						return taskExecutionTargetActionMaterialization{conflict: taskExecutionTargetNegotiationConflictResult(prepared.TaskID)}, nil
					}
					return taskExecutionTargetActionMaterialization{}, selectionErr
				}
				return taskExecutionTargetActionMaterialization{selectionRequired: selectionRequired}, nil
			}
			if executionTargetNegotiationConflict(err) {
				return taskExecutionTargetActionMaterialization{conflict: taskExecutionTargetNegotiationConflictResult(prepared.TaskID)}, nil
			}
			return taskExecutionTargetActionMaterialization{}, err
		}
		return taskExecutionTargetActionMaterialization{}, nil
	}
	selectionRequired, err := s.requireTaskExecutionTargetSelection(ctx, prepared)
	if err != nil {
		if executionTargetNegotiationConflict(err) {
			return taskExecutionTargetActionMaterialization{conflict: taskExecutionTargetNegotiationConflictResult(prepared.TaskID)}, nil
		}
		return taskExecutionTargetActionMaterialization{}, err
	}
	return taskExecutionTargetActionMaterialization{selectionRequired: selectionRequired}, nil
}

func noneExecutionTarget(taskID workflow.TaskID) workflow.ExecutionTarget {
	return workflow.ExecutionTarget{
		TaskID:              taskID,
		Policy:              workflow.ExecutionPolicyNone,
		State:               workflow.ExecutionTargetStateLocked,
		SetupState:          workflow.ExecutionTargetSetupNotApplicable,
		RecoveryDisposition: workflow.ExecutionTargetRecoveryAvailable,
	}
}

func (s *Service) matchesNoneExecutionTargetSelection(ctx context.Context, prepared workflowstore.TaskExecutionTargetNegotiationPreparation, selectionGeneration *string, selection *serverapi.WorkflowTaskExecutionTargetSelection) (bool, error) {
	if selection == nil || selection.Mode != serverapi.WorkflowTaskExecutionTargetSelectionNone {
		return false, nil
	}
	return s.matchesExecutionTargetSelection(ctx, prepared, selectionGeneration, selection)
}

func (s *Service) matchesExecutionTargetSelection(ctx context.Context, prepared workflowstore.TaskExecutionTargetNegotiationPreparation, selectionGeneration *string, selection *serverapi.WorkflowTaskExecutionTargetSelection) (bool, error) {
	if selection == nil {
		return false, nil
	}
	if prepared.ExistingTarget != nil && prepared.ExistingTarget.RecoveryDisposition != workflow.ExecutionTargetRecoveryManualRecovery {
		return false, nil
	}
	if prepared.ExistingTarget != nil && prepared.ExistingTarget.ActiveClaim != nil {
		return false, nil
	}
	if prepared.ExistingNegotiation == nil {
		if prepared.ExistingTarget != nil {
			return false, nil
		}
		if prepared.ExecutionPolicy.Mode == workflow.ExecutionPolicyAsk {
			return false, nil
		}
		return true, nil
	}
	source, err := s.executionTargetNegotiationSource(ctx, prepared.SourceWorkspace.Root)
	if err != nil {
		return false, err
	}
	if selectionGeneration == nil ||
		*selectionGeneration != prepared.ExistingNegotiation.Generation ||
		!executionTargetNegotiationActionMatches(prepared.ExistingNegotiation.Action, prepared.Action) ||
		!executionTargetNegotiationSourceMatches(prepared.ExistingNegotiation.Source, source) {
		return false, nil
	}
	return true, nil
}

func (s *Service) materializeManagedExecutionTarget(ctx context.Context, prepared workflowstore.TaskExecutionTargetNegotiationPreparation, policy workflow.ExecutionPolicy, setupOperationID serverapi.WorktreeSetupOperationID, manualReplacement *workflow.ExecutionTargetNegotiation) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if policy.Mode == workflow.ExecutionPolicyNone || policy.Mode == workflow.ExecutionPolicyAsk {
		return errors.New("managed execution target materialization requires a concrete Git policy")
	}
	resolution, err := s.targetResolver.ResolveExecutionTarget(ctx, prepared.SourceWorkspace.Root, policy.Mode, policy.CustomRef)
	if err != nil {
		return err
	}
	if s.targetWorktrees == nil {
		return errors.New("execution target worktree materializer is required")
	}
	intendedWorktreeRoot, err := s.targetWorktrees.PlanExecutionTargetWorktreeRoot(prepared.SourceWorkspace.ID, prepared.TaskShortID)
	if err != nil {
		return err
	}
	provisioningGeneration := uuid.NewString()
	claim := workflow.ExecutionTargetClaim{Generation: uuid.NewString(), Phase: workflow.ExecutionTargetClaimMaterializing}
	target := workflow.ExecutionTarget{
		TaskID:                      prepared.TaskID,
		Policy:                      policy.Mode,
		RequestedCustomRef:          policy.CustomRef,
		ResolvedSource:              &resolution.Source,
		State:                       workflow.ExecutionTargetStateInitialProvisioning,
		IntendedWorktreeRoot:        &intendedWorktreeRoot,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &claim,
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
	}
	materialization := workflowstore.BeginManagedExecutionTargetMaterializationRequest{
		Target:              target,
		ExpectedNegotiation: prepared.ExistingNegotiation,
	}
	if manualReplacement != nil {
		err = s.store.ReplaceManualExecutionTargetMaterialization(ctx, materialization, *manualReplacement)
	} else {
		err = s.store.BeginManagedExecutionTargetMaterialization(ctx, materialization)
	}
	if err != nil {
		return err
	}
	provisioned, err := s.targetWorktrees.ProvisionExecutionTargetWorktree(ctx, worktree.ProvisionExecutionTargetWorktreeRequest{
		WorkspaceID:         prepared.SourceWorkspace.ID,
		SourceWorkspaceRoot: prepared.SourceWorkspace.Root,
		TaskShortID:         prepared.TaskShortID,
		ResolvedCommit:      resolution.Source.Commit,
		WorktreeRoot:        intendedWorktreeRoot,
	})
	if err != nil {
		return err
	}
	if provisioned.WorktreeRoot != intendedWorktreeRoot {
		return fmt.Errorf("provisioned execution target root %q does not match intended root %q", provisioned.WorktreeRoot, intendedWorktreeRoot)
	}
	if strings.TrimSpace(provisioned.ExactBranchObservation) == "" || provisioned.LinkedWorktreeOwnership == nil {
		return errors.New("provisioned execution target is missing exact linked-worktree ownership evidence")
	}
	target.State = workflow.ExecutionTargetStateLocked
	target.IntendedWorktreeRoot = nil
	target.ExactBranchObservation = stringPointer(provisioned.ExactBranchObservation)
	target.LinkedWorktreeOwnership = provisioned.LinkedWorktreeOwnership
	attached, err := s.store.AttachManagedExecutionTargetWorktree(ctx, workflowstore.AttachManagedExecutionTargetWorktreeRequest{
		Target:        target,
		ExpectedClaim: claim,
		WorkspaceID:   prepared.SourceWorkspace.ID,
		WorktreeRoot:  provisioned.WorktreeRoot,
		CreatedBranch: provisioned.CreatedBranch,
	})
	if err != nil {
		return err
	}
	target.SetupState = workflow.ExecutionTargetSetupRunning
	if err := s.store.UpdateTaskExecutionTargetLifecycle(ctx, target, claim); err != nil {
		return err
	}
	if err := s.targetWorktrees.RunExecutionTargetSetup(ctx, worktree.RunExecutionTargetSetupRequest{
		SetupOperationID:    setupOperationID,
		SourceWorkspaceRoot: prepared.SourceWorkspace.Root,
		WorktreeRoot:        attached.Root,
		BranchName:          provisioned.BranchName,
		ProjectID:           prepared.ProjectID,
		WorkspaceID:         prepared.SourceWorkspace.ID,
		WorktreeID:          attached.ID,
		CreatedBranch:       provisioned.CreatedBranch,
	}); err != nil {
		target.SetupState = workflow.ExecutionTargetSetupFailed
		target.ActiveClaim = nil
		if updateErr := s.store.UpdateTaskExecutionTargetLifecycle(ctx, target, claim); updateErr != nil {
			return errors.Join(err, updateErr)
		}
		return err
	}
	target.SetupState = workflow.ExecutionTargetSetupSucceeded
	target.ActiveClaim = nil
	if err := s.store.UpdateTaskExecutionTargetLifecycle(ctx, target, claim); err != nil {
		return err
	}
	return nil
}

func executionTargetSelectionForRequest(policy workflow.ExecutionPolicy, selection *serverapi.WorkflowTaskExecutionTargetSelection) (*serverapi.WorkflowTaskExecutionTargetSelection, bool, error) {
	if selection != nil {
		return selection, false, nil
	}
	switch policy.Mode {
	case workflow.ExecutionPolicyAsk:
		return nil, false, nil
	case workflow.ExecutionPolicyNone:
		return &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionNone}, true, nil
	case workflow.ExecutionPolicyHead:
		return &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionHead}, true, nil
	case workflow.ExecutionPolicyDefaultBranch:
		return &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionDefaultBranch}, true, nil
	case workflow.ExecutionPolicyCustomRef:
		if err := policy.Validate(); err != nil {
			return nil, false, err
		}
		return &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionCustomRef, CustomRef: policy.CustomRef}, true, nil
	default:
		return nil, false, fmt.Errorf("unsupported execution policy %q", policy.Mode)
	}
}

func executionTargetResolutionRequiresSelection(err error) bool {
	var resolutionErr *worktree.ExecutionTargetResolutionError
	if !errors.As(err, &resolutionErr) {
		return false
	}
	switch resolutionErr.Kind {
	case worktree.ExecutionTargetResolutionFailureNonGit, worktree.ExecutionTargetResolutionFailureUnavailable, worktree.ExecutionTargetResolutionFailureInvalidRef:
		return true
	default:
		return false
	}
}

func executionTargetPolicyForSelection(mode serverapi.WorkflowTaskExecutionTargetSelectionMode, customRef *string) (workflow.ExecutionPolicy, error) {
	switch mode {
	case serverapi.WorkflowTaskExecutionTargetSelectionHead:
		return workflow.ExecutionPolicy{Mode: workflow.ExecutionPolicyHead}, nil
	case serverapi.WorkflowTaskExecutionTargetSelectionDefaultBranch:
		return workflow.ExecutionPolicy{Mode: workflow.ExecutionPolicyDefaultBranch}, nil
	case serverapi.WorkflowTaskExecutionTargetSelectionCustomRef:
		policy := workflow.ExecutionPolicy{Mode: workflow.ExecutionPolicyCustomRef, CustomRef: customRef}
		return policy, policy.Validate()
	default:
		return workflow.ExecutionPolicy{}, fmt.Errorf("selection %q does not materialize a managed execution target", mode)
	}
}

func (s *Service) requireTaskExecutionTargetSelection(ctx context.Context, prepared workflowstore.TaskExecutionTargetNegotiationPreparation) (*serverapi.WorkflowTaskInitiatingActionResult, error) {
	if prepared.ExistingTarget != nil {
		if prepared.ExistingTarget.ActiveClaim != nil {
			return taskExecutionTargetInProgressResult(prepared.TaskID, prepared.ExistingTarget.ActiveClaim.Phase), nil
		}
		if prepared.ExistingTarget.RecoveryDisposition != workflow.ExecutionTargetRecoveryManualRecovery {
			return nil, nil
		}
	}
	source, err := s.executionTargetNegotiationSource(ctx, prepared.SourceWorkspace.Root)
	if err != nil {
		return nil, err
	}
	if prepared.ExistingNegotiation != nil {
		if !executionTargetNegotiationActionMatches(prepared.ExistingNegotiation.Action, prepared.Action) {
			return nil, workflowstore.ErrTaskExecutionTargetNegotiationChanged
		}
		if executionTargetNegotiationSourceMatches(prepared.ExistingNegotiation.Source, source) {
			return taskExecutionTargetSelectionRequiredResult(*prepared.ExistingNegotiation, prepared.ExecutionPolicy), nil
		}
	}
	negotiation := workflow.ExecutionTargetNegotiation{
		TaskID:            prepared.TaskID,
		Generation:        uuid.NewString(),
		WorkflowID:        prepared.WorkflowID,
		SourceWorkspaceID: prepared.SourceWorkspace.ID,
		Source:            source,
		Action:            prepared.Action,
	}
	if prepared.ExistingTarget != nil {
		negotiation.RecoveryCause = prepared.ExistingTarget.RecoveryCause
	}
	if err := s.store.SaveTaskExecutionTargetNegotiationIfExpected(ctx, prepared.ExistingNegotiation, negotiation); err != nil {
		return nil, err
	}
	return taskExecutionTargetSelectionRequiredResult(negotiation, prepared.ExecutionPolicy), nil
}

func (s *Service) executionTargetNegotiationSource(ctx context.Context, workspaceRoot string) (workflow.ExecutionTargetNegotiationSource, error) {
	if s.targetResolver == nil {
		return workflow.ExecutionTargetNegotiationSource{}, errors.New("execution target resolver is required")
	}
	resolution, err := s.targetResolver.ResolveExecutionTarget(ctx, workspaceRoot, workflow.ExecutionPolicyHead, nil)
	if err == nil {
		source := workflow.ExecutionTargetNegotiationSource{
			Commit: stringPointer(resolution.Source.Commit),
		}
		switch resolution.Source.Kind {
		case workflow.ExecutionTargetSourceNamedRef:
			source.Kind = workflow.ExecutionTargetNegotiationSourceNamedRef
			source.NamedRef = resolution.Source.NamedRef
		case workflow.ExecutionTargetSourceDetachedCommit:
			source.Kind = workflow.ExecutionTargetNegotiationSourceDetachedCommit
		default:
			return workflow.ExecutionTargetNegotiationSource{}, fmt.Errorf("unexpected execution target source kind %q", resolution.Source.Kind)
		}
		return source, nil
	}
	var resolutionErr *worktree.ExecutionTargetResolutionError
	if errors.As(err, &resolutionErr) {
		switch resolutionErr.Kind {
		case worktree.ExecutionTargetResolutionFailureNonGit:
			return workflow.ExecutionTargetNegotiationSource{Kind: workflow.ExecutionTargetNegotiationSourceNonGit}, nil
		case worktree.ExecutionTargetResolutionFailureUnavailable, worktree.ExecutionTargetResolutionFailureInvalidRef:
			return workflow.ExecutionTargetNegotiationSource{Kind: workflow.ExecutionTargetNegotiationSourceUnavailable}, nil
		}
	}
	return workflow.ExecutionTargetNegotiationSource{}, err
}

func taskExecutionTargetSelectionRequiredResult(negotiation workflow.ExecutionTargetNegotiation, policy workflow.ExecutionPolicy) *serverapi.WorkflowTaskInitiatingActionResult {
	var recoveryCause *serverapi.WorkflowTaskExecutionTargetRecoveryCause
	if negotiation.RecoveryCause != nil {
		cause := serverapi.WorkflowTaskExecutionTargetRecoveryCause(*negotiation.RecoveryCause)
		recoveryCause = &cause
	}
	return &serverapi.WorkflowTaskInitiatingActionResult{
		Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeSelectionRequired,
		SelectionRequired: &serverapi.WorkflowTaskExecutionTargetSelectionRequired{
			TaskID:              string(negotiation.TaskID),
			Generation:          negotiation.Generation,
			SourceWorkspaceID:   negotiation.SourceWorkspaceID,
			Source:              taskExecutionTargetSource(negotiation.Source),
			SupportedSelections: []serverapi.WorkflowTaskExecutionTargetSelectionMode{serverapi.WorkflowTaskExecutionTargetSelectionNone, serverapi.WorkflowTaskExecutionTargetSelectionHead, serverapi.WorkflowTaskExecutionTargetSelectionDefaultBranch, serverapi.WorkflowTaskExecutionTargetSelectionCustomRef},
			ConfiguredPolicy:    workflowview.ExecutionPolicyDTO(policy),
			RecoveryCause:       recoveryCause,
		},
	}
}

func manualExecutionTargetReplacement(prepared workflowstore.TaskExecutionTargetNegotiationPreparation) *workflow.ExecutionTargetNegotiation {
	if prepared.ExistingTarget == nil ||
		prepared.ExistingTarget.ActiveClaim != nil ||
		prepared.ExistingTarget.RecoveryDisposition != workflow.ExecutionTargetRecoveryManualRecovery ||
		prepared.ExistingNegotiation == nil {
		return nil
	}
	return prepared.ExistingNegotiation
}

func taskExecutionTargetInProgressResult(taskID workflow.TaskID, phase workflow.ExecutionTargetClaimPhase) *serverapi.WorkflowTaskInitiatingActionResult {
	return &serverapi.WorkflowTaskInitiatingActionResult{
		Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeInProgress,
		InProgress: &serverapi.WorkflowTaskExecutionTargetMaterializationProgress{
			TaskID: string(taskID),
			Phase:  serverapi.WorkflowTaskExecutionTargetMaterializationPhase(phase),
		},
	}
}

func taskExecutionTargetNegotiationConflictResult(taskID workflow.TaskID) *serverapi.WorkflowTaskInitiatingActionResult {
	return &serverapi.WorkflowTaskInitiatingActionResult{
		Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeConflict,
		Conflict: &serverapi.WorkflowTaskExecutionTargetNegotiationConflict{
			TaskID: string(taskID),
		},
	}
}

func executionTargetNegotiationConflict(err error) bool {
	return errors.Is(err, workflowstore.ErrTaskExecutionTargetNegotiationChanged) ||
		errors.Is(err, workflowstore.ErrTaskExecutionTargetNegotiationRequired) ||
		errors.Is(err, workflowstore.ErrTaskExecutionTargetNegotiationInProgress)
}

func taskExecutionTargetSource(source workflow.ExecutionTargetNegotiationSource) serverapi.WorkflowTaskExecutionTargetSource {
	result := serverapi.WorkflowTaskExecutionTargetSource{
		Kind:     serverapi.WorkflowTaskExecutionTargetSourceKind(source.Kind),
		NamedRef: source.NamedRef,
		Commit:   source.Commit,
	}
	return result
}

func executionTargetNegotiationActionMatches(left workflow.ExecutionTargetNegotiationAction, right workflow.ExecutionTargetNegotiationAction) bool {
	return left.Kind == right.Kind &&
		executionTargetNegotiationOptionalValueMatches(left.StartPlacementID, right.StartPlacementID) &&
		executionTargetNegotiationOptionalValueMatches(left.MoveSourcePlacementID, right.MoveSourcePlacementID) &&
		executionTargetNegotiationOptionalValueMatches(left.MoveTargetNodeID, right.MoveTargetNodeID) &&
		executionTargetNegotiationOptionalValueMatches(left.ApprovalTransitionID, right.ApprovalTransitionID)
}

func executionTargetNegotiationSourceMatches(left workflow.ExecutionTargetNegotiationSource, right workflow.ExecutionTargetNegotiationSource) bool {
	return left.Kind == right.Kind &&
		executionTargetNegotiationOptionalValueMatches(left.NamedRef, right.NamedRef) &&
		executionTargetNegotiationOptionalValueMatches(left.Commit, right.Commit)
}

func executionTargetNegotiationOptionalValueMatches[T comparable](left *T, right *T) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func stringPointer(value string) *string {
	return &value
}

func (s *Service) InterruptWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskInterruptRequest) (serverapi.WorkflowTaskInterruptResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskInterruptResponse{}, err
	}
	// workflow.task.interrupt is always operator-initiated; persist the canonical
	// non-attention reason even when the caller supplied custom display text.
	interrupted, err := s.store.InterruptTaskRuns(ctx, workflow.TaskID(req.TaskID), req.SessionID, workflowattention.InterruptionReasonUserInterrupt)
	if err != nil {
		return serverapi.WorkflowTaskInterruptResponse{}, err
	}
	cancelErr := s.cancelInterruptedRuntimes(ctx, workflow.TaskID(req.TaskID), req.SessionID, interrupted)
	if detail, detailErr := s.view.GetTask(ctx, req.TaskID); detailErr == nil {
		s.publishWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, "task", "interrupted", req.TaskID)
	}
	if cancelErr != nil {
		return serverapi.WorkflowTaskInterruptResponse{}, cancelErr
	}
	return serverapi.WorkflowTaskInterruptResponse{Runs: workflowTaskRunSummaries(interrupted)}, nil
}

func (s *Service) cancelInterruptedRuntimes(ctx context.Context, taskID workflow.TaskID, sessionID string, interrupted []workflowstore.RunRecord) error {
	if s.runtimeCancel == nil {
		return nil
	}
	if strings.TrimSpace(sessionID) == "" {
		return s.runtimeCancel.CancelTaskRuns(ctx, taskID)
	}
	canceler, ok := s.runtimeCancel.(taskRuntimeRunCanceler)
	if !ok {
		return nil
	}
	errs := make([]error, 0, len(interrupted))
	for _, run := range interrupted {
		if err := canceler.CancelRun(ctx, run.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func workflowTaskRunSummaries(runs []workflowstore.RunRecord) []serverapi.WorkflowTaskRunSummary {
	summaries := make([]serverapi.WorkflowTaskRunSummary, 0, len(runs))
	for _, run := range runs {
		summaries = append(summaries, serverapi.WorkflowTaskRunSummary{PlacementID: string(run.PlacementID), NodeID: string(run.NodeID), Generation: run.Generation, SessionID: run.SessionID})
	}
	return summaries
}

func (s *Service) ResumeWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskResumeResponse{}, err
	}
	resumed, err := s.store.ResumeTaskRuns(ctx, workflow.TaskID(req.TaskID))
	if err != nil {
		return serverapi.WorkflowTaskResumeResponse{}, err
	}
	s.resolveWorkflowInterruptedRunAttention(ctx, runIDsFromRecords(resumed))
	if s.schedulerWake != nil {
		s.schedulerWake.Notify()
	}
	if detail, detailErr := s.view.GetTask(ctx, req.TaskID); detailErr == nil {
		s.publishWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, "task", "resumed", req.TaskID)
	}
	return serverapi.WorkflowTaskResumeResponse{Runs: workflowTaskRunSummaries(resumed)}, nil
}

type TaskAutomationStartRequest struct {
	TaskID           string
	SetupOperationID serverapi.WorktreeSetupOperationID
}

func (s *Service) StartTaskAutomation(ctx context.Context, taskID string) (workflowstore.StartTaskResult, error) {
	return s.startTaskAutomation(ctx, TaskAutomationStartRequest{TaskID: taskID, SetupOperationID: serverapi.NewWorktreeSetupOperationID()})
}

func (s *Service) StartTaskAutomationWithSetup(ctx context.Context, req TaskAutomationStartRequest) (workflowstore.StartTaskResult, error) {
	return s.startTaskAutomation(ctx, req)
}

func (s *Service) startTaskAutomation(ctx context.Context, req TaskAutomationStartRequest) (workflowstore.StartTaskResult, error) {
	taskID := strings.TrimSpace(req.TaskID)
	target, err := s.store.GetTaskExecutionTarget(ctx, workflow.TaskID(taskID))
	if err != nil {
		return workflowstore.StartTaskResult{}, err
	}
	if target == nil && s.taskWorktrees != nil {
		if err := s.store.ValidateTaskStart(ctx, workflow.TaskID(taskID)); err != nil {
			return workflowstore.StartTaskResult{}, err
		}
		if err := s.taskWorktrees.EnsureTaskWorktree(ctx, TaskWorktreeEnsureRequest{TaskID: workflow.TaskID(taskID), SetupOperationID: req.SetupOperationID}); err != nil {
			return workflowstore.StartTaskResult{}, err
		}
	}
	started, err := s.store.StartTask(ctx, workflow.TaskID(taskID))
	if err != nil {
		return workflowstore.StartTaskResult{}, err
	}
	if s.schedulerWake != nil {
		s.schedulerWake.Notify()
	}
	return started, nil
}

func (s *Service) ApproveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskApproveRequest) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskInitiatingActionResult{}, err
	}
	transitionID := strings.TrimSpace(req.TaskTransitionID)
	if transitionID == "" {
		transitionID = strings.TrimSpace(req.TransitionID)
	}
	taskID, projectID, workflowID, err := s.store.TaskIdentityForTransition(ctx, workflow.TransitionID(transitionID))
	if err != nil {
		return serverapi.WorkflowTaskInitiatingActionResult{}, err
	}
	prepared, requiresExecutionTarget, err := s.store.PrepareApprovalExecutionTargetNegotiation(ctx, workflow.TransitionID(transitionID))
	if err != nil {
		return serverapi.WorkflowTaskInitiatingActionResult{}, err
	}
	if requiresExecutionTarget {
		materialization, materializationErr := s.materializeTaskExecutionTargetForAction(ctx, prepared, req.SelectionGeneration, req.Selection, req.SetupOperationID)
		if materializationErr != nil {
			return serverapi.WorkflowTaskInitiatingActionResult{}, materializationErr
		}
		if materialization.conflict != nil {
			return *materialization.conflict, nil
		}
		if materialization.selectionRequired != nil {
			return *materialization.selectionRequired, nil
		}
		if materialization.none {
			var approved workflowstore.CompleteRunResult
			var approveErr error
			if materialization.manualReplacement != nil {
				approved, approveErr = s.store.ReplaceManualExecutionTargetAndApprove(ctx, noneExecutionTarget(prepared.TaskID), workflow.TransitionID(transitionID), *materialization.manualReplacement)
			} else {
				approved, approveErr = s.store.ApproveTransitionWithExecutionTarget(ctx, noneExecutionTarget(prepared.TaskID), workflow.TransitionID(transitionID))
			}
			if approveErr != nil {
				return serverapi.WorkflowTaskInitiatingActionResult{}, approveErr
			}
			return s.approvedWorkflowTaskResult(ctx, approved, taskID, projectID, workflowID, transitionID)
		}
	}
	approved, err := s.approveTransition(ctx, workflow.TransitionID(transitionID), taskID, req.SetupOperationID)
	if err != nil {
		return serverapi.WorkflowTaskInitiatingActionResult{}, err
	}
	return s.approvedWorkflowTaskResult(ctx, approved, taskID, projectID, workflowID, transitionID)
}

func (s *Service) approvedWorkflowTaskResult(ctx context.Context, approved workflowstore.CompleteRunResult, taskID string, projectID string, workflowID string, transitionID string) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	s.finalizeWorkflowAttention(ctx, approved)
	if s.schedulerWake != nil {
		s.schedulerWake.Notify()
	}
	s.publishWorkflowEvent(ctx, projectID, workflowID, "task", "approved", taskID, transitionID)
	return serverapi.WorkflowTaskInitiatingActionResult{
		Outcome:  serverapi.WorkflowTaskInitiatingActionOutcomeApproved,
		Approved: &serverapi.WorkflowTaskApproveResponse{TransitionID: string(approved.TransitionID), TaskID: taskID, State: approved.State, PlacementIDs: placementIDs(approved.PlacementIDs), RunIDs: runIDs(approved.RunIDs)},
	}, nil
}

func (s *Service) MoveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskInitiatingActionResult{}, err
	}
	moveRequest := workflowstore.ManualMoveRequest{TaskID: workflow.TaskID(req.TaskID), TargetNodeID: workflow.NodeID(req.TargetNodeID), OutputValues: req.OutputValues, Commentary: req.Commentary, Actor: "user", AllowMissingEdge: req.AllowMissingEdge}
	prepared, requiresExecutionTarget, err := s.store.PrepareManualMoveExecutionTargetNegotiation(ctx, moveRequest)
	if err != nil {
		return serverapi.WorkflowTaskInitiatingActionResult{}, err
	}
	if requiresExecutionTarget {
		materialization, materializationErr := s.materializeTaskExecutionTargetForAction(ctx, prepared, req.SelectionGeneration, req.Selection, req.SetupOperationID)
		if materializationErr != nil {
			return serverapi.WorkflowTaskInitiatingActionResult{}, materializationErr
		}
		if materialization.conflict != nil {
			return *materialization.conflict, nil
		}
		if materialization.selectionRequired != nil {
			return *materialization.selectionRequired, nil
		}
		if materialization.none {
			var moved workflowstore.ManualMoveResult
			var moveErr error
			if materialization.manualReplacement != nil {
				moved, moveErr = s.store.ReplaceManualExecutionTargetAndMove(ctx, noneExecutionTarget(prepared.TaskID), moveRequest, *materialization.manualReplacement)
			} else {
				moved, moveErr = s.store.ManualMoveTaskWithExecutionTarget(ctx, noneExecutionTarget(prepared.TaskID), moveRequest)
			}
			if moveErr != nil {
				return serverapi.WorkflowTaskInitiatingActionResult{}, moveErr
			}
			return s.movedWorkflowTaskResult(ctx, req, moved)
		}
		if err := s.store.ValidateManualMoveExecutionScripts(ctx, moveRequest); err != nil {
			return serverapi.WorkflowTaskInitiatingActionResult{}, err
		}
	}
	moved, err := s.store.ManualMoveTask(ctx, moveRequest)
	if err != nil {
		return serverapi.WorkflowTaskInitiatingActionResult{}, err
	}
	return s.movedWorkflowTaskResult(ctx, req, moved)
}

func (s *Service) movedWorkflowTaskResult(ctx context.Context, req serverapi.WorkflowTaskMoveRequest, moved workflowstore.ManualMoveResult) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	approvalError := ""
	resolvedApprovals := append([]workflow.TransitionID(nil), moved.ResolvedApprovalTransitionIDs...)
	if req.AutoApprove && moved.State == "pending_approval" && !moved.RequiresApproval {
		approved, approveErr := s.approveTransition(ctx, moved.TransitionID, req.TaskID, req.SetupOperationID)
		if approveErr != nil {
			var ensureErr taskWorktreeEnsureError
			if errors.As(approveErr, &ensureErr) {
				return serverapi.WorkflowTaskInitiatingActionResult{}, approveErr
			}
			approvalError = approveErr.Error()
		} else {
			approved.ResolvedApprovalTransitionIDs = append(resolvedApprovals, approved.ResolvedApprovalTransitionIDs...)
			moved = approved
		}
	}
	s.finalizeWorkflowAttention(ctx, moved)
	if s.schedulerWake != nil {
		s.schedulerWake.Notify()
	}
	if detail, detailErr := s.view.GetTask(ctx, req.TaskID); detailErr == nil {
		s.publishWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, "task", "moved", req.TaskID, string(moved.TransitionID))
	}
	return serverapi.WorkflowTaskInitiatingActionResult{
		Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeMoved,
		Moved:   &serverapi.WorkflowTaskMoveResponse{TransitionID: string(moved.TransitionID), State: moved.State, PlacementIDs: placementIDs(moved.PlacementIDs), RunIDs: runIDs(moved.RunIDs), ApprovalError: approvalError},
	}, nil
}

func (s *Service) approveTransition(ctx context.Context, transitionID workflow.TransitionID, taskID string, setupOperationID serverapi.WorktreeSetupOperationID) (workflowstore.CompleteRunResult, error) {
	resolvedTaskID := strings.TrimSpace(taskID)
	if resolvedTaskID == "" {
		var err error
		resolvedTaskID, _, _, err = s.store.TaskIdentityForTransition(ctx, transitionID)
		if err != nil {
			return workflowstore.CompleteRunResult{}, err
		}
	}
	target, err := s.store.GetTaskExecutionTarget(ctx, workflow.TaskID(resolvedTaskID))
	if err != nil {
		return workflowstore.CompleteRunResult{}, err
	}
	if target != nil {
		return s.approve(ctx, transitionID)
	}
	if s.taskWorktrees != nil {
		requiresWorktree, err := s.store.PendingTransitionTargetsExecutableNode(ctx, transitionID)
		if err != nil {
			return workflowstore.CompleteRunResult{}, err
		}
		if requiresWorktree {
			if err := s.taskWorktrees.EnsureTaskWorktree(ctx, TaskWorktreeEnsureRequest{TaskID: workflow.TaskID(resolvedTaskID), SetupOperationID: setupOperationID}); err != nil {
				return workflowstore.CompleteRunResult{}, taskWorktreeEnsureError{err: err}
			}
		}
	}
	return s.approve(ctx, transitionID)
}

func (s *Service) CompleteWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCompleteRequest) (serverapi.WorkflowTaskCompleteResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskCompleteResponse{}, err
	}
	target, err := s.store.ResolveActiveRunCompletionTarget(ctx, workflowCompletionTargetSelector(req))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return serverapi.WorkflowTaskCompleteResponse{}, serverapi.ErrWorkflowTaskCompleteTargetNotFound
		}
		if errors.Is(err, workflowstore.ErrRunIDRequired) {
			return serverapi.WorkflowTaskCompleteResponse{}, serverapi.WorkflowTaskCompleteSelectorAmbiguousError{Message: "completion selector matched multiple active workflow runs"}
		}
		return serverapi.WorkflowTaskCompleteResponse{}, err
	}
	actor := "agent"
	if req.ActorKind == serverapi.WorkflowTaskCompleteActorUser {
		actor = "user"
	} else if strings.TrimSpace(target.Run.SessionID) != strings.TrimSpace(req.AgentSessionID) {
		return serverapi.WorkflowTaskCompleteResponse{}, errors.New(prompts.WorkflowTaskCompleteAgentOwnershipErrorPrompt)
	}
	taskID := string(target.Run.TaskID)
	completed, err := s.store.CompleteRun(ctx, workflowstore.CompleteRunRequest{
		RunID:              target.Run.ID,
		TransitionID:       req.TransitionID,
		OutputValues:       req.OutputValues,
		Commentary:         req.Commentary,
		Actor:              actor,
		ExpectedGeneration: target.Run.Generation,
		RequireGeneration:  true,
	})
	if err != nil {
		return serverapi.WorkflowTaskCompleteResponse{}, err
	}
	s.finalizeWorkflowAttention(ctx, completed)
	if req.ActorKind == serverapi.WorkflowTaskCompleteActorUser {
		notifyScheduler := true
		if requester, ok := s.runtimeCancel.(taskRuntimeRunCancelRequester); ok {
			notifyScheduler = !requester.RequestCancelRun(target.Run.ID)
		} else if canceler, ok := s.runtimeCancel.(taskRuntimeRunCanceler); ok {
			if err := canceler.CancelRun(ctx, target.Run.ID); err != nil {
				slog.Warn("cancel completed workflow run failed", "run_id", string(target.Run.ID), "task_id", taskID, "error", err)
			}
		}
		if notifyScheduler && s.schedulerWake != nil {
			s.schedulerWake.Notify()
		}
	}
	return serverapi.WorkflowTaskCompleteResponse{
		TransitionID: string(completed.TransitionID),
		TaskID:       taskID,
		RunID:        string(target.Run.ID),
		State:        completed.State,
		PlacementIDs: placementIDs(completed.PlacementIDs),
		RunIDs:       runIDs(completed.RunIDs),
	}, nil
}

func (s *Service) finalizeWorkflowAttention(ctx context.Context, result workflowstore.CompleteRunResult) {
	if s == nil || s.attentionFinalizer == nil {
		return
	}
	finalizeCtx, cancel := workflowAttentionContext(ctx)
	defer cancel()
	s.attentionFinalizer.FinalizeTransition(finalizeCtx, workflowattention.TransitionResult{
		TransitionID:                  result.TransitionID,
		State:                         result.State,
		ResolvedApprovalTransitionIDs: append([]workflow.TransitionID(nil), result.ResolvedApprovalTransitionIDs...),
	})
	for _, runID := range result.InterruptedRunIDs {
		if runID == "" {
			continue
		}
		runFinalizeCtx, runCancel := workflowAttentionContext(ctx)
		s.attentionFinalizer.FinalizeInterruptedRun(runFinalizeCtx, runID)
		runCancel()
	}
}

func workflowCompletionTargetSelector(req serverapi.WorkflowTaskCompleteRequest) workflowstore.ActiveRunCompletionTargetSelector {
	if req.ActorKind == serverapi.WorkflowTaskCompleteActorAgent && workflowTaskCompleteExplicitSelectorCount(req) == 0 {
		return workflowstore.ActiveRunCompletionTargetSelector{SessionID: strings.TrimSpace(req.AgentSessionID)}
	}
	return workflowstore.ActiveRunCompletionTargetSelector{
		RunID:     workflow.RunID(req.RunID),
		SessionID: strings.TrimSpace(req.SessionID),
		TaskID:    workflow.TaskID(req.TaskID),
		ProjectID: strings.TrimSpace(req.ProjectID),
		ShortID:   strings.TrimSpace(req.ShortID),
	}
}

func workflowTaskCompleteExplicitSelectorCount(req serverapi.WorkflowTaskCompleteRequest) int {
	count := 0
	for _, value := range []string{req.RunID, req.SessionID, req.TaskID, req.ShortID} {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}
	return count
}

func (s *Service) CancelWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCancelRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "user_canceled"
	}
	pendingApprovals, err := s.pendingApprovalTransitionIDs(ctx, workflow.TaskID(req.TaskID))
	if err != nil {
		return err
	}
	interruptedRunIDs := s.workflowInterruptedRunAttentionIDs(ctx, workflow.TaskID(req.TaskID))
	if err := s.store.CancelTask(ctx, workflow.TaskID(req.TaskID), reason); err != nil {
		return err
	}
	s.finalizeWorkflowAttention(ctx, workflowstore.CompleteRunResult{ResolvedApprovalTransitionIDs: pendingApprovals})
	s.resolveWorkflowInterruptedRunAttention(ctx, interruptedRunIDs)
	if detail, detailErr := s.view.GetTask(ctx, req.TaskID); detailErr == nil {
		s.publishWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, "task", "canceled", req.TaskID)
	}
	if s.runtimeCancel != nil {
		return s.runtimeCancel.CancelTaskRuns(ctx, workflow.TaskID(req.TaskID))
	}
	return nil
}

func (s *Service) DeleteWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskDeleteRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	// Preflight blockers that canceling this task's runs cannot clear (e.g. a
	// shared worktree still managed by another non-terminal task) before stopping
	// any automation, so a delete that would fail does not leave the task stopped
	// yet undeleted.
	if s.taskWorktreeCleanup != nil {
		if err := s.taskWorktreeCleanup.EnsureTaskWorktreeDeletable(ctx, req.TaskID); err != nil {
			return err
		}
	}
	pendingApprovals, err := s.pendingApprovalTransitionProjections(ctx, workflow.TaskID(req.TaskID))
	if err != nil {
		return err
	}
	interruptedRunIDs := s.workflowInterruptedRunAttentionIDs(ctx, workflow.TaskID(req.TaskID))
	if s.runtimeCancel != nil {
		if err := s.runtimeCancel.CancelTaskRuns(ctx, workflow.TaskID(req.TaskID)); err != nil {
			return err
		}
	}
	if s.taskWorktreeCleanup != nil {
		if err := s.taskWorktreeCleanup.DeleteTaskWorktree(ctx, req.TaskID); err != nil {
			return err
		}
	}
	task, err := s.store.DeleteTask(ctx, workflow.TaskID(req.TaskID))
	if err != nil {
		return err
	}
	s.finalizeWorkflowApprovalProjections(ctx, pendingApprovals)
	s.resolveWorkflowInterruptedRunAttention(ctx, interruptedRunIDs)
	s.publishWorkflowEvent(ctx, task.ProjectID, string(task.WorkflowID), "task", "deleted", req.TaskID)
	return nil
}

func (s *Service) resolveWorkflowInterruptedRunAttention(ctx context.Context, runIDs []workflow.RunID) {
	if s == nil || len(runIDs) == 0 {
		return
	}
	finalizer, ok := s.attentionFinalizer.(workflowInterruptedRunFinalizer)
	if !ok {
		return
	}
	for _, runID := range runIDs {
		if runID == "" {
			continue
		}
		finalizeCtx, cancel := workflowAttentionContext(ctx)
		finalizer.ResolveInterruptedRun(finalizeCtx, runID)
		cancel()
	}
}

func (s *Service) workflowInterruptedRunAttentionIDs(ctx context.Context, taskID workflow.TaskID) []workflow.RunID {
	if s == nil || s.view == nil || taskID == "" {
		return nil
	}
	attention, err := s.view.ListTaskAttention(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(taskID)}, s.roleResolver)
	if err != nil {
		slog.Warn("workflow interrupted-run attention resolution lookup failed", "task_id", string(taskID), "error", err)
		return nil
	}
	runIDs := make([]workflow.RunID, 0)
	for _, item := range attention.Items {
		if item.Kind == "interrupted_run" && strings.TrimSpace(item.RunID) != "" {
			runIDs = append(runIDs, workflow.RunID(item.RunID))
		}
	}
	return runIDs
}

func (s *Service) workflowInterruptedRunAttentionIDsByWorkflow(ctx context.Context, workflowID string, links []workflowstore.ProjectWorkflowLinkRecord) []workflow.RunID {
	if s == nil || s.view == nil || strings.TrimSpace(workflowID) == "" {
		return nil
	}
	projectIDs := make([]string, 0, len(links))
	seenProjects := map[string]bool{}
	for _, link := range links {
		projectID := strings.TrimSpace(link.ProjectID)
		if projectID == "" || seenProjects[projectID] {
			continue
		}
		seenProjects[projectID] = true
		projectIDs = append(projectIDs, projectID)
	}
	runIDs := make([]workflow.RunID, 0)
	seenRuns := map[string]bool{}
	for _, projectID := range projectIDs {
		pageToken := ""
		for {
			attention, err := s.view.ListAttention(ctx, serverapi.WorkflowAttentionListRequest{
				ProjectID: projectID,
				PageSize:  workflowAttentionResolutionPageSize,
				PageToken: pageToken,
			}, s.roleResolver)
			if err != nil {
				slog.Warn("workflow interrupted-run attention resolution lookup failed", "workflow_id", workflowID, "project_id", projectID, "error", err)
				break
			}
			for _, item := range attention.Items {
				runID := strings.TrimSpace(item.RunID)
				if item.Kind == "interrupted_run" && item.WorkflowID == workflowID && runID != "" && !seenRuns[runID] {
					seenRuns[runID] = true
					runIDs = append(runIDs, workflow.RunID(runID))
				}
			}
			if attention.NextPageToken == "" {
				break
			}
			pageToken = attention.NextPageToken
		}
	}
	return runIDs
}

func runIDsFromRecords(runs []workflowstore.RunRecord) []workflow.RunID {
	out := make([]workflow.RunID, 0, len(runs))
	for _, run := range runs {
		out = append(out, run.ID)
	}
	return out
}

func (s *Service) finalizeWorkflowApprovalProjections(ctx context.Context, projections []workflowstore.ApprovalTransitionProjection) {
	if s == nil || s.attentionFinalizer == nil || len(projections) == 0 {
		return
	}
	resolved := make([]workflowattention.ApprovalProjection, 0, len(projections))
	for _, projection := range projections {
		resolved = append(resolved, workflowattention.ApprovalProjection{
			TransitionID:     projection.TransitionID,
			ProjectID:        projection.ProjectID,
			WorkflowID:       projection.WorkflowID,
			TaskID:           projection.TaskID,
			TaskShortID:      projection.TaskShortID,
			TaskTitle:        projection.TaskTitle,
			RunID:            string(projection.SourceRunID),
			SessionID:        projection.SessionID,
			Message:          "action required",
			OccurredAtUnixMs: projection.OccurredAtUnixMs,
		})
	}
	finalizeCtx, cancel := workflowAttentionContext(ctx)
	defer cancel()
	s.attentionFinalizer.FinalizeTransition(finalizeCtx, workflowattention.TransitionResult{ResolvedApprovalProjections: resolved})
}

func workflowAttentionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), workflowAttentionFinalizationTimeout)
	}
	return context.WithTimeout(context.WithoutCancel(ctx), workflowAttentionFinalizationTimeout)
}

func (s *Service) pendingApprovalTransitionIDs(ctx context.Context, taskID workflow.TaskID) ([]workflow.TransitionID, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	return s.store.ListPendingApprovalTransitionIDs(ctx, taskID)
}

func (s *Service) pendingApprovalTransitionProjections(ctx context.Context, taskID workflow.TaskID) ([]workflowstore.ApprovalTransitionProjection, error) {
	transitionIDs, err := s.pendingApprovalTransitionIDs(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]workflowstore.ApprovalTransitionProjection, 0, len(transitionIDs))
	for _, transitionID := range transitionIDs {
		projection, err := s.store.ApprovalTransitionProjection(ctx, transitionID)
		if err != nil {
			return nil, err
		}
		out = append(out, projection)
	}
	return out, nil
}

func (s *Service) ListWorkflowAttention(ctx context.Context, req serverapi.WorkflowAttentionListRequest) (serverapi.WorkflowAttentionListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowAttentionListResponse{}, err
	}
	return s.view.ListAttention(ctx, req, s.roleResolver)
}

func (s *Service) ListWorkflowTaskAttention(ctx context.Context, req serverapi.WorkflowTaskAttentionListRequest) (serverapi.WorkflowTaskAttentionListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
	return s.view.ListTaskAttention(ctx, req, s.roleResolver)
}

func (s *Service) AnswerWorkflowTaskQuestion(ctx context.Context, req serverapi.WorkflowTaskQuestionAnswerRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if s == nil || s.prompts == nil {
		return errors.New("prompt responder is required")
	}
	memoReq := taskQuestionAnswerMemoRequest{TaskID: req.TaskID, RunID: req.RunID, AskID: req.AskID, ErrorMessage: req.ErrorMessage, Answer: req.Answer, SelectedOptionNumber: req.SelectedOptionNumber, FreeformAnswer: req.FreeformAnswer}
	if req.Approval != nil {
		memoReq.ApprovalDecision = req.Approval.Decision
		memoReq.ApprovalCommentary = req.Approval.Commentary
	}
	_, err := s.questionMemo.Do(ctx, req.ClientRequestID, memoReq, sameTaskQuestionAnswerMemoRequest, func(ctx context.Context) (struct{}, error) {
		run, err := s.store.ResolveTaskWaitingAsk(ctx, workflow.TaskID(req.TaskID), workflow.RunID(req.RunID), req.AskID)
		if err != nil {
			return struct{}{}, err
		}
		if strings.TrimSpace(req.ErrorMessage) != "" {
			if err := s.prompts.SubmitPromptResponse(run.SessionID, askquestion.AskQuestionResponse{RequestID: req.AskID}, errors.New(req.ErrorMessage)); err != nil {
				return struct{}{}, err
			}
		} else {
			response := askquestion.AskQuestionResponse{RequestID: req.AskID, Answer: req.Answer, SelectedOptionNumber: req.SelectedOptionNumber, FreeformAnswer: req.FreeformAnswer}
			if req.Approval != nil {
				response = askquestion.AskQuestionResponse{
					RequestID: req.AskID,
					Approval: &askquestion.AskQuestionApprovalPayload{
						Decision:   askquestion.AskQuestionApprovalDecision(req.Approval.Decision),
						Commentary: req.Approval.Commentary,
					},
				}
			}
			if err := s.prompts.SubmitPromptResponse(run.SessionID, response, nil); err != nil {
				return struct{}{}, err
			}
		}
		if detail, detailErr := s.view.GetTask(ctx, req.TaskID); detailErr == nil {
			s.publishWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, "task", "question_answered", req.TaskID, string(run.ID), req.AskID)
		}
		return struct{}{}, nil
	})
	return err
}

func sameTaskQuestionAnswerMemoRequest(a taskQuestionAnswerMemoRequest, b taskQuestionAnswerMemoRequest) bool {
	return a.TaskID == b.TaskID &&
		a.RunID == b.RunID &&
		a.AskID == b.AskID &&
		a.ErrorMessage == b.ErrorMessage &&
		a.Answer == b.Answer &&
		a.SelectedOptionNumber == b.SelectedOptionNumber &&
		a.FreeformAnswer == b.FreeformAnswer &&
		a.ApprovalDecision == b.ApprovalDecision &&
		a.ApprovalCommentary == b.ApprovalCommentary
}

func (s *Service) AddWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentAddRequest) (serverapi.WorkflowTaskCommentAddResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskCommentAddResponse{}, err
	}
	comment, err := s.store.AddComment(ctx, workflow.TaskID(req.TaskID), req.Body, req.Author, req.AuthorID)
	if err != nil {
		return serverapi.WorkflowTaskCommentAddResponse{}, err
	}
	if detail, detailErr := s.view.GetTask(ctx, req.TaskID); detailErr == nil {
		s.publishWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, "task", "comment_added", req.TaskID, comment.ID)
	}
	return serverapi.WorkflowTaskCommentAddResponse{Comment: commentRecord(comment)}, nil
}

func (s *Service) ListWorkflowTaskComments(ctx context.Context, req serverapi.WorkflowTaskCommentListRequest) (serverapi.WorkflowTaskCommentListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskCommentListResponse{}, err
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = 100
	}
	cursor, err := parseCommentPageToken(req.PageToken)
	if err != nil {
		return serverapi.WorkflowTaskCommentListResponse{}, err
	}
	comments, err := s.store.ListCommentsPage(ctx, workflow.TaskID(req.TaskID), cursor, pageSize+1)
	if err != nil {
		return serverapi.WorkflowTaskCommentListResponse{}, err
	}
	nextPageToken := ""
	if len(comments) > pageSize {
		comments = comments[:pageSize]
		nextPageToken = commentPageToken(comments[len(comments)-1])
	}
	out := make([]serverapi.WorkflowTaskComment, 0, len(comments))
	for _, comment := range comments {
		out = append(out, commentRecord(comment))
	}
	return serverapi.WorkflowTaskCommentListResponse{Comments: out, NextPageToken: nextPageToken}, nil
}

// parseCommentPageToken decodes a stable keyset cursor (created_at|base64(id)),
// mirroring the task-activity page token so concurrent comment inserts/deletes
// can't shift an in-flight infinite-scroll page.
func parseCommentPageToken(token string) (workflowstore.CommentPageCursor, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return workflowstore.CommentPageCursor{}, nil
	}
	timestampPart, encodedID, ok := strings.Cut(trimmed, "|")
	if !ok {
		return workflowstore.CommentPageCursor{}, errors.New("page_token is invalid")
	}
	createdAt, err := strconv.ParseInt(timestampPart, 10, 64)
	if err != nil || createdAt < 0 {
		return workflowstore.CommentPageCursor{}, errors.New("page_token is invalid")
	}
	decodedID, err := base64.RawURLEncoding.DecodeString(encodedID)
	if err != nil || strings.TrimSpace(string(decodedID)) == "" {
		return workflowstore.CommentPageCursor{}, errors.New("page_token is invalid")
	}
	return workflowstore.CommentPageCursor{CreatedAtUnixMs: createdAt, ID: string(decodedID), HasValue: true}, nil
}

func commentPageToken(comment workflowstore.CommentRecord) string {
	return strconv.FormatInt(comment.CreatedAt, 10) + "|" + base64.RawURLEncoding.EncodeToString([]byte(comment.ID))
}

func (s *Service) ReplaceWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentReplaceRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	taskID, projectID, workflowID, err := s.store.TaskIdentityForComment(ctx, strings.TrimSpace(req.CommentID))
	if err != nil {
		return err
	}
	if err := s.store.ReplaceComment(ctx, req.CommentID, req.Body); err != nil {
		return err
	}
	s.publishWorkflowEvent(ctx, projectID, workflowID, "task", "comment_updated", taskID, req.CommentID)
	return nil
}

func (s *Service) DeleteWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentDeleteRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	taskID, projectID, workflowID, err := s.store.TaskIdentityForComment(ctx, strings.TrimSpace(req.CommentID))
	if err != nil {
		return err
	}
	if err := s.store.DeleteComment(ctx, req.CommentID); err != nil {
		return err
	}
	s.publishWorkflowEvent(ctx, projectID, workflowID, "task", "comment_deleted", taskID, req.CommentID)
	return nil
}

func (s *Service) ListWorkflowTaskActivity(ctx context.Context, req serverapi.WorkflowTaskActivityListRequest) (serverapi.WorkflowTaskActivityListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	return s.view.ListTaskActivity(ctx, req)
}

func (s *Service) ListWorkflowTasks(ctx context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	return s.view.ListTasks(ctx, req, s.roleResolver)
}

func (s *Service) GetWorkflowBoard(ctx context.Context, req serverapi.WorkflowBoardRequest) (serverapi.WorkflowBoardResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowBoardResponse{}, err
	}
	board, err := s.view.GetBoard(ctx, req, s.roleResolver)
	if err != nil {
		return serverapi.WorkflowBoardResponse{}, err
	}
	return serverapi.WorkflowBoardResponse{Board: board}, nil
}

func (s *Service) ListWorkflowBoardNodeCards(ctx context.Context, req serverapi.WorkflowBoardNodeCardsListRequest) (serverapi.WorkflowBoardNodeCardsListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	return s.view.ListBoardNodeCards(ctx, req, s.roleResolver)
}

func (s *Service) SubscribeWorkflowProject(ctx context.Context, req serverapi.WorkflowProjectSubscribeRequest) (serverapi.WorkflowProjectSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return s.events.subscribe(strings.TrimSpace(req.ProjectID), "")
}

func (s *Service) SubscribeWorkflow(ctx context.Context, req serverapi.WorkflowSubscribeRequest) (serverapi.WorkflowSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	workflowID := strings.TrimSpace(req.WorkflowID)
	if _, err := s.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID}); err != nil {
		return nil, err
	}
	return s.events.subscribe("", workflowID)
}

func (s *Service) GetWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskGetResponse{}, err
	}
	var (
		detail serverapi.WorkflowTaskDetail
		err    error
	)
	if strings.TrimSpace(req.TaskID) != "" {
		detail, err = s.view.GetTask(ctx, req.TaskID)
	} else if strings.TrimSpace(req.ProjectID) != "" {
		detail, err = s.view.GetTaskByProjectShortID(ctx, req.ProjectID, req.ShortID)
	} else {
		detail, err = s.view.GetTaskByShortID(ctx, req.ShortID)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return serverapi.WorkflowTaskGetResponse{}, errors.Join(serverapi.ErrWorkflowTaskNotFound, err)
		}
		return serverapi.WorkflowTaskGetResponse{}, err
	}
	return serverapi.WorkflowTaskGetResponse{Task: detail}, nil
}

func workflowRecord(row workflowstore.WorkflowRecord) serverapi.WorkflowRecord {
	return serverapi.WorkflowRecord{ID: string(row.ID), Name: row.Name, Description: row.Description, ExecutionPolicy: workflowview.ExecutionPolicyDTO(row.ExecutionPolicy), Version: row.Version}
}

func workflowExecutionPolicyFromAPI(policy *serverapi.WorkflowExecutionPolicy) *workflow.ExecutionPolicy {
	if policy == nil {
		return nil
	}
	var customRef *string
	if policy.CustomRef != nil {
		value := *policy.CustomRef
		customRef = &value
	}
	return &workflow.ExecutionPolicy{
		Mode:      workflow.ExecutionPolicyMode(policy.Mode),
		CustomRef: customRef,
	}
}

func workflowNodeGroup(row workflowstore.NodeGroupRecord) serverapi.WorkflowNodeGroup {
	return serverapi.WorkflowNodeGroup{GroupID: row.ID, WorkflowID: string(row.WorkflowID), GroupKey: string(row.Key), DisplayName: row.DisplayName, SortOrder: int(row.SortOrder)}
}

func projectWorkflowLink(row workflowstore.ProjectWorkflowLinkRecord) serverapi.ProjectWorkflowLink {
	return serverapi.ProjectWorkflowLink{ID: row.ID, ProjectID: row.ProjectID, WorkflowID: string(row.WorkflowID), Default: row.IsDefault}
}

func workflowStoreDefaultPolicy(policy serverapi.WorkflowProjectLinkDefaultMode) workflowstore.WorkflowLinkDefaultPolicy {
	switch policy {
	case serverapi.WorkflowProjectLinkDefaultAlways:
		return workflowstore.WorkflowLinkDefaultAlways
	case serverapi.WorkflowProjectLinkDefaultIfProjectHasNone:
		return workflowstore.WorkflowLinkDefaultIfProjectHasNone
	default:
		return workflowstore.WorkflowLinkDefaultNever
	}
}

func workflowUnlinkProjectResponse(result workflowstore.ProjectWorkflowUnlinkResult) serverapi.WorkflowUnlinkProjectResponse {
	resp := serverapi.WorkflowUnlinkProjectResponse{LinkID: result.LinkID, Unlinked: result.Unlinked}
	for _, blocker := range result.Blockers {
		dto := serverapi.WorkflowUnlinkProjectBlocker{Code: blocker.Code, Message: blocker.Message, Count: blocker.Count}
		for _, task := range blocker.Tasks {
			dto.Tasks = append(dto.Tasks, serverapi.WorkflowUnlinkTaskReference{TaskID: string(task.TaskID), ShortID: task.ShortID, Title: task.Title})
		}
		resp.Blockers = append(resp.Blockers, dto)
	}
	return resp
}

func workflowDeleteResponse(result workflowstore.WorkflowDeleteResult) serverapi.WorkflowDeleteResponse {
	resp := serverapi.WorkflowDeleteResponse{Deleted: result.Deleted, Impact: workflowDeleteImpact(result.Impact)}
	for _, blocker := range result.Blockers {
		resp.Blockers = append(resp.Blockers, serverapi.WorkflowDeleteBlocker{Code: blocker.Code, Message: blocker.Message, Count: blocker.Count})
	}
	return resp
}

func workflowDeleteImpact(impact workflowstore.WorkflowDeleteImpact) serverapi.WorkflowDeleteImpact {
	return serverapi.WorkflowDeleteImpact{
		WorkflowID:                     string(impact.WorkflowID),
		Version:                        impact.Version,
		ProjectCount:                   impact.ProjectCount,
		LinkCount:                      impact.LinkCount,
		DefaultReplacementProjectCount: impact.DefaultReplacementProjectCount,
		TaskCount:                      impact.TaskCount,
		ActiveRunCount:                 impact.ActiveRunCount,
		RunnableRunCount:               impact.RunnableRunCount,
		BlockedTaskCount:               impact.BlockedTaskCount,
	}
}

func (s *Service) workflowGraphValidationResults(ctx context.Context, workflowID string, metadata *serverapi.WorkflowGraphMetadata, graph serverapi.WorkflowGraphDraft, modes []serverapi.WorkflowValidationMode) (map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse, error) {
	def, err := s.workflowGraphDraftDefinition(ctx, workflowID, metadata, graph)
	if err != nil {
		return nil, err
	}
	return s.workflowGraphValidationResultsForDefinition(def, modes), nil
}

func (s *Service) workflowGraphValidationResultsForDefinition(def workflow.Definition, modes []serverapi.WorkflowValidationMode) map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse {
	out := make(map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse, len(modes))
	for _, mode := range modes {
		context := workflow.ValidationContext(mode)
		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: context, RoleResolver: s.roleResolver})
		resp := workflowValidationResponse(def.ID, result)
		if context == workflow.ValidationContextExecution {
			resp.Errors = append(resp.Errors, scriptPathValidationErrors(def, "")...)
			resp.Valid = workflowValidationErrorsValid(resp.Errors)
		}
		out[mode] = resp
	}
	return out
}

func scriptPathValidationErrors(def workflow.Definition, executionRoot string) []serverapi.WorkflowValidationError {
	out := []serverapi.WorkflowValidationError{}
	for _, node := range def.Nodes {
		if node.Kind() != workflow.NodeKindScript {
			continue
		}
		diagnostics := workflowscript.Validate(workflowscript.ValidationRequest{
			RawPath:       workflow.NodeScriptPath(node).String(),
			ExecutionRoot: executionRoot,
		})
		for _, diagnostic := range diagnostics {
			out = append(out, scriptPathValidationError(def.ID, workflow.NodeIDOf(node), diagnostic))
		}
	}
	return out
}

func scriptPathValidationError(workflowID workflow.WorkflowID, nodeID workflow.NodeID, diagnostic workflowscript.Diagnostic) serverapi.WorkflowValidationError {
	return serverapi.WorkflowValidationError{
		Code:          diagnostic.Code,
		Message:       diagnostic.Message,
		WorkflowID:    string(workflowID),
		NodeID:        string(nodeID),
		BlocksContext: diagnostic.Blocking,
	}
}

func workflowValidationErrorsValid(errors []serverapi.WorkflowValidationError) bool {
	for _, err := range errors {
		if err.BlocksContext {
			return false
		}
	}
	return true
}

func (s *Service) workflowGraphDraftDefinition(ctx context.Context, workflowID string, metadata *serverapi.WorkflowGraphMetadata, graph serverapi.WorkflowGraphDraft) (workflow.Definition, error) {
	current, _, err := s.store.GetDefinition(ctx, workflow.WorkflowID(workflowID))
	if err != nil {
		return workflow.Definition{}, err
	}
	displayName := current.DisplayName
	if metadata != nil {
		displayName = metadata.Name
	}
	def := workflow.Definition{ID: workflow.WorkflowID(workflowID), DisplayName: displayName}
	groupMemberIDs := map[string][]workflow.NodeID{}
	for _, group := range graph.NodeGroups {
		def.NodeGroups = append(def.NodeGroups, workflow.NodeGroup{
			WorkflowID:  workflow.WorkflowID(workflowID),
			ID:          group.ID,
			Key:         workflow.ModelKey(group.Key),
			DisplayName: group.DisplayName,
		})
	}
	for _, node := range graph.Nodes {
		if strings.TrimSpace(node.GroupID) != "" {
			groupMemberIDs[node.GroupID] = append(groupMemberIDs[node.GroupID], workflow.NodeID(node.ID))
		}
		workflowNode, err := workflow.NewNode(
			workflow.NodeIdentity{
				WorkflowID:  workflow.WorkflowID(workflowID),
				ID:          workflow.NodeID(node.ID),
				Key:         workflow.ModelKey(node.Key),
				DisplayName: node.DisplayName,
				GroupID:     node.GroupID,
			},
			workflow.NodeKind(node.Kind),
			workflow.NodeFields{
				SubagentRole:   node.SubagentRole,
				PromptTemplate: node.PromptTemplate,
				CompletionMode: node.CompletionMode,
				ScriptPath: func() workflow.OptionalScriptPath {
					if scriptPath, ok := workflow.PresentScriptPath(optionalStringValue(node.ScriptPath)); ok {
						return scriptPath
					}
					return workflow.AbsentScriptPath()
				}(),
				InputFields:        inputFields(node.InputFields),
				JoinInputProviders: joinInputProviders(node.JoinInputProviders),
			},
		)
		if err != nil {
			return workflow.Definition{}, err
		}
		def.Nodes = append(def.Nodes, workflowNode)
	}
	for index := range def.NodeGroups {
		def.NodeGroups[index].MemberNodeIDs = groupMemberIDs[def.NodeGroups[index].ID]
	}
	for _, group := range graph.TransitionGroups {
		def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{
			WorkflowID:   workflow.WorkflowID(workflowID),
			ID:           workflow.TransitionGroupID(group.ID),
			SourceNodeID: workflow.NodeID(group.SourceNodeID),
			TransitionID: workflow.TransitionID(group.TransitionID),
			DisplayName:  group.DisplayName,
			Description:  group.Description,
		})
	}
	for _, edge := range graph.Edges {
		def.Edges = append(def.Edges, workflow.Edge{
			WorkflowID:        workflow.WorkflowID(workflowID),
			ID:                workflow.EdgeID(edge.ID),
			Key:               workflow.ModelKey(edge.Key),
			TransitionGroupID: workflow.TransitionGroupID(edge.TransitionGroupID),
			TargetNodeID:      workflow.NodeID(edge.TargetNodeID),
			ContextMode:       workflow.ContextMode(edge.ContextMode),
			ContextSource:     workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(edge.ContextSource.Kind), NodeKey: workflow.ModelKey(edge.ContextSource.NodeKey)}),
			RequiresApproval:  edge.RequiresApproval,
			PromptTemplate:    edge.PromptTemplate,
			Parameters:        domainParameters(edge.Parameters),
		})
	}
	return def, nil
}

func workflowValidationResponse(workflowID workflow.WorkflowID, result workflow.ValidationResult) serverapi.WorkflowValidateResponse {
	resp := serverapi.WorkflowValidateResponse{Valid: !result.HasBlockingErrors()}
	resp.Errors = workflowview.ValidationErrors(string(workflowID), result.Errors)
	return resp
}

func workflowGraphSaveValidationModes() []serverapi.WorkflowValidationMode {
	return []serverapi.WorkflowValidationMode{serverapi.WorkflowValidationModeDraft, serverapi.WorkflowValidationModeExecution}
}

func workflowGraphStoreSaveRequest(workflowID string, expectedVersion int64, metadata *serverapi.WorkflowGraphMetadata, graph serverapi.WorkflowGraphDraft, confirmation *serverapi.WorkflowGraphSaveConfirmation) workflowstore.WorkflowGraphSaveRequest {
	req := workflowstore.WorkflowGraphSaveRequest{WorkflowID: workflow.WorkflowID(workflowID), ExpectedVersion: expectedVersion}
	if metadata != nil {
		req.Metadata = &workflowstore.WorkflowGraphSaveMetadata{Name: metadata.Name, Description: metadata.Description, ExecutionPolicy: workflowExecutionPolicyFromAPI(metadata.ExecutionPolicy)}
	}
	if confirmation != nil {
		req.Confirmed = true
		req.ExpectedRemovedNodeCount = confirmation.ExpectedRemovedNodeCount
		req.ExpectedRemovedTransitionGroupCount = confirmation.ExpectedRemovedTransitionGroupCount
		req.ExpectedRemovedEdgeCount = confirmation.ExpectedRemovedEdgeCount
		req.ExpectedNodeTaskReferenceCount = confirmation.ExpectedNodeTaskReferenceCount
		req.ExpectedEdgeTaskReferenceCount = confirmation.ExpectedEdgeTaskReferenceCount
	}
	for _, group := range graph.NodeGroups {
		req.NodeGroups = append(req.NodeGroups, workflowstore.NodeGroupRecord{ID: group.ID, WorkflowID: workflow.WorkflowID(workflowID), Key: workflow.ModelKey(group.Key), DisplayName: group.DisplayName})
	}
	for _, node := range graph.Nodes {
		req.Nodes = append(req.Nodes, workflowstore.NodeRecord{ID: workflow.NodeID(node.ID), WorkflowID: workflow.WorkflowID(workflowID), Key: workflow.ModelKey(node.Key), Kind: workflow.NodeKind(node.Kind), DisplayName: node.DisplayName, GroupID: node.GroupID, GroupKey: node.GroupKey, SubagentRole: node.SubagentRole, PromptTemplate: node.PromptTemplate, CompletionMode: node.CompletionMode, ScriptPath: optionalStringValue(node.ScriptPath), InputFields: inputFields(node.InputFields), JoinInputProviders: joinInputProviders(node.JoinInputProviders)})
	}
	for _, group := range graph.TransitionGroups {
		req.TransitionGroups = append(req.TransitionGroups, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID(group.ID), WorkflowID: workflow.WorkflowID(workflowID), SourceNodeID: workflow.NodeID(group.SourceNodeID), TransitionID: workflow.TransitionID(group.TransitionID), DisplayName: group.DisplayName, Description: group.Description})
	}
	for _, edge := range graph.Edges {
		req.Edges = append(req.Edges, workflowstore.EdgeRecord{ID: workflow.EdgeID(edge.ID), WorkflowID: workflow.WorkflowID(workflowID), TransitionGroupID: workflow.TransitionGroupID(edge.TransitionGroupID), Key: workflow.ModelKey(edge.Key), TargetNodeID: workflow.NodeID(edge.TargetNodeID), RequiresApproval: edge.RequiresApproval, ContextMode: workflow.ContextMode(edge.ContextMode), ContextSource: workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(edge.ContextSource.Kind), NodeKey: workflow.ModelKey(edge.ContextSource.NodeKey)}), PromptTemplate: edge.PromptTemplate, Parameters: domainParameters(edge.Parameters)})
	}
	return req
}

func workflowGraphSavePreviewResponse(result workflowstore.WorkflowGraphSaveResult, validationResults map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse) serverapi.WorkflowGraphSavePreviewResponse {
	return serverapi.WorkflowGraphSavePreviewResponse{
		CurrentVersion:       result.Version,
		ValidationResults:    validationResults,
		Impact:               workflowGraphSaveImpact(result),
		Blockers:             workflowGraphSaveBlockers(result.Blockers),
		CanSave:              result.CanSave,
		ConfirmationRequired: result.ConfirmationRequired,
	}
}

func workflowGraphSaveResponse(result workflowstore.WorkflowGraphSaveResult, validationResults map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse) serverapi.WorkflowGraphSaveResponse {
	return serverapi.WorkflowGraphSaveResponse{
		Saved:                result.Saved,
		CurrentVersion:       result.Version,
		ValidationResults:    validationResults,
		Impact:               workflowGraphSaveImpact(result),
		Blockers:             workflowGraphSaveBlockers(result.Blockers),
		CanSave:              result.CanSave,
		ConfirmationRequired: result.ConfirmationRequired,
	}
}

func workflowGraphSaveImpact(result workflowstore.WorkflowGraphSaveResult) serverapi.WorkflowGraphSaveImpact {
	return serverapi.WorkflowGraphSaveImpact{
		RemovedNodeCount:                  result.Impact.RemovedNodeCount,
		RemovedTransitionGroupCount:       result.Impact.RemovedTransitionGroupCount,
		RemovedEdgeCount:                  result.Impact.RemovedEdgeCount,
		NodeTaskReferenceCount:            result.Impact.NodeTaskReferenceCount,
		EdgeTaskReferenceCount:            result.Impact.EdgeTaskReferenceCount,
		ActiveNodePlacementCount:          result.EditPolicyImpact.ActiveNodePlacementCount,
		PendingApprovalCount:              result.EditPolicyImpact.PendingApprovalCount,
		ActiveRunCount:                    result.EditPolicyImpact.ActiveRunCount,
		RunnableRunCount:                  result.EditPolicyImpact.RunnableRunCount,
		StartNodeChangeCount:              result.EditPolicyImpact.StartNodeChangeCount,
		LastTerminalChangeCount:           result.EditPolicyImpact.LastTerminalChangeCount,
		TaskReferencedNodeKindChangeCount: result.EditPolicyImpact.TaskReferencedNodeKindChangeCount,
	}
}

func workflowGraphSaveBlockers(blockers []workflowstore.WorkflowGraphSaveBlocker) []serverapi.WorkflowGraphSaveBlocker {
	out := make([]serverapi.WorkflowGraphSaveBlocker, 0, len(blockers))
	for _, blocker := range blockers {
		out = append(out, serverapi.WorkflowGraphSaveBlocker{Code: blocker.Code, Message: blocker.Message, Count: blocker.Count})
	}
	return out
}

func commentRecord(row workflowstore.CommentRecord) serverapi.WorkflowTaskComment {
	return serverapi.WorkflowTaskComment{ID: row.ID, TaskID: string(row.TaskID), Body: row.Body, Author: row.Author, AuthorID: row.AuthorID, CreatedAtUnixMs: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func inputFields(in []serverapi.WorkflowInputField) []workflow.InputField {
	out := make([]workflow.InputField, 0, len(in))
	for _, field := range in {
		out = append(out, workflow.InputField{Name: field.Name, Description: field.Description})
	}
	return out
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func joinInputProviders(in []serverapi.WorkflowJoinInputProvider) []workflow.JoinInputProvider {
	out := make([]workflow.JoinInputProvider, 0, len(in))
	for _, provider := range in {
		out = append(out, workflow.JoinInputProvider{InputName: provider.InputName, ProviderEdgeID: workflow.EdgeID(provider.ProviderEdgeID)})
	}
	return out
}

func domainParameters(in []serverapi.WorkflowParameter) []workflow.Parameter {
	out := make([]workflow.Parameter, 0, len(in))
	for _, parameter := range in {
		out = append(out, workflow.Parameter{Key: parameter.Key, Description: parameter.Description})
	}
	return out
}

func placementIDs(in []workflow.PlacementID) []string {
	out := make([]string, 0, len(in))
	for _, id := range in {
		out = append(out, string(id))
	}
	return out
}

func runIDs(in []workflow.RunID) []string {
	out := make([]string, 0, len(in))
	for _, id := range in {
		out = append(out, string(id))
	}
	return out
}
