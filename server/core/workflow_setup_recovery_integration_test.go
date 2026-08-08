package core

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/metadata"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/clientui"
	brand "core/shared/config"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

func TestCoreWorkflowSetupFailureRecoversThroughTypedSubscriptionAndImmediateResume(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	initializeCoreSetupRecoveryRepository(t, workspace)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	binding, err := metadata.RegisterBinding(ctx, resolved.Config.PersistenceRoot, resolved.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	metadataStore, err := metadata.Open(resolved.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	if err := metadataStore.SetProjectKey(ctx, binding.ProjectID, "E2E"); err != nil {
		_ = metadataStore.Close()
		t.Fatalf("SetProjectKey: %v", err)
	}
	workflowStore, err := workflowstore.New(
		metadataStore,
		workflowstore.WithRoleResolver(configRoleResolver{settings: resolved.Config.Settings}),
	)
	if err != nil {
		_ = metadataStore.Close()
		t.Fatalf("workflowstore.New: %v", err)
	}
	taskID := createCoreSetupRecoveryTask(t, workflowStore, binding)
	if err := metadataStore.Close(); err != nil {
		t.Fatalf("close setup metadata store: %v", err)
	}

	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())
	workflowClient := appCore.WorkflowClient()
	worktreeClient := appCore.WorktreeClient()
	attentionSub, err := appCore.AttentionNotificationClient().SubscribeAttentionNotifications(
		ctx,
		serverapi.AttentionNotificationSubscribeRequest{},
	)
	if err != nil {
		t.Fatalf("SubscribeAttentionNotifications: %v", err)
	}
	t.Cleanup(func() { _ = attentionSub.Close() })

	firstSetupID := serverapi.NewWorktreeSetupOperationID()
	firstSetupSub, err := worktreeClient.SubscribeWorktreeSetup(ctx, serverapi.WorktreeSetupSubscribeRequest{
		SetupOperationID: firstSetupID,
	})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup first: %v", err)
	}
	t.Cleanup(func() { _ = firstSetupSub.Close() })
	startResult := make(chan struct {
		response serverapi.WorkflowTaskStartResponse
		err      error
	}, 1)
	go func() {
		response, startErr := workflowClient.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
			TaskID:           string(taskID),
			SetupOperationID: firstSetupID,
			ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
				Mode: serverapi.WorkflowExecutionTargetModeHead,
			},
		})
		startResult <- struct {
			response serverapi.WorkflowTaskStartResponse
			err      error
		}{response: response, err: startErr}
	}()

	waitForCoreSetupRecoveryPath(t, filepath.Join(workspace, "setup-started"))
	var started serverapi.WorkflowTaskStartResponse
	select {
	case result := <-startResult:
		if result.err != nil {
			t.Fatalf("StartWorkflowTask: %v", result.err)
		}
		started = result.response
	case <-time.After(3 * time.Second):
		t.Fatal("StartWorkflowTask waited for the blocked setup script")
	}
	if started.Outcome != serverapi.WorkflowTaskActionOutcomeApplied || started.Applied == nil {
		t.Fatalf("StartWorkflowTask response = %+v, want applied before setup completion", started)
	}
	if err := os.WriteFile(filepath.Join(workspace, "release-setup"), []byte("release\n"), 0o644); err != nil {
		t.Fatalf("release setup script: %v", err)
	}

	firstEvents := nextCoreSetupEvents(t, firstSetupSub, 3)
	if firstEvents[0].Phase != serverapi.WorktreeSetupPhaseStarted ||
		firstEvents[1].Phase != serverapi.WorktreeSetupPhaseStarted ||
		firstEvents[2].Phase != serverapi.WorktreeSetupPhaseFailed ||
		firstEvents[2].Failed == nil ||
		firstEvents[2].Failed.RetryReadiness != serverapi.WorktreeSetupRetryReady ||
		firstEvents[2].Failed.Cause.Kind != serverapi.WorktreeSetupFailureProcessExit {
		t.Fatalf("first setup events = %+v, want two attempts and retry-ready process failure", firstEvents)
	}
	attention := nextCoreSetupRecoveryAttention(t, attentionSub, firstSetupID, string(taskID))
	taskAttention, err := workflowClient.ListWorkflowTaskAttention(ctx, serverapi.WorkflowTaskAttentionListRequest{
		TaskID: string(taskID),
	})
	if err != nil {
		t.Fatalf("ListWorkflowTaskAttention after setup failure: %v", err)
	}
	if len(taskAttention.Items) != 1 ||
		taskAttention.Items[0].SetupOperationID == nil ||
		*taskAttention.Items[0].SetupOperationID != firstSetupID ||
		taskAttention.Items[0].CurrentNode == nil ||
		attention.Pending.Target.CurrentNodeID == nil ||
		taskAttention.Items[0].CurrentNode.NodeID != *attention.Pending.Target.CurrentNodeID {
		t.Fatalf("durable and live canonical setup attention disagree: durable=%+v live=%+v", taskAttention, attention)
	}

	detail, err := workflowClient.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: string(taskID)})
	if err != nil {
		t.Fatalf("GetWorkflowTask with provisional worktree: %v", err)
	}
	if detail.Task.ExecutionTarget != nil || detail.Task.WorktreePath != nil {
		t.Fatalf(
			"provisional Task detail target = %+v, worktree path = %v; want readable unlocked Task",
			detail.Task.ExecutionTarget,
			detail.Task.WorktreePath,
		)
	}
	_, err = workflowClient.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           string(taskID),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeHead,
		},
	})
	var startConflict *serverapi.WorkflowTaskStartConflictError
	if !errors.As(err, &startConflict) ||
		startConflict.Reason != serverapi.WorkflowTaskStartConflictAlreadyStarted {
		t.Fatalf("repeated Start error = %T %v, want typed already-started conflict", err, err)
	}

	if err := os.WriteFile(filepath.Join(workspace, "setup-fixed"), []byte("fixed\n"), 0o644); err != nil {
		t.Fatalf("fix setup script condition: %v", err)
	}
	secondSetupID := serverapi.NewWorktreeSetupOperationID()
	secondSetupSub, err := worktreeClient.SubscribeWorktreeSetup(ctx, serverapi.WorktreeSetupSubscribeRequest{
		SetupOperationID: secondSetupID,
	})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup Resume: %v", err)
	}
	t.Cleanup(func() { _ = secondSetupSub.Close() })
	resumed, err := workflowClient.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           string(taskID),
		SetupOperationID: secondSetupID,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeHead,
		},
	})
	if err != nil ||
		resumed.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied ||
		resumed.Applied == nil {
		t.Fatalf("immediate ResumeWorkflowTask = %+v, %v; want applied", resumed, err)
	}
	secondEvents := nextCoreSetupEvents(t, secondSetupSub, 2)
	if secondEvents[0].Phase != serverapi.WorktreeSetupPhaseStarted ||
		secondEvents[1].Phase != serverapi.WorktreeSetupPhaseCompleted {
		t.Fatalf("Resume setup events = %+v, want one successful attempt", secondEvents)
	}
	detail, err = workflowClient.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: string(taskID)})
	if err != nil {
		t.Fatalf("GetWorkflowTask after Resume: %v", err)
	}
	if detail.Task.ExecutionTarget == nil ||
		detail.Task.ExecutionTarget.Mode != serverapi.WorkflowExecutionTargetModeHead ||
		detail.Task.WorktreePath == nil ||
		strings.TrimSpace(*detail.Task.WorktreePath) == "" {
		t.Fatalf("Task detail after Resume = %+v, want locked managed target", detail.Task)
	}
}

