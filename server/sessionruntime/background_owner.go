package sessionruntime

import (
	"context"
	"fmt"

	"core/server/runtime"
	"core/server/session"
	shelltool "core/server/tools/shell"
	"core/shared/runtimeids"

	"github.com/google/uuid"
)

type backgroundOwner interface {
	backgroundOwnerProcessID() string
	backgroundOwnerActivityID() uuid.UUID
	backgroundOwnerSessionID() runtimeids.SessionID
}

type ordinaryBackgroundOwner struct {
	processID      string
	activityID     uuid.UUID
	sessionID      runtimeids.SessionID
	launchResource runtimeids.ResourceGeneration
	launchScopeID  runtimeids.ExecutionScopeID
}

func (o ordinaryBackgroundOwner) backgroundOwnerProcessID() string               { return o.processID }
func (o ordinaryBackgroundOwner) backgroundOwnerActivityID() uuid.UUID           { return o.activityID }
func (o ordinaryBackgroundOwner) backgroundOwnerSessionID() runtimeids.SessionID { return o.sessionID }

type workflowDeliveryTarget interface {
	workflowDeliveryTarget()
}

type pendingWorkflowDeliveryTarget struct{}

func (pendingWorkflowDeliveryTarget) workflowDeliveryTarget() {}

type admittedWorkflowDeliveryTarget struct {
	scopeID runtimeids.ExecutionScopeID
}

func (admittedWorkflowDeliveryTarget) workflowDeliveryTarget() {}

type workflowBackgroundOwner struct {
	processID      string
	activityID     uuid.UUID
	sessionID      runtimeids.SessionID
	launchResource runtimeids.ResourceGeneration
	launchScopeID  runtimeids.ExecutionScopeID
	workflow       WorkflowExecutionRef
	delivery       workflowDeliveryTarget
	diagnostic     *runtime.PendingBackgroundDeliveryDiagnostic
}

func (o workflowBackgroundOwner) backgroundOwnerProcessID() string               { return o.processID }
func (o workflowBackgroundOwner) backgroundOwnerActivityID() uuid.UUID           { return o.activityID }
func (o workflowBackgroundOwner) backgroundOwnerSessionID() runtimeids.SessionID { return o.sessionID }

