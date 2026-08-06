package runtime

import (
	"fmt"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/config"
)

func TestEnsurePersistedWorkflowAssignmentUsesStructuredActiveIdentity(t *testing.T) {
	t.Parallel()
	current := workflowAssignmentForEnsureTest(t, "task-current", "node-current")
	previous := workflowAssignmentForEnsureTest(t, "task-previous", "node-previous")

	for _, test := range []struct {
		name         string
		existing     *WorkflowAssignment
		wantAppended int
		wantIdentity string
	}{
		{name: "identity absent", wantAppended: 1, wantIdentity: current.Prompt.Identity},
		{name: "identity matching", existing: &current, wantAppended: 0, wantIdentity: current.Prompt.Identity},
		{name: "identity previous", existing: &previous, wantAppended: 1, wantIdentity: current.Prompt.Identity},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			if test.existing != nil {
				mustAppendTestEvent(t, store, "existing", mustWorkflowAssignmentMessageForEnsureTest(t, *test.existing))
			}
			before := workflowAssignmentCountForEnsureTest(t, store)
			ensure, err := EnsurePersistedWorkflowAssignment(store, current, persistedWorkflowAssignmentContextForEnsureTest())
			if err != nil {
				t.Fatalf("EnsurePersistedWorkflowAssignment: %v", err)
			}
			receipt, err := ensure.Wait(t.Context())
			if err != nil || !receipt.Committed {
				t.Fatalf("assignment ensure receipt = %+v, error = %v", receipt, err)
			}
			after := workflowAssignmentCountForEnsureTest(t, store)
			if got := after - before; got != test.wantAppended {
				t.Fatalf("appended workflow assignments = %d, want %d", got, test.wantAppended)
			}

			reopened := mustOpenTestSession(t, store.Dir())
			engine := mustNewTestEngine(t, reopened, &fakeClient{}, mustWorkflowEnsureRegistry(t), Config{Model: "gpt-5"})
			if got, present := activeWorkflowAssignmentIdentity(engine.transcriptRuntimeState().SnapshotItems()); !present || got != test.wantIdentity {
				t.Fatalf("active workflow identity = %q, present=%t, want %q", got, present, test.wantIdentity)
			}
		})
	}
}

func TestEnsurePersistedWorkflowAssignmentReusesCommittedAppendAfterRestartBeforeExecution(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	assignment := workflowAssignmentForEnsureTest(t, "task-restart", "node-review")

	first, err := EnsurePersistedWorkflowAssignment(store, assignment, persistedWorkflowAssignmentContextForEnsureTest())
	if err != nil {
		t.Fatalf("first EnsurePersistedWorkflowAssignment: %v", err)
	}
	if receipt, waitErr := first.Wait(t.Context()); waitErr != nil || !receipt.Committed {
		t.Fatalf("first ensure receipt = %+v, error = %v", receipt, waitErr)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	before := workflowAssignmentCountForEnsureTest(t, reopened)
	second, err := EnsurePersistedWorkflowAssignment(reopened, assignment, persistedWorkflowAssignmentContextForEnsureTest())
	if err != nil {
		t.Fatalf("second EnsurePersistedWorkflowAssignment: %v", err)
	}
	if receipt, waitErr := second.Wait(t.Context()); waitErr != nil || !receipt.Committed {
		t.Fatalf("second ensure receipt = %+v, error = %v", receipt, waitErr)
	}
	if after := workflowAssignmentCountForEnsureTest(t, reopened); after != before {
		t.Fatalf("workflow assignments after restart ensure = %d, want %d", after, before)
	}
}

func TestEnsureLiveWorkflowAssignmentUsesStructuredActiveIdentity(t *testing.T) {
	t.Parallel()
	current := workflowAssignmentForEnsureTest(t, "task-live-current", "node-current")
	previous := workflowAssignmentForEnsureTest(t, "task-live-previous", "node-previous")

	for _, test := range []struct {
		name         string
		existing     *WorkflowAssignment
		wantAppended int
	}{
		{name: "identity absent", wantAppended: 1},
		{name: "identity matching", existing: &current, wantAppended: 0},
		{name: "identity previous", existing: &previous, wantAppended: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			if test.existing != nil {
				mustAppendTestEvent(t, store, "existing", mustWorkflowAssignmentMessageForEnsureTest(t, *test.existing))
			}
			engine := mustNewTestEngine(t, store, &fakeClient{}, mustWorkflowEnsureRegistry(t), Config{Model: "gpt-5"})
			before := liveWorkflowAssignmentCountForEnsureTest(engine)
			ensure, err := engine.EnsureWorkflowAssignment(current)
			if err != nil {
				t.Fatalf("EnsureWorkflowAssignment: %v", err)
			}
			receipt, err := ensure.Wait(t.Context())
			if err != nil || !receipt.Committed {
				t.Fatalf("live assignment ensure receipt = %+v, error = %v", receipt, err)
			}
			after := liveWorkflowAssignmentCountForEnsureTest(engine)
			if got := after - before; got != test.wantAppended {
				t.Fatalf("appended live workflow assignments = %d, want %d", got, test.wantAppended)
			}
			if got, present := activeWorkflowAssignmentIdentity(engine.transcriptRuntimeState().SnapshotItems()); !present || got != current.Prompt.Identity {
				t.Fatalf("active live workflow identity = %q, present=%t, want %q", got, present, current.Prompt.Identity)
			}
		})
	}
}