func initializeCoreSetupRecoveryRepository(t *testing.T, workspace string) {
	t.Helper()
	configRoot := filepath.Join(workspace, brand.ConfigDirName)
	scriptRoot := filepath.Join(workspace, "scripts")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("create workspace config root: %v", err)
	}
	if err := os.MkdirAll(scriptRoot, 0o755); err != nil {
		t.Fatalf("create setup script root: %v", err)
	}
	configText := "[worktrees]\nsetup_script = \"scripts/setup-worktree.sh\"\nsetup_timeout_seconds = 30\n"
	if err := os.WriteFile(filepath.Join(configRoot, "config.toml"), []byte(configText), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
	script := `#!/bin/sh
set -eu
touch "$1/setup-started"
while [ ! -f "$1/release-setup" ]; do sleep 0.02; done
if [ ! -f "$1/setup-fixed" ]; then
  echo "setup intentionally failing" >&2
  exit 7
fi
`
	scriptPath := filepath.Join(scriptRoot, "setup-worktree.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write setup script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("setup recovery\n"), 0o644); err != nil {
		t.Fatalf("write repository content: %v", err)
	}
	runCoreSetupRecoveryGit(t, workspace, "init", "-q")
	runCoreSetupRecoveryGit(t, workspace, "config", "user.email", "kent@example.invalid")
	runCoreSetupRecoveryGit(t, workspace, "config", "user.name", "Kent Test")
	runCoreSetupRecoveryGit(t, workspace, "add", ".")
	runCoreSetupRecoveryGit(t, workspace, "commit", "-q", "-m", "initial")
}

