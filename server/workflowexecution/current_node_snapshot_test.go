package workflowexecution

import (
	"core/server/workflow"
	"core/shared/runtimeids"
)

type currentNodeAdmissionGateSnapshot struct {
	CurrentNode workflow.CurrentNodeReference
	ScopeID     runtimeids.ExecutionScopeID
	Automatic   bool
}

type currentNodeLiveScopeSnapshot struct {
	CurrentNode workflow.CurrentNodeReference
	ScopeID     runtimeids.ExecutionScopeID
	Automatic   bool
}

type currentNodeHeldIntentSnapshot struct {
	CurrentNode workflow.CurrentNodeReference
	SourceScope runtimeids.ExecutionScopeID
	Automatic   bool
}

type currentNodeExplicitStartSnapshot struct {
	CurrentNode workflow.CurrentNodeReference
}

type currentNodeExecutionSnapshot struct {
	AutomaticIntents  []CurrentNodeAutomaticIntent
	ExplicitStarts    []currentNodeExplicitStartSnapshot
	HeldIntents       []currentNodeHeldIntentSnapshot
	Gates             []currentNodeAdmissionGateSnapshot
	LiveScopes        []currentNodeLiveScopeSnapshot
	InterruptingTasks []workflow.TaskID
}

func currentNodeControllerSnapshotForTest(c *CurrentNodeController) currentNodeExecutionSnapshot {
	if c == nil {
		return currentNodeExecutionSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := currentNodeExecutionSnapshot{
		AutomaticIntents: make([]CurrentNodeAutomaticIntent, 0, c.automaticQueue.len()+len(c.automaticReservations)),
		ExplicitStarts:   make([]currentNodeExplicitStartSnapshot, 0, len(c.explicitQueue)+len(c.explicitReservations)),
		Gates:            make([]currentNodeAdmissionGateSnapshot, 0, len(c.gates)),
		LiveScopes:       make([]currentNodeLiveScopeSnapshot, 0, len(c.live)),
	}
	for entry := c.automaticQueue.first; entry != nil; entry = entry.globalNext {
		start := entry.start
		snapshot.AutomaticIntents = append(snapshot.AutomaticIntents, CurrentNodeAutomaticIntent{
			CurrentNode: start.reference,
			NodeKind:    start.policy.nodeKind(),
		})
	}
	for _, start := range c.automaticReservations {
		snapshot.AutomaticIntents = append(snapshot.AutomaticIntents, CurrentNodeAutomaticIntent{
			CurrentNode: start.reference,
			NodeKind:    start.policy.nodeKind(),
		})
	}
	for _, start := range c.explicitQueue {
		snapshot.ExplicitStarts = append(
			snapshot.ExplicitStarts,
			currentNodeExplicitStartSnapshot{CurrentNode: start.reference},
		)
	}
	for _, start := range c.explicitReservations {
		snapshot.ExplicitStarts = append(
			snapshot.ExplicitStarts,
			currentNodeExplicitStartSnapshot{CurrentNode: start.reference},
		)
	}
	for _, gate := range c.gates {
		snapshot.Gates = append(snapshot.Gates, currentNodeAdmissionGateSnapshot{
			CurrentNode: gate.reference,
			ScopeID:     gate.lease.ScopeID(),
			Automatic:   gate.policy.isAutomatic(),
		})
	}
	for scopeID, live := range c.live {
		snapshot.LiveScopes = append(snapshot.LiveScopes, currentNodeLiveScopeSnapshot{
			CurrentNode: live.reference,
			ScopeID:     scopeID,
			Automatic:   live.policy.isAutomatic(),
		})
	}
	for sourceScope, starts := range c.heldStarts {
		for _, start := range starts {
			snapshot.HeldIntents = append(snapshot.HeldIntents, currentNodeHeldIntentSnapshot{
				CurrentNode: start.reference,
				SourceScope: sourceScope,
				Automatic:   start.policy.isAutomatic(),
			})
		}
	}
	snapshot.InterruptingTasks = c.interrupts.taskIDs()
	return snapshot
}
