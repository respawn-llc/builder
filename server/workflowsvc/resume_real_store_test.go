package workflowsvc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestServiceResumeUsesRealStorePreflightForMixedAndAllInvalidNodes(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createServiceResumeBoundaryWorkflow(t, ctx, service, binding.ProjectID)

	mixedTask, err := service.store.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:         binding.ProjectID,
		WorkflowID:        &workflowID,
		SourceWorkspaceID: binding.WorkspaceID,
		Title:             "Mixed resume",
		Body:              "Mixed resume",
	})
	if err != nil {
		t.Fatalf("CreateTask mixed: %v", err)
	}
	mixedReferences := seedServiceResumeCurrentNodes(t, ctx, metadataStore, mixedTask.ID, workflowID, true)

	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := newServiceResumeRunner()
	attention := &serviceResumeAttention{}
	controller, err := workflowexecution.NewCurrentNodeController(
		service.store,
		runner,
		authority,
		service.mutationPermit,
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:  2,
			Attention:         attention,
			AssignmentSteerer: serviceResumeAssignmentSteerer{},
		},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	service.currentNodeExecution = controller
	t.Cleanup(func() {
		runner.releaseStart()
		_ = controller.Close()
		_ = authority.Close(ctx)
	})

	response, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           string(mixedTask.ID),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget:  &serverapi.WorkflowExecutionTargetSelection{Mode: serverapi.WorkflowExecutionTargetModeNone},
	})
	if err == nil {
		t.Fatal("ResumeWorkflowTask mixed returned nil error")
	}
	diagnostics := serviceResumeValidationDiagnostics(err)
	if len(diagnostics) != 2 {
		t.Fatalf("ResumeWorkflowTask mixed error = %T %v, want two typed diagnostics", err, err)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Code != workflowstore.CurrentNodeResumeParameterNotMaterializedCode ||
			diagnostic.CurrentNode.TaskID != mixedTask.ID ||
			diagnostic.EnteringEdgeID == "" ||
			diagnostic.ParameterKey == "" {
			t.Fatalf("mixed resume diagnostic = %+v, want complete typed context", diagnostic)
		}
	}
	assertServiceResumeDiagnostic(t, diagnostics, mixedReferences.invalid,
		workflow.EdgeID("edge-invalid-"+workflowID.String()), "risk")
	assertServiceResumeDiagnostic(t, diagnostics, mixedReferences.synth,
		workflow.EdgeID("edge-join-synth-"+workflowID.String()), "risk")
	if response.Applied != nil {
		t.Fatalf("mixed ResumeWorkflowTask response = %+v, want no applied response on validation error", response)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return runner.starts() == 1
	}, "valid mixed Current Node did not start")
	runner.releaseStart()
	if got := runner.references(); len(got) != 1 || !got[0].Equal(mixedReferences.valid) {
		t.Fatalf("started mixed Current Nodes = %+v, want valid %v", got, mixedReferences.valid)
	}
	nodes, err := service.store.ListCurrentNodes(ctx, mixedTask.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes mixed: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("mixed Current Nodes = %+v, want three persisted branches", nodes)
	}
	for _, node := range nodes {
		if node.Reference.Equal(mixedReferences.valid) {
			if node.Scheduling == nil || node.Scheduling.State == workflow.CurrentNodeSchedulingInterrupted {
				state := "<nil>"
				if node.Scheduling != nil {
					state = string(node.Scheduling.State)
				}
				t.Fatalf("valid mixed Current Node = %+v, state=%q, want resumed and queued", node, state)
			}
			continue
		}
		if node.Scheduling == nil || node.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
			t.Fatalf("invalid mixed Current Node = %+v, want interrupted", node)
		}
	}
	if got := attention.resolved(); len(got) != 1 || !got[0].CurrentNode.Equal(mixedReferences.valid) {
		t.Fatalf("mixed attention resolutions = %+v, want valid branch only", got)
	}
	if got := attention.pending(); len(got) != 0 {
		t.Fatalf("mixed attention pending publications = %+v, want none during resume preflight", got)
	}

	allInvalidTask, err := service.store.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:         binding.ProjectID,
		WorkflowID:        &workflowID,
		SourceWorkspaceID: binding.WorkspaceID,
		Title:             "All invalid resume",
		Body:              "All invalid resume",
	})
	if err != nil {
		t.Fatalf("CreateTask all-invalid: %v", err)
	}
	seedServiceResumeCurrentNodes(t, ctx, metadataStore, allInvalidTask.ID, workflowID, false)
	beforeStarts := runner.starts()
	_, err = service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           string(allInvalidTask.ID),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget:  &serverapi.WorkflowExecutionTargetSelection{Mode: serverapi.WorkflowExecutionTargetModeNone},
	})
	if err == nil {
		t.Fatal("ResumeWorkflowTask all-invalid returned nil error")
	}
	if diagnostics := serviceResumeValidationDiagnostics(err); len(diagnostics) != 2 {
		t.Fatalf("ResumeWorkflowTask all-invalid error = %T %v, want two typed diagnostics", err, err)
	}
	if runner.starts() != beforeStarts {
		t.Fatalf("all-invalid runner starts = %d, want unchanged %d", runner.starts(), beforeStarts)
	}
	allInvalidNodes, err := service.store.ListCurrentNodes(ctx, allInvalidTask.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes all-invalid: %v", err)
	}
	for _, node := range allInvalidNodes {
		if node.Scheduling == nil || node.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
			t.Fatalf("all-invalid Current Node = %+v, want interrupted and unqueued", node)
		}
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, allInvalidTask.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext all-invalid: %v", err)
	}
	if targetContext.Task.ExecutionTarget != nil {
		t.Fatalf("all-invalid Resume materialized execution target = %+v, want no preparation mutation", targetContext.Task.ExecutionTarget)
	}
}

