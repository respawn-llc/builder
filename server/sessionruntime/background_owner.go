package sessionruntime

import (
	"context"
	"fmt"

	"core/server/runtime"
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

func (a *Authority) replayOrdinaryBackground(sessionID runtimeids.SessionID) {
	if a.options.background == nil {
		return
	}
	a.mu.Lock()
	processIDs := make([]string, 0)
	for processID, owner := range a.backgroundOwners {
		if _, workflow := owner.(workflowBackgroundOwner); !workflow && owner.backgroundOwnerSessionID() == sessionID {
			processIDs = append(processIDs, processID)
		}
	}
	a.mu.Unlock()
	for _, processID := range processIDs {
		a.options.background.ReplayPendingTerminal(processID)
	}
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
	processIDs := make([]string, 0)
	for processID, owner := range a.backgroundOwners {
		workflowOwner, ok := owner.(workflowBackgroundOwner)
		if !ok || !sameWorkflowExecution(workflowOwner.workflow, workflow) {
			continue
		}
		if _, admitted := workflowOwner.delivery.(pendingWorkflowDeliveryTarget); admitted {
			workflowOwner.delivery = admittedWorkflowDeliveryTarget{scopeID: scope.ID()}
			a.backgroundOwners[processID] = workflowOwner
			processIDs = append(processIDs, processID)
		}
	}
	a.mu.Unlock()
	for _, processID := range processIDs {
		a.options.background.ReplayPendingTerminal(processID)
	}
}

func (a *Authority) withdrawWorkflowBackground(scope ExecutionScope, engine *runtime.Engine) {
	if a.options.background == nil || engine == nil {
		return
	}
	a.mu.Lock()
	owners := make([]backgroundOwner, 0)
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
		owners = append(owners, workflowOwner)
	}
	a.mu.Unlock()
	for _, owner := range owners {
		engine.DiscardProvisionalBackgroundNotice(owner.backgroundOwnerProcessID())
		a.options.background.WithdrawTerminalHandoff(owner.backgroundOwnerProcessID(), owner.backgroundOwnerActivityID())
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
