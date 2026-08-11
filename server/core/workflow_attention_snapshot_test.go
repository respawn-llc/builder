package core

import (
	"context"
	"testing"
	"time"

	"core/internal/testharness/workflowfixture"
	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/metadata"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

func TestCoreRootAttentionSnapshotsCurrentNodeInterruptedDuringStartupRecovery(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	binding, err := metadata.RegisterBinding(context.Background(), resolved.Config.PersistenceRoot, resolved.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	metadataStore, err := metadata.Open(resolved.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	if err := metadataStore.SetProjectKey(context.Background(), binding.ProjectID, "WOR"); err != nil {
		_ = metadataStore.Close()
		t.Fatalf("SetProjectKey: %v", err)
	}
	workflowStore, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(configRoleResolver{settings: resolved.Config.Settings}))
	if err != nil {
		_ = metadataStore.Close()
		t.Fatalf("workflowstore.New: %v", err)
	}
	taskID, currentNode := createCoreStartupRecoveryTask(t, workflowStore, binding.ProjectID)
	if err := metadataStore.Close(); err != nil {
		t.Fatalf("close setup metadata store: %v", err)
	}

	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())
	sub, err := appCore.AttentionNotificationClient().SubscribeAttentionNotifications(
		context.Background(),
		serverapi.AttentionNotificationSubscribeRequest{},
	)
	if err != nil {
		t.Fatalf("SubscribeAttentionNotifications: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	nextContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	event, err := sub.Next(nextContext)
	if err != nil {
		t.Fatalf("Next startup-recovery attention: %v", err)
	}
	if event.Sequence != 1 ||
		event.Source != clientui.AttentionNotificationSourceSnapshot ||
		event.Type != clientui.AttentionNotificationEventPending ||
		event.Pending == nil ||
		event.Pending.Kind != clientui.AttentionNotificationKindInterruptedCurrentNode ||
		event.Pending.InterruptedCurrentNode == nil ||
		event.Pending.InterruptedCurrentNode.Reason != "workflow_startup_recovery" ||
		event.Pending.Target.TaskID != string(taskID) ||
		event.Pending.Target.CurrentNodeID == nil ||
		*event.Pending.Target.CurrentNodeID != string(currentNode.NodeID) {
		t.Fatalf("startup-recovery attention event = %+v", event)
	}
}

func createCoreStartupRecoveryTask(t *testing.T, store *workflowstore.Store, projectID string) (workflow.TaskID, workflow.CurrentNodeReference) {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Startup recovery"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	agentID := workflow.NodeID("node-" + uuid.NewString())
	startGroupID := workflow.TransitionGroupID("group-" + uuid.NewString())
	doneGroupID := workflow.TransitionGroupID("group-" + uuid.NewString())
	workflowfixture.SaveStoreGraph(t, ctx, store, created.ID, func(definition workflow.Definition, request *workflowstore.WorkflowGraphSaveRequest) {
		start := coreWorkflowNodeByKind(t, definition, workflow.NodeKindStart)
		terminal := coreWorkflowNodeByKind(t, definition, workflow.NodeKindTerminal)
		request.Nodes = append(request.Nodes, workflowstore.NodeRecord{
			ID: agentID, WorkflowID: created.ID, Key: "agent",
			Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "default",
		})
		request.TransitionGroups = append(request.TransitionGroups,
			workflowstore.TransitionGroupRecord{ID: startGroupID, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
			workflowstore.TransitionGroupRecord{ID: doneGroupID, WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"},
		)
		request.Edges = append(request.Edges,
			workflowstore.EdgeRecord{
				ID: workflow.EdgeID("edge-" + uuid.NewString()), WorkflowID: created.ID,
				TransitionGroupID: startGroupID, Key: "start", TargetNodeID: agentID,
				AssigneeSelection: workflow.AssigneeSelectionConfigured,
				ThinkingSelection: workflow.ThinkingSelectionConfigured,
				ContextMode:       workflow.ContextModeNewSession, PromptTemplate: "Do work.",
			},
			workflowstore.EdgeRecord{
				ID: workflow.EdgeID("edge-" + uuid.NewString()), WorkflowID: created.ID,
				TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: workflow.NodeIDOf(terminal),
				AssigneeSelection: workflow.AssigneeSelectionConfigured,
				ThinkingSelection: workflow.ThinkingSelectionConfigured,
				ContextMode:       workflow.ContextModeNewSession,
			},
		)
	})
	if _, err := store.LinkWorkflow(ctx, projectID, created.ID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := store.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:  projectID,
		WorkflowID: &created.ID,
		Title:      "Recover me",
		Body:       "Leave executable work ready before Core starts.",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := store.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("StartTask created Current Nodes = %+v", started.Mutation.Created)
	}
	return task.ID, started.Mutation.Created[0].Reference
}

func coreWorkflowNodeByKind(t *testing.T, definition workflow.Definition, kind workflow.NodeKind) workflow.Node {
	t.Helper()
	for _, node := range definition.Nodes {
		if node.Kind() == kind {
			return node
		}
	}
	t.Fatalf("workflow has no %q node", kind)
	return nil
}