func createCoreSetupRecoveryTask(
	t *testing.T,
	store *workflowstore.Store,
	binding metadata.Binding,
) workflow.TaskID {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Setup recovery"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := coreWorkflowNodeByKind(t, definition, workflow.NodeKindStart)
	terminal := coreWorkflowNodeByKind(t, definition, workflow.NodeKindTerminal)
	agentID := workflow.NodeID("node-" + uuid.NewString())
	if _, err := store.AddNode(ctx, workflowstore.NodeRecord{
		ID:           agentID,
		WorkflowID:   created.ID,
		Key:          "agent",
		Kind:         workflow.NodeKindAgent,
		DisplayName:  "Agent",
		SubagentRole: "default",
	}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	startGroupID := workflow.TransitionGroupID("group-" + uuid.NewString())
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{
		ID:           startGroupID,
		WorkflowID:   created.ID,
		SourceNodeID: workflow.NodeIDOf(start),
		TransitionID: "start",
		DisplayName:  "Start",
	}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{
		ID:                workflow.EdgeID("edge-" + uuid.NewString()),
		WorkflowID:        created.ID,
		TransitionGroupID: startGroupID,
		Key:               "start",
		TargetNodeID:      agentID,
		AssigneeSelection: workflow.AssigneeSelectionConfigured,
		ThinkingSelection: workflow.ThinkingSelectionConfigured,
		ContextMode:       workflow.ContextModeNewSession,
		PromptTemplate:    "Do work.",
	}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	doneGroupID := workflow.TransitionGroupID("group-" + uuid.NewString())
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{
		ID:           doneGroupID,
		WorkflowID:   created.ID,
		SourceNodeID: agentID,
		TransitionID: "done",
		DisplayName:  "Done",
	}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{
		ID:                workflow.EdgeID("edge-" + uuid.NewString()),
		WorkflowID:        created.ID,
		TransitionGroupID: doneGroupID,
		Key:               "done",
		TargetNodeID:      workflow.NodeIDOf(terminal),
		AssigneeSelection: workflow.AssigneeSelectionConfigured,
		ThinkingSelection: workflow.ThinkingSelectionConfigured,
		ContextMode:       workflow.ContextModeNewSession,
	}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	if _, err := store.LinkWorkflow(ctx, binding.ProjectID, created.ID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := store.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:         binding.ProjectID,
		WorkflowID:        &created.ID,
		Title:             "Recover setup",
		Body:              "Exercise the complete setup recovery path.",
		SourceWorkspaceID: binding.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task.ID
}

func nextCoreSetupEvents(
	t *testing.T,
	sub serverapi.WorktreeSetupSubscription,
	count int,
) []serverapi.WorktreeSetupEvent {
	t.Helper()
	events := make([]serverapi.WorktreeSetupEvent, 0, count)
	for len(events) < count {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		event, err := sub.Next(ctx)
		cancel()
		if err != nil {
			t.Fatalf("next Worktree Setup event %d: %v", len(events), err)
		}
		events = append(events, event)
	}
	return events
}

func nextCoreSetupRecoveryAttention(
	t *testing.T,
	sub serverapi.AttentionNotificationSubscription,
	setupID serverapi.WorktreeSetupOperationID,
	taskID string,
) clientui.AttentionNotificationEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		event, err := sub.Next(ctx)
		cancel()
		if err != nil {
			t.Fatalf("next setup-recovery attention: %v", err)
		}
		if event.Type != clientui.AttentionNotificationEventPending ||
			event.Pending == nil ||
			event.Pending.Kind != clientui.AttentionNotificationKindInterruptedCurrentNode ||
			event.Pending.Target.TaskID != taskID ||
			event.Pending.Target.Focus == nil ||
			event.Pending.Target.Focus.SetupOperationID == nil ||
			*event.Pending.Target.Focus.SetupOperationID != setupID.String() {
			continue
		}
		return event
	}
	t.Fatalf("live setup-recovery attention did not arrive for operation %s", setupID)
	return clientui.AttentionNotificationEvent{}
}

func waitForCoreSetupRecoveryPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("path did not appear: %s", path)
}

func runCoreSetupRecoveryGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
