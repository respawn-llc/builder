package workflowexecution

import (
	"fmt"
	"testing"

	"core/server/workflow"
)

func TestCurrentNodeControllerLargeMixedKindQueueSelection(t *testing.T) {
	queue := currentNodeAutomaticQueue{}
	for index := 0; index < 4096; index++ {
		policy := currentNodeAdmissionAutomaticScript
		if index%2 == 0 {
			policy = currentNodeAdmissionAutomaticAgent
		}
		queue.append(currentNodeQueuedStart{
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
		if !agentAvailable && entry.start.policy != currentNodeAdmissionAutomaticScript {
			t.Fatalf("selected queue entry = %+v, want an admissible Script", entry)
		}
		queue.remove(entry)
	}
}

func TestCurrentNodeAutomaticQueueSelectionPreservesTaskLocalityAndFIFO(t *testing.T) {
	queue := currentNodeAutomaticQueue{}
	taskOne := workflow.TaskID("task-automatic-queue-one")
	taskTwo := workflow.TaskID("task-automatic-queue-two")
	entries := []currentNodeQueuedStart{
		{reference: workflow.CurrentNodeReference{TaskID: taskOne, NodeID: "agent-one"}, policy: currentNodeAdmissionAutomaticAgent},
		{reference: workflow.CurrentNodeReference{TaskID: taskTwo, NodeID: "script-two"}, policy: currentNodeAdmissionAutomaticScript},
		{reference: workflow.CurrentNodeReference{TaskID: taskOne, NodeID: "script-one"}, policy: currentNodeAdmissionAutomaticScript},
		{reference: workflow.CurrentNodeReference{TaskID: taskTwo, NodeID: "agent-two"}, policy: currentNodeAdmissionAutomaticAgent},
	}
	for _, entry := range entries {
		queue.append(entry)
	}

	lastTask := taskOne
	entry, ok := queue.selectEntry(&lastTask, true)
	if !ok || !entry.start.reference.Equal(entries[0].reference) {
		t.Fatalf("same-Task Agent selection = %+v, want first Agent", entry)
	}
	queue.remove(entry)

	lastTask = taskTwo
	entry, ok = queue.selectEntry(&lastTask, true)
	if !ok || !entry.start.reference.Equal(entries[1].reference) {
		t.Fatalf("same-Task FIFO selection = %+v, want first Script", entry)
	}
	queue.remove(entry)

	entry, ok = queue.selectEntry(nil, false)
	if !ok || !entry.start.reference.Equal(entries[2].reference) {
		t.Fatalf("Script selection at saturated Agent capacity = %+v, want Script", entry)
	}
}

func TestCurrentNodeAutomaticQueueRemovalMaintainsEveryIndex(t *testing.T) {
	queue := currentNodeAutomaticQueue{}
	entries := []currentNodeQueuedStart{
		{reference: workflow.CurrentNodeReference{TaskID: "task-a", NodeID: "agent-a"}, policy: currentNodeAdmissionAutomaticAgent},
		{reference: workflow.CurrentNodeReference{TaskID: "task-a", NodeID: "script-a"}, policy: currentNodeAdmissionAutomaticScript},
		{reference: workflow.CurrentNodeReference{TaskID: "task-b", NodeID: "agent-b"}, policy: currentNodeAdmissionAutomaticAgent},
		{reference: workflow.CurrentNodeReference{TaskID: "task-b", NodeID: "script-b"}, policy: currentNodeAdmissionAutomaticScript},
	}
	for _, entry := range entries {
		queue.append(entry)
	}
	for queue.len() != 0 {
		entry, ok := queue.selectEntry(nil, true)
		if !ok {
			t.Fatal("queue lost an eligible entry")
		}
		queue.remove(entry)
	}
	if len(queue.tasks) != 0 || queue.first != nil || queue.last != nil {
		t.Fatalf("queue indexes after drain = %+v, want empty", queue)
	}
}