func serviceResumeValidationDiagnostics(err error) []workflowstore.CurrentNodeResumeValidationDiagnostic {
	if err == nil {
		return nil
	}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		var diagnostics []workflowstore.CurrentNodeResumeValidationDiagnostic
		for _, child := range joined.Unwrap() {
			diagnostics = append(diagnostics, serviceResumeValidationDiagnostics(child)...)
		}
		return diagnostics
	}
	var validationErr *workflowstore.CurrentNodeResumeValidationError
	if errors.As(err, &validationErr) {
		return append([]workflowstore.CurrentNodeResumeValidationDiagnostic(nil), validationErr.Diagnostics...)
	}
	return nil
}

type serviceResumeReferences struct {
	valid   workflow.CurrentNodeReference
	invalid workflow.CurrentNodeReference
	synth   workflow.CurrentNodeReference
}

func assertServiceResumeDiagnostic(
	t *testing.T,
	diagnostics []workflowstore.CurrentNodeResumeValidationDiagnostic,
	currentNode workflow.CurrentNodeReference,
	edgeID workflow.EdgeID,
	parameterKey string,
) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.CurrentNode.Equal(currentNode) &&
			diagnostic.EnteringEdgeID == edgeID &&
			diagnostic.ParameterKey == parameterKey {
			return
		}
	}
	t.Fatalf("diagnostics = %+v, want Current Node %v, Edge %s, Parameter %q", diagnostics, currentNode, edgeID, parameterKey)
}

