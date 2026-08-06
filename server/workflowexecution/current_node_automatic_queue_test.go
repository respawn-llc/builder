package workflowexecution

import (
	"fmt"
	"testing"

	"core/server/workflow"
)

func TestCurrentNodeControllerLargeMixedKindQueueSelection(t *testing.T) {
	queue := currentNodeAutomaticQueue{}
	registry := newCurrentNodeRunRegistry()
	for index := 0; index < 4096; index++ {
		policy := currentNodeAdmissionAutomaticScript
		if index%2 == 0 {
			policy = currentNodeAdmissionAutomaticAgent
		}
		appendAutomaticQueueRun(t, &queue, &registry, currentNodeQueuedStart{
			reference: workflow.CurrentNodeReference{
				TaskID: workflow.TaskID("task-large-mixed-kind-queue"),
				NodeID: workflow.NodeID(fmt.Sprintf("node-%d", index)),
			},
			policy: policy,
		})
	}
	agentAvailable := false
	for queue.len() != 0 {
		entry, ok := queue.selectEntry(nil, agentAvailable)
		if !ok {
			if agentAvailable {
				t.Fatalf("queue lost an eligible entry with %d remaining", queue.len())
			}
			agentAvailable = true
			continue
		}
		run := automaticQueueRun(t, &registry, entry)
		if !agentAvailable && run.policy != currentNodeAdmissionAutomaticScript {
			t.Fatalf("selected queue entry = %+v, want an admissible Script", entry)
		}
		queue.remove(entry, run)
	}
}

func TestCurrentNodeAutomaticQueueSelectionPreservesTaskLocalityAndFIFO(t *testing.T) {
	queue := currentNodeAutomaticQueue{}
	registry := newCurrentNodeRunRegistry()
	taskOne := workflow.TaskID("task-automatic-queue-one")
	taskTwo := workflow.TaskID("task-automatic-queue-two")
	entries := []currentNodeQueuedStart{
		{reference: workflow.CurrentNodeReference{TaskID: taskOne, NodeID: "agent-one"}, policy: currentNodeAdmissionAutomaticAgent},
		{reference: workflow.CurrentNodeReference{TaskID: taskTwo, NodeID: "script-two"}, policy: currentNodeAdmissionAutomaticScript},
		{reference: workflow.CurrentNodeReference{TaskID: taskOne, NodeID: "script-one"}, policy: currentNodeAdmissionAutomaticScript},
		{reference: workflow.CurrentNodeReference{TaskID: taskTwo, NodeID: "agent-two"}, policy: currentNodeAdmissionAutomaticAgent},
	}
	for _, entry := range entries {
		appendAutomaticQueueRun(t, &queue, &registry, entry)
	}

	lastTask := taskOne
	entry, ok := queue.selectEntry(&lastTask, true)
	if !ok || !automaticQueueRun(t, &registry, entry).reference.Equal(entries[0].reference) {
		t.Fatalf("same-Task Agent selection = %+v, want first Agent", entry)
	}
	queue.remove(entry, automaticQueueRun(t, &registry, entry))

	lastTask = taskTwo
	entry, ok = queue.selectEntry(&lastTask, true)
	if !ok || !automaticQueueRun(t, &registry, entry).reference.Equal(entries[1].reference) {
		t.Fatalf("same-Task FIFO selection = %+v, want first Script", entry)
	}
	queue.remove(entry, automaticQueueRun(t, &registry, entry))

	entry, ok = queue.selectEntry(nil, false)
	if !ok || !automaticQueueRun(t, &registry, entry).reference.Equal(entries[2].reference) {
		t.Fatalf("Script selection at saturated Agent capacity = %+v, want Script", entry)
	}
}

func TestCurrentNodeAutomaticQueueRemovalMaintainsEveryIndex(t *testing.T) {
	queue := currentNodeAutomaticQueue{}
	registry := newCurrentNodeRunRegistry()
	entries := []currentNodeQueuedStart{
		{reference: workflow.CurrentNodeReference{TaskID: "task-a", NodeID: "agent-a"}, policy: currentNodeAdmissionAutomaticAgent},
		{reference: workflow.CurrentNodeReference{TaskID: "task-a", NodeID: "script-a"}, policy: currentNodeAdmissionAutomaticScript},
		{reference: workflow.CurrentNodeReference{TaskID: "task-b", NodeID: "agent-b"}, policy: currentNodeAdmissionAutomaticAgent},
		{reference: workflow.CurrentNodeReference{TaskID: "task-b", NodeID: "script-b"}, policy: currentNodeAdmissionAutomaticScript},
	}
	for _, entry := range entries {
		appendAutomaticQueueRun(t, &queue, &registry, entry)
	}
	for queue.len() != 0 {
		entry, ok := queue.selectEntry(nil, true)
		if !ok {
			t.Fatal("queue lost an eligible entry")
		}
		queue.remove(entry, automaticQueueRun(t, &registry, entry))
	}
	if len(queue.tasks) != 0 || queue.first != nil || queue.last != nil {
		t.Fatalf("queue indexes after drain = %+v, want empty", queue)
	}
}

func appendAutomaticQueueRun(
	t *testing.T,
	queue *currentNodeAutomaticQueue,
	registry *currentNodeRunRegistry,
	candidate currentNodeQueuedStart,
) {
	t.Helper()
	if candidate.nodeKind == "" {
		candidate.nodeKind = candidate.policy.nodeKind()
	}
	run, _, err := registry.register(&candidate)
	if err != nil {
		t.Fatalf("register automatic queue Run: %v", err)
	}
	queue.append(mustCurrentNodeRunKey(run), run)
}

func automaticQueueRun(
	t *testing.T,
	registry *currentNodeRunRegistry,
	entry *currentNodeAutomaticQueueEntry,
) *currentNodeRun {
	t.Helper()
	run, exists := registry.get(entry.key)
	if !exists {
		t.Fatal("automatic queue entry lost its Run")
	}
	return run
}