func (a *Authority) rememberBackgroundOwner(event shelltool.Event, sessionID runtimeids.SessionID, scope ExecutionScope) {
	correlation := event.Snapshot.ExecutionCorrelation
	if correlation == nil {
		panic(fmt.Sprintf("background owner requires execution correlation: process_id=%q", event.Snapshot.ID))
	}
	var owner backgroundOwner
	workflow, hasWorkflow := scope.Workflow()
	if hasWorkflow {
		owner = workflowBackgroundOwner{
			processID:      event.Snapshot.ID,
			activityID:     event.Snapshot.ActivityID,
			sessionID:      sessionID,
			launchResource: correlation.ResourceGeneration(),
			launchScopeID:  scope.ID(),
			workflow:       workflow,
			delivery:       admittedWorkflowDeliveryTarget{scopeID: scope.ID()},
		}
	} else {
		owner = ordinaryBackgroundOwner{
			processID:      event.Snapshot.ID,
			activityID:     event.Snapshot.ActivityID,
			sessionID:      sessionID,
			launchResource: correlation.ResourceGeneration(),
			launchScopeID:  scope.ID(),
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if existing := a.backgroundOwners[event.Snapshot.ID]; existing != nil {
		panic(fmt.Sprintf(
			"background owner already exists: process_id=%q activity_id=%s",
			event.Snapshot.ID,
			event.Snapshot.ActivityID,
		))
	}
	a.backgroundOwners[event.Snapshot.ID] = owner
}

func (a *Authority) backgroundOwner(processID string, activityID uuid.UUID) backgroundOwner {
	a.mu.Lock()
	defer a.mu.Unlock()
	owner := a.backgroundOwners[processID]
	if owner == nil || owner.backgroundOwnerActivityID() != activityID {
		return nil
	}
	return owner
}

func (a *Authority) hasOrdinaryBackgroundOwner(sessionID runtimeids.SessionID) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, owner := range a.backgroundOwners {
		if _, workflow := owner.(workflowBackgroundOwner); !workflow && owner.backgroundOwnerSessionID() == sessionID {
			return true
		}
	}
	return false
}

func (a *Authority) settleBackgroundOwner(processID string) {
	a.mu.Lock()
	owner := a.backgroundOwners[processID]
	if owner != nil {
		delete(a.backgroundOwners, processID)
	}
	a.mu.Unlock()
	if owner == nil {
		return
	}
	if _, workflow := owner.(workflowBackgroundOwner); workflow {
		return
	}
	a.mu.Lock()
	resource := a.resources[owner.backgroundOwnerSessionID()]
	a.mu.Unlock()
	if resource != nil {
		_ = a.closeRetiringResource(context.Background(), resource)
	}
}

func (a *Authority) replayOrdinaryBackground(ctx context.Context, sessionID runtimeids.SessionID) error {
	if a.options.background == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.Lock()
	resource := a.resources[sessionID]
	processIDs := make([]string, 0)
	for processID, owner := range a.backgroundOwners {
		if _, workflow := owner.(workflowBackgroundOwner); !workflow && owner.backgroundOwnerSessionID() == sessionID {
			processIDs = append(processIDs, processID)
		}
	}
	a.mu.Unlock()
	for _, processID := range processIDs {
		a.mu.Lock()
		owner, ok := a.backgroundOwners[processID].(ordinaryBackgroundOwner)
		a.mu.Unlock()
		if !ok {
			continue
		}
		if resource == nil {
			return fmt.Errorf("ordinary background replay has no resource: session_id=%s", sessionID)
		}
		if diagnostic, hasDiagnostic := a.options.background.TakeTerminalDeliveryDiagnostic(processID, owner.activityID); hasDiagnostic {
			var receipt session.CommitReceipt
			err := resource.withEngine(ctx, resource.ref, func(_ context.Context, engine *runtime.Engine) error {
				var commitErr error
				runtimeDiagnostic := runtime.NewBackgroundRoutingDiagnosticDetail(
					diagnostic.ProcessID,
					diagnostic.Activity,
					diagnostic.Attempt,
					diagnostic.Detail,
				)
				receipt, commitErr = engine.CommitPendingBackgroundDeliveryDiagnostic(runtimeDiagnostic)
				return commitErr
			})
			if !receipt.Committed {
				a.options.background.RestoreTerminalDeliveryDiagnostic(diagnostic)
				a.backgroundLogger.Error(
					"ordinary background diagnostic replay failed",
					"process_id", processID,
					"session_id", sessionID.String(),
					"error", err,
				)
				return err
			}
			if err != nil {
				a.backgroundLogger.Error(
					"ordinary background diagnostic committed with observer error",
					"process_id", processID,
					"session_id", sessionID.String(),
					"error", err,
				)
			}
		}
		a.options.background.ReplayPendingTerminal(processID)
	}
	return nil
}

func (a *Authority) replayWorkflowBackground(scope ExecutionScope) {
	if a.options.background == nil {
		return
	}
	workflow, hasWorkflow := scope.Workflow()
	if !hasWorkflow {
		panic(fmt.Sprintf("workflow background replay requires workflow scope: scope_id=%s", scope.ID()))
	}
	a.mu.Lock()
	execution := a.byScope[scope.ID()]
	if execution == nil || execution.resource == nil {
		a.mu.Unlock()
		return
	}
	resource := execution.resource
	processIDs := make([]string, 0)
	for processID, owner := range a.backgroundOwners {
		workflowOwner, ok := owner.(workflowBackgroundOwner)
		if !ok || !sameWorkflowExecution(workflowOwner.workflow, workflow) {
			continue
		}
		if _, pending := workflowOwner.delivery.(pendingWorkflowDeliveryTarget); pending {
			processIDs = append(processIDs, processID)
		}
	}
	a.mu.Unlock()
	for _, processID := range processIDs {
		var diagnostic *runtime.PendingBackgroundDeliveryDiagnostic
		a.mu.Lock()
		owner, ok := a.backgroundOwners[processID].(workflowBackgroundOwner)
		if ok {
			diagnostic = owner.diagnostic
		}
		a.mu.Unlock()
		if diagnostic != nil {
			var receipt session.CommitReceipt
			err := resource.withEngine(context.Background(), resource.ref, func(_ context.Context, engine *runtime.Engine) error {
				var commitErr error
				receipt, commitErr = engine.CommitPendingBackgroundDeliveryDiagnostic(*diagnostic)
				return commitErr
			})
			if !receipt.Committed {
				a.backgroundLogger.Error(
					"resume background diagnostic failed",
					"process_id", processID,
					"scope_id", scope.ID().String(),
					"error", err,
				)
				continue
			}
			if err != nil {
				a.backgroundLogger.Error(
					"resume background diagnostic committed with observer error",
					"process_id", processID,
					"scope_id", scope.ID().String(),
					"error", err,
				)
			}
			a.mu.Lock()
			current, currentOK := a.backgroundOwners[processID].(workflowBackgroundOwner)
			if currentOK && current.activityID == owner.activityID {
				current.diagnostic = nil
				a.backgroundOwners[processID] = current
			}
			a.mu.Unlock()
		}
		a.mu.Lock()
		owner, ok = a.backgroundOwners[processID].(workflowBackgroundOwner)
		if !ok || owner.diagnostic != nil {
			a.mu.Unlock()
			continue
		}
		owner.delivery = admittedWorkflowDeliveryTarget{scopeID: scope.ID()}
		a.backgroundOwners[processID] = owner
		a.mu.Unlock()
		a.options.background.ReplayPendingTerminal(processID)
	}
}

func (a *Authority) withdrawWorkflowBackground(scope ExecutionScope, engine *runtime.Engine) {
	if a.options.background == nil || engine == nil {
		return
	}
	type withdrawingOwner struct {
		processID string
		activity  uuid.UUID
	}
	a.mu.Lock()
	owners := make([]withdrawingOwner, 0)
	for processID, owner := range a.backgroundOwners {
		workflowOwner, ok := owner.(workflowBackgroundOwner)
		if !ok {
			continue
		}
		delivery, admitted := workflowOwner.delivery.(admittedWorkflowDeliveryTarget)
		if !admitted || delivery.scopeID != scope.ID() {
			continue
		}
		workflowOwner.delivery = pendingWorkflowDeliveryTarget{}
		a.backgroundOwners[processID] = workflowOwner
		owners = append(owners, withdrawingOwner{
			processID: workflowOwner.processID,
			activity:  workflowOwner.activityID,
		})
	}
	a.mu.Unlock()
	for _, owner := range owners {
		withdrawal, found, withdrawalErr := engine.WithdrawBackgroundDelivery(
			context.Background(),
			owner.processID,
			owner.activity,
		)
		if withdrawalErr != nil {
			a.backgroundLogger.Error(
				"withdraw background delivery failed",
				"process_id", owner.processID,
				"activity_id", owner.activity.String(),
				"scope_id", scope.ID().String(),
				"error", withdrawalErr,
			)
			continue
		}
		if found && withdrawal.Diagnostic != nil {
			a.mu.Lock()
			current, ok := a.backgroundOwners[owner.processID].(workflowBackgroundOwner)
			if ok && current.activityID == owner.activity {
				current.diagnostic = withdrawal.Diagnostic
				a.backgroundOwners[owner.processID] = current
			}
			a.mu.Unlock()
		}
		if !found || withdrawal.CompletionPending {
			a.options.background.WithdrawTerminalHandoff(owner.processID, owner.activity)
		}
	}
}

func sameWorkflowExecution(left WorkflowExecutionRef, right WorkflowExecutionRef) bool {
	return left.ProjectID == right.ProjectID &&
		left.WorkflowID == right.WorkflowID &&
		left.CurrentNode.Equal(right.CurrentNode)
}

func workflowDeliveryScope(owner backgroundOwner) (runtimeids.ExecutionScopeID, bool) {
	workflowOwner, ok := owner.(workflowBackgroundOwner)
	if !ok {
		return runtimeids.ExecutionScopeID{}, false
	}
	delivery, ok := workflowOwner.delivery.(admittedWorkflowDeliveryTarget)
	if !ok {
		return runtimeids.ExecutionScopeID{}, false
	}
	return delivery.scopeID, true
}
