package workflowexecution

import (
	"core/server/workflow"
	"core/shared/runtimeids"
)

type CurrentNodeAdmissionGateSnapshot struct {
	CurrentNode workflow.CurrentNodeReference
	ScopeID     runtimeids.ExecutionScopeID
	Automatic   bool
}

type CurrentNodeLiveScopeSnapshot struct {
	CurrentNode workflow.CurrentNodeReference
	ScopeID     runtimeids.ExecutionScopeID
	Automatic   bool
}

type CurrentNodeHeldIntentSnapshot struct {
	CurrentNode workflow.CurrentNodeReference
	SourceScope runtimeids.ExecutionScopeID
	Automatic   bool
}

// CurrentNodeExecutionSnapshot is immutable live controller state. Durable
// Current Node scheduling rows are intentionally not inferred from this view.
type CurrentNodeExecutionSnapshot struct {
	AutomaticIntents  []CurrentNodeAutomaticIntent
	ExplicitStarts    []CurrentNodeExplicitStart
	HeldIntents       []CurrentNodeHeldIntentSnapshot
	Gates             []CurrentNodeAdmissionGateSnapshot
	LiveScopes        []CurrentNodeLiveScopeSnapshot
	InterruptingTasks []workflow.TaskID
}

func (c *CurrentNodeController) Snapshot() CurrentNodeExecutionSnapshot {
	if c == nil {
		return CurrentNodeExecutionSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := CurrentNodeExecutionSnapshot{
		AutomaticIntents: make([]CurrentNodeAutomaticIntent, 0, c.automaticQueue.len()+len(c.automaticReservations)),
		ExplicitStarts:   make([]CurrentNodeExplicitStart, 0, len(c.explicitQueue)+len(c.explicitReservations)),
		Gates:            make([]CurrentNodeAdmissionGateSnapshot, 0, len(c.gates)),
		LiveScopes:       make([]CurrentNodeLiveScopeSnapshot, 0, len(c.live)),
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
			CurrentNodeExplicitStart{CurrentNode: start.reference},
		)
	}
	for _, start := range c.explicitReservations {
		snapshot.ExplicitStarts = append(
			snapshot.ExplicitStarts,
			CurrentNodeExplicitStart{CurrentNode: start.reference},
		)
	}
	for _, gate := range c.gates {
		snapshot.Gates = append(snapshot.Gates, CurrentNodeAdmissionGateSnapshot{
			CurrentNode: gate.reference,
			ScopeID:     gate.lease.ScopeID(),
			Automatic:   gate.policy.isAutomatic(),
		})
	}
	for scopeID, live := range c.live {
		snapshot.LiveScopes = append(snapshot.LiveScopes, CurrentNodeLiveScopeSnapshot{
			CurrentNode: live.reference,
			ScopeID:     scopeID,
			Automatic:   live.policy.isAutomatic(),
		})
	}
	for sourceScope, starts := range c.heldStarts {
		for _, start := range starts {
			snapshot.HeldIntents = append(snapshot.HeldIntents, CurrentNodeHeldIntentSnapshot{
				CurrentNode: start.reference,
				SourceScope: sourceScope,
				Automatic:   start.policy.isAutomatic(),
			})
		}
	}
	snapshot.InterruptingTasks = c.interrupts.taskIDs()
	return snapshot
}