func createServiceResumeBoundaryWorkflow(t *testing.T, ctx context.Context, service *Service, projectID string) runtimeids.WorkflowID {
	t.Helper()
	created, _, err := service.store.CreateAndLinkWorkflow(ctx, workflowstore.CreateAndLinkWorkflowRequest{
		Name:          "Resume boundary",
		ProjectID:     projectID,
		DefaultPolicy: workflowstore.WorkflowLinkDefaultAlways,
	})
	if err != nil {
		t.Fatalf("CreateAndLinkWorkflow: %v", err)
	}
	definition, record, err := service.store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	var start, done workflow.NodeID
	for _, node := range definition.Nodes {
		switch node.Kind() {
		case workflow.NodeKindStart:
			start = workflow.NodeIDOf(node)
		case workflow.NodeKindTerminal:
			done = workflow.NodeIDOf(node)
		}
	}
	source := workflow.NodeID("node-source-" + created.ID.String())
	valid := workflow.NodeID("node-valid-" + created.ID.String())
	invalid := workflow.NodeID("node-invalid-" + created.ID.String())
	join := workflow.NodeID("node-join-" + created.ID.String())
	synth := workflow.NodeID("node-synth-" + created.ID.String())
	startGroup := workflow.TransitionGroupID("group-start-" + created.ID.String())
	splitGroup := workflow.TransitionGroupID("group-split-" + created.ID.String())
	validJoinGroup := workflow.TransitionGroupID("group-valid-join-" + created.ID.String())
	invalidJoinGroup := workflow.TransitionGroupID("group-invalid-join-" + created.ID.String())
	joinSynthGroup := workflow.TransitionGroupID("group-join-synth-" + created.ID.String())
	synthDoneGroup := workflow.TransitionGroupID("group-synth-done-" + created.ID.String())
	req := workflowstore.WorkflowGraphSaveRequest{
		WorkflowID:      created.ID,
		ExpectedVersion: record.Version,
		Nodes: []workflowstore.NodeRecord{
			{ID: start, WorkflowID: created.ID, Key: "start", Kind: workflow.NodeKindStart, DisplayName: "Start"},
			{ID: source, WorkflowID: created.ID, Key: "source", Kind: workflow.NodeKindAgent, DisplayName: "Source", SubagentRole: config.DefaultSubagentRole},
			{ID: valid, WorkflowID: created.ID, Key: "valid", Kind: workflow.NodeKindAgent, DisplayName: "Valid", SubagentRole: config.DefaultSubagentRole},
			{ID: invalid, WorkflowID: created.ID, Key: "invalid", Kind: workflow.NodeKindAgent, DisplayName: "Invalid", SubagentRole: config.DefaultSubagentRole},
			{ID: join, WorkflowID: created.ID, Key: "join", Kind: workflow.NodeKindJoin, DisplayName: "Join", JoinInputProviders: []workflow.JoinInputProvider{{InputName: "joined", ProviderEdgeID: workflow.EdgeID("edge-valid-join-" + created.ID.String())}}},
			{ID: synth, WorkflowID: created.ID, Key: "synth", Kind: workflow.NodeKindAgent, DisplayName: "Synth", SubagentRole: config.DefaultSubagentRole},
			{ID: done, WorkflowID: created.ID, Key: "done", Kind: workflow.NodeKindTerminal, DisplayName: "Done"},
		},
		TransitionGroups: []workflowstore.TransitionGroupRecord{
			{ID: startGroup, WorkflowID: created.ID, SourceNodeID: start, TransitionID: "start", DisplayName: "Start"},
			{ID: splitGroup, WorkflowID: created.ID, SourceNodeID: source, TransitionID: "split", DisplayName: "Split"},
			{ID: validJoinGroup, WorkflowID: created.ID, SourceNodeID: valid, TransitionID: "join_valid", DisplayName: "Join"},
			{ID: invalidJoinGroup, WorkflowID: created.ID, SourceNodeID: invalid, TransitionID: "join_invalid", DisplayName: "Join"},
			{ID: joinSynthGroup, WorkflowID: created.ID, SourceNodeID: join, TransitionID: "synth", DisplayName: "Synthesize"},
			{ID: synthDoneGroup, WorkflowID: created.ID, SourceNodeID: synth, TransitionID: "done", DisplayName: "Done"},
		},
		Edges: []workflowstore.EdgeRecord{
			{ID: workflow.EdgeID("edge-start-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: source, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Start."},
			{ID: workflow.EdgeID("edge-valid-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "valid", TargetNodeID: valid, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Valid."},
			{ID: workflow.EdgeID("edge-invalid-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "invalid", TargetNodeID: invalid, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Invalid.", Parameters: []workflow.Parameter{{Key: "risk", Description: "Risk."}}},
			{ID: workflow.EdgeID("edge-valid-join-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: validJoinGroup, Key: "join_valid", TargetNodeID: join, ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "joined", Description: "Joined value."}, {Key: "risk", Description: "Derived risk."}}},
			{ID: workflow.EdgeID("edge-invalid-join-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: invalidJoinGroup, Key: "join_invalid", TargetNodeID: join, ContextMode: workflow.ContextModeNewSession},
			{ID: workflow.EdgeID("edge-join-synth-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: joinSynthGroup, Key: "synth", TargetNodeID: synth, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Synthesize."},
			{ID: workflow.EdgeID("edge-synth-done-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: synthDoneGroup, Key: "done", TargetNodeID: done, ContextMode: workflow.ContextModeNewSession},
		},
	}
	saved, err := service.store.SaveWorkflowGraph(ctx, req)
	if err != nil || !saved.Saved {
		t.Fatalf("SaveWorkflowGraph = %+v, err=%v", saved, err)
	}
	return created.ID
}

func seedServiceResumeCurrentNodes(t *testing.T, ctx context.Context, store *metadata.Store, taskID workflow.TaskID, workflowID runtimeids.WorkflowID, mixed bool) serviceResumeReferences {
	t.Helper()
	validID := workflow.NodeID("node-valid-" + workflowID.String())
	invalidID := workflow.NodeID("node-invalid-" + workflowID.String())
	synthID := workflow.NodeID("node-synth-" + workflowID.String())
	valid, err := workflow.NewCurrentNodeReference(taskID, validID, nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference valid: %v", err)
	}
	invalidBranch := workflow.TransitionBranchKey("invalid")
	invalid, err := workflow.NewCurrentNodeReference(taskID, invalidID, &invalidBranch)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference invalid: %v", err)
	}
	synthBranch := workflow.TransitionBranchKey("synth")
	synth, err := workflow.NewCurrentNodeReference(taskID, synthID, &synthBranch)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference synth: %v", err)
	}
	type seed struct {
		reference workflow.CurrentNodeReference
		inputs    string
		edgeID    string
	}
	seeds := []seed{
		{reference: invalid, inputs: `{}`, edgeID: "edge-invalid-" + workflowID.String()},
		{reference: synth, inputs: `{"joined":"existing"}`, edgeID: "edge-join-synth-" + workflowID.String()},
	}
	if mixed {
		validBranch := workflow.TransitionBranchKey("valid")
		valid, err = workflow.NewCurrentNodeReference(taskID, validID, &validBranch)
		if err != nil {
			t.Fatalf("NewCurrentNodeReference valid branch: %v", err)
		}
		seeds = append(seeds, seed{reference: valid, inputs: `{}`, edgeID: "edge-valid-" + workflowID.String()})
	}
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM task_current_nodes WHERE task_id = ?`, string(taskID)); err != nil {
		t.Fatalf("clear serial Current Node: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO task_active_fanouts (task_id) VALUES (?)`, string(taskID)); err != nil {
		t.Fatalf("seed active fanout: %v", err)
	}
	for _, item := range seeds {
		branchKey, branchScoped := item.reference.TransitionBranchKey()
		if !branchScoped {
			t.Fatalf("seed Current Node %v is not branch-scoped", item.reference)
		}
		if _, err := store.DB().ExecContext(ctx, `
INSERT INTO task_active_fanout_branches (
    task_id, transition_branch_key, arrival_state, arrival_values_json
) VALUES (?, ?, 'pending', NULL)`, string(taskID), string(branchKey)); err != nil {
			t.Fatalf("seed active fanout branch %v: %v", item.reference, err)
		}
	}
	for _, item := range seeds {
		branchKey, _ := item.reference.TransitionBranchKey()
		_, err := store.DB().ExecContext(ctx, `
INSERT INTO task_current_nodes (
    task_id, node_id, transition_branch_key, current_input_values_json, prior_node_values_json,
    scheduling_state, interruption_reason, interruption_detail_json, interrupted_at_unix_ms, entered_by_edge_id
) VALUES (?, ?, ?, ?, '{"transition_parameters":{}}', 'interrupted', 'workflow_runtime_start_failed', '{}', 1, ?)`,
			string(taskID), string(item.reference.NodeID), string(branchKey), item.inputs, item.edgeID)
		if err != nil {
			t.Fatalf("seed service Resume Current Node %v: %v", item.reference, err)
		}
	}
	return serviceResumeReferences{valid: valid, invalid: invalid, synth: synth}
}

type serviceResumeRunner struct {
	mu          sync.Mutex
	nodes       []workflow.CurrentNodeReference
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func newServiceResumeRunner() *serviceResumeRunner {
	return &serviceResumeRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (r *serviceResumeRunner) StartCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery, _ workflowexecution.CurrentNodeAssignmentSteer, _ sessionruntime.WorkflowExecutionLease, _ workflowruntime.Controller) error {
	r.mu.Lock()
	r.nodes = append(r.nodes, reference)
	r.mu.Unlock()
	select {
	case <-r.entered:
	default:
		close(r.entered)
	}
	<-r.release
	return nil
}

func (r *serviceResumeRunner) releaseStart() {
	r.releaseOnce.Do(func() { close(r.release) })
}

func (r *serviceResumeRunner) starts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.nodes)
}

func (r *serviceResumeRunner) references() []workflow.CurrentNodeReference {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]workflow.CurrentNodeReference(nil), r.nodes...)
}

type serviceResumeAssignmentSteerer struct{}

func (serviceResumeAssignmentSteerer) SteerCurrentNodeAssignment(context.Context, workflow.CurrentNodeReference) (workflowexecution.CurrentNodeAssignmentSteer, error) {
	return serviceResumeAssignmentSteer{}, nil
}

type serviceResumeAssignmentSteer struct{}

func (serviceResumeAssignmentSteer) Wait(context.Context) (session.CommitReceipt, error) {
	return session.CommitReceipt{Committed: true}, nil
}

type serviceResumeAttention struct {
	mu          sync.Mutex
	pendingRefs []workflow.CurrentNodeReference
	resolutions []workflowstore.TaskAttentionResolution
}

func (a *serviceResumeAttention) PublishPendingInterruptedCurrentNode(_ context.Context, reference workflow.CurrentNodeReference) {
	a.mu.Lock()
	a.pendingRefs = append(a.pendingRefs, reference)
	a.mu.Unlock()
}

func (a *serviceResumeAttention) FinalizeTaskResolution(resolution workflowstore.TaskAttentionResolution) {
	a.mu.Lock()
	a.resolutions = append(a.resolutions, resolution)
	a.mu.Unlock()
}

func (a *serviceResumeAttention) pending() []workflow.CurrentNodeReference {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]workflow.CurrentNodeReference(nil), a.pendingRefs...)
}

func (a *serviceResumeAttention) resolved() []workflowstore.InterruptedCurrentNodeAttentionProjection {
	a.mu.Lock()
	defer a.mu.Unlock()
	var result []workflowstore.InterruptedCurrentNodeAttentionProjection
	for _, resolution := range a.resolutions {
		result = append(result, resolution.InterruptedCurrentNodes...)
	}
	return result
}