func TestEnsureLiveWorkflowAssignmentConcurrentCallsAppendOnce(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, mustWorkflowEnsureRegistry(t), Config{Model: "gpt-5"})
	assignment := workflowAssignmentForEnsureTest(t, "task-live-concurrent", "node-current")

	const callers = 8
	results := make(chan error, callers)
	for range callers {
		go func() {
			ensure, err := engine.EnsureWorkflowAssignment(assignment)
			if err != nil {
				results <- err
				return
			}
			receipt, err := ensure.Wait(t.Context())
			if err != nil {
				results <- err
				return
			}
			if !receipt.Committed {
				results <- fmt.Errorf("assignment ensure receipt was not committed")
				return
			}
			results <- nil
		}()
	}
	for range callers {
		if err := <-results; err != nil {
			t.Fatalf("concurrent assignment ensure: %v", err)
		}
	}
	if count := liveWorkflowAssignmentCountForEnsureTest(engine); count != 1 {
		t.Fatalf("concurrent workflow assignment count = %d, want one", count)
	}
}

func workflowAssignmentForEnsureTest(t *testing.T, taskID, nodeID string) WorkflowAssignment {
	t.Helper()
	reference, err := workflow.NewCurrentNodeReference(workflow.TaskID(taskID), workflow.NodeID(nodeID), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	assignment := workflowAssignmentForCommitReceiptTest()
	assignment.Prompt.Identity = workflowruntime.CurrentNodePromptIdentity(reference)
	assignment.Prompt.Instructions.CurrentNode = reference
	return assignment
}

func mustWorkflowAssignmentMessageForEnsureTest(t *testing.T, assignment WorkflowAssignment) llm.Message {
	t.Helper()
	message, err := buildWorkflowAssignmentMessage(assignment)
	if err != nil {
		t.Fatalf("build workflow assignment message: %v", err)
	}
	return message
}

func persistedWorkflowAssignmentContextForEnsureTest() PersistedWorkflowAssignmentContext {
	return PersistedWorkflowAssignmentContext{
		Workdir:         ".",
		Model:           "gpt-5",
		ThinkingLevel:   "medium",
		SkillPolicy:     config.SkillPolicy{},
		EnabledTools:    nil,
		GlobalConfigDir: "",
	}
}

func workflowAssignmentCountForEnsureTest(t *testing.T, store *session.Store) int {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(64)
	if err != nil {
		t.Fatalf("read bounded assignment records: %v", err)
	}
	count := 0
	for _, record := range window.Records {
		payload, payloadErr := record.Payload()
		if payloadErr != nil {
			t.Fatalf("decode assignment record: %v", payloadErr)
		}
		message, ok := payload.(session.MessageRecord)
		if ok && message.MessageType != nil && *message.MessageType == session.MessageTypeWorkflowMode {
			count++
		}
	}
	return count
}

func mustWorkflowEnsureRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	return tools.NewRegistry()
}

func liveWorkflowAssignmentCountForEnsureTest(engine *Engine) int {
	count := 0
	for _, item := range engine.transcriptRuntimeState().SnapshotItems() {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeWorkflowMode {
			count++
		}
	}
	return count
}
