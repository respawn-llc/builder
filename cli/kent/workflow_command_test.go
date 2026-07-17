package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/server/workflowsvc"
	"core/server/workflowview"
	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/sessionenv"
)

type workflowCommandLoopbackRemote struct {
	apicontract.WorkflowService
	cfg                   config.App
	binding               metadata.Binding
	projectBindingsByRoot map[string]serverapi.ProjectBinding
	metadataStore         *metadata.Store
	store                 *workflowstore.Store
	closeErr              error
	closeCalls            int
	listRequests          []serverapi.WorkflowListRequest
}

func (r *workflowCommandLoopbackRemote) Close() error {
	r.closeCalls++
	return r.closeErr
}

func (r *workflowCommandLoopbackRemote) ListWorkflows(ctx context.Context, req serverapi.WorkflowListRequest) (serverapi.WorkflowListResponse, error) {
	r.listRequests = append(r.listRequests, req)
	return r.WorkflowService.ListWorkflows(ctx, req)
}

func (r *workflowCommandLoopbackRemote) SubscribeWorktreeSetup(ctx context.Context, req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return noopWorktreeSetupSubscription{}, nil
}

func (r *workflowCommandLoopbackRemote) ResolveProjectPath(ctx context.Context, req serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	if binding, ok := r.projectBindingsByRoot[req.Path]; ok {
		return serverapi.ProjectResolvePathResponse{CanonicalRoot: req.Path, Binding: &binding}, nil
	}
	if req.Path != r.cfg.WorkspaceRoot {
		return serverapi.ProjectResolvePathResponse{}, nil
	}
	return serverapi.ProjectResolvePathResponse{Binding: &serverapi.ProjectBinding{ProjectID: r.binding.ProjectID, WorkspaceID: r.binding.WorkspaceID, CanonicalRoot: r.cfg.WorkspaceRoot}}, nil
}

func TestWorkflowCommandsExposeJSONAndPersistGraphState(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := workflowCreateForTest(t, "JSON Workflow").ID
	if workflowID == "" {
		t.Fatal("workflow create did not return a workflow id")
	}

	listOut, _ := runWorkflowRootCommandOK(t, "workflow", "list", "--json")
	var list workflowListOutput
	if err := json.Unmarshal([]byte(listOut), &list); err != nil {
		t.Fatalf("workflow list --json = %q, want JSON: %v", listOut, err)
	}
	if !workflowListContains(list.Workflows, workflowID) {
		t.Fatalf("workflow list --json = %+v, want created workflow", list.Workflows)
	}

	if validation, code := workflowValidateJSONForTest(t, workflowID); code == 0 || validation.Valid {
		t.Fatalf("validation before wiring code=%d valid=%v, want invalid", code, validation.Valid)
	}

	node := workflowNodeAddForTest(t, workflowID, "--key", "implement", "--kind", "agent", "--display-name", "Implement", "--agent", "workflow-test", "--prompt", "Do work")
	if node.NodeID == "" || node.Key != "implement" || node.Kind != "agent" {
		t.Fatalf("workflow node add --json = %+v, want implement agent node", node)
	}
	startEdge := workflowEdgeAddForTest(t, workflowID,
		"--from", "backlog", "--transition", "start", "--edge-key", "start", "--to", "implement", "--context", "new_session",
		"--prompt", "Do work")
	doneEdge := workflowEdgeAddForTest(t, workflowID, "--from", "implement", "--transition", "done", "--edge-key", "done", "--to", "done", "--context", "new_session")
	if startEdge.EdgeID == "" || startEdge.TransitionGroupID == "" || doneEdge.EdgeID == "" {
		t.Fatalf("workflow edge add --json start=%+v done=%+v, want ids", startEdge, doneEdge)
	}

	def := workflowInspectDefinitionForTest(t, workflowID)
	if def.Workflow.ID != workflowID || len(def.Nodes) != 3 || len(def.Edges) != 2 {
		t.Fatalf("workflow definition = %+v, want created graph", def)
	}
	link := workflowLinkForTest(t, binding.ProjectID, workflowID, "--default")
	if link.ID == "" || !link.Default {
		t.Fatalf("workflow link --json = %+v, want default link", link)
	}
	if validation, code := workflowValidateJSONForTest(t, workflowID); code != 0 || !validation.Valid {
		t.Fatalf("validation after wiring code=%d valid=%v, want valid", code, validation.Valid)
	}
}

func TestWorkflowNodeAddRejectsSecondStart(t *testing.T) {
	cfg, _, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()
	workflowID := workflowCreateForTest(t, "Duplicate Start").ID

	_, stderr, code := runWorkflowRootCommand(
		"workflow", "node", "add", workflowID,
		"--key", "second_start", "--kind", "start",
	)
	if code == 0 || strings.TrimSpace(stderr) == "" {
		t.Fatalf("second Start exit=%d stderr_present=%t, want actionable command failure", code, strings.TrimSpace(stderr) != "")
	}
}

func TestWorkflowJSONFlagPlacementCompatibility(t *testing.T) {
	cfg, _, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	createOut, _, code := runWorkflowRootCommand("workflow", "create", "Trailing Flag Flow", "--json")
	if code != 0 {
		t.Fatalf("workflow create trailing --json exit=%d", code)
	}
	var record serverapi.WorkflowRecord
	if err := json.Unmarshal([]byte(createOut), &record); err != nil {
		t.Fatalf("workflow create trailing --json = %q, want JSON: %v", createOut, err)
	}
	if record.Name != "Trailing Flag Flow" {
		t.Fatalf("workflow name = %q, want trailing flag name", record.Name)
	}
	selector, err := parseWorkflowSelector(record.ID)
	if err != nil {
		t.Fatalf("created workflow id %q: %v", record.ID, err)
	}

	nodeOut, _, code := runWorkflowRootCommand("workflow", "node", "add", "--json", selector.String(), "--key", "implement", "--kind", "agent", "--agent", "workflow-test", "--prompt", "Do work")
	if code != 0 {
		t.Fatalf("workflow node add leading --json exit=%d", code)
	}
	var node workflowNodeOutput
	if err := json.Unmarshal([]byte(nodeOut), &node); err != nil {
		t.Fatalf("workflow node add leading --json = %q, want JSON: %v", nodeOut, err)
	}
	if node.WorkflowID != record.ID || node.Key != "implement" {
		t.Fatalf("workflow node add leading --json = %+v, want implement node", node)
	}
}

func TestWorkflowUpdateRoundTripsExecutionTargetPolicies(t *testing.T) {
	cfg, _, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := workflowCreateForTest(t, "Target Policy Workflow").ID
	for _, selector := range []string{
		"none",
		"head",
		"default-branch",
		"ref:release/v1",
		"ask-on-first-execution",
	} {
		output, _ := runWorkflowRootCommandOK(t, "workflow", "update", workflowID, "--execution-target", selector, "--json")
		var record serverapi.WorkflowRecord
		if err := json.Unmarshal([]byte(output), &record); err != nil {
			t.Fatalf("decode workflow update %q output %q: %v", selector, output, err)
		}
		if got := workflowExecutionTargetPolicySelector(record.ExecutionTargetPolicy); got != selector {
			t.Fatalf("workflow update %q returned policy %q (%+v)", selector, got, record.ExecutionTargetPolicy)
		}
		def := workflowInspectDefinitionForTest(t, workflowID)
		if got := workflowExecutionTargetPolicySelector(def.Workflow.ExecutionTargetPolicy); got != selector {
			t.Fatalf("workflow inspect after %q returned policy %q (%+v)", selector, got, def.Workflow.ExecutionTargetPolicy)
		}
	}

	human, _ := runWorkflowRootCommandOK(t, "workflow", "inspect", workflowID)
	if !strings.Contains(human, "ask-on-first-execution") {
		t.Fatalf("workflow inspect omitted execution target policy: %s", human)
	}
}

func TestWorkflowEditCommandsPersistNodeAndEdgeMetadata(t *testing.T) {
	cfg, _, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := workflowCreateForTest(t, "Editable Workflow").ID
	workflowNodeAddForTest(t, workflowID, "--key", "triaging", "--kind", "agent", "--display-name", "Triaging", "--agent", "workflow-test", "--prompt", "Triage.")
	startEdgeID := workflowEdgeAddForTest(t, workflowID, "--from", "backlog", "--transition", "start", "--edge-key", "start", "--to", "triaging", "--context", "new_session", "--prompt", "Triage.").EdgeID
	edgeID := workflowEdgeAddForTest(t, workflowID, "--from", "triaging", "--transition", "done", "--edge-key", "done", "--to", "done", "--context", "continue_session", "--context-source", "node:triaging").EdgeID

	nodeOut, _ := runWorkflowRootCommandOK(t, "workflow", "node", "update", workflowID, "triaging", "--json", "--prompt", "Decide whether the ticket is actionable.", "--completion-mode", "structured_output")
	var updatedNode workflowNodeOutput
	if err := json.Unmarshal([]byte(nodeOut), &updatedNode); err != nil {
		t.Fatalf("workflow node update --json = %q, want JSON: %v", nodeOut, err)
	}
	if updatedNode.Key != "triaging" || updatedNode.NodeID == "" {
		t.Fatalf("workflow node update --json = %+v, want triaging node", updatedNode)
	}

	runWorkflowRootCommandOK(t, "workflow", "edge", "update", workflowID, startEdgeID, "--json", "--transition", "start_review")
	runWorkflowRootCommandOK(t, "workflow", "edge", "update", workflowID, edgeID, "--json",
		"--transition", "not_actionable",
		"--edge-key", "not_actionable",
		"--transition-description", "Pick when the task should close.",
		"--param", "reason=Closure reason")

	def := workflowInspectDefinitionForTest(t, workflowID)
	node := workflowNodeByIDForTest(t, def, updatedNode.NodeID)
	if node.PromptTemplate != "Decide whether the ticket is actionable." || node.CompletionMode != "structured_output" {
		t.Fatalf("updated node = %+v, want prompt and completion mode persisted", node)
	}

	if validation, code := workflowValidateJSONForTest(t, workflowID); code != 0 || !validation.Valid {
		t.Fatalf("validation code=%d valid=%v, want valid after start transition update", code, validation.Valid)
	}

	updatedEdge := workflowEdgeByKeyForTest(t, def, "not_actionable")
	if updatedEdge.ContextSource.Kind != "selected_node" || updatedEdge.ContextSource.NodeKey != "triaging" {
		t.Fatalf("edge context source = %+v, want selected_node triaging", updatedEdge.ContextSource)
	}
	if got := workflowNodeKeyForID(def, updatedEdge.TargetNodeID); got != "done" {
		t.Fatalf("edge target = %q, want done", got)
	}
	if len(updatedEdge.Parameters) != 1 || updatedEdge.Parameters[0].Key != "reason" {
		t.Fatalf("edge parameters = %+v, want reason parameter", updatedEdge.Parameters)
	}
	if group := workflowTransitionGroupForID(def, updatedEdge.TransitionGroupID); group.TransitionID != "not_actionable" || group.Description != "Pick when the task should close." {
		t.Fatalf("edge transition group = %+v, want updated transition metadata", group)
	}
}

func TestWorkflowEdgeCommandsPersistTargetDerivedContextSources(t *testing.T) {
	cfg, _, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := workflowCreateForTest(t, "Target Context Workflow").ID
	workflowNodeAddForTest(t, workflowID, "--key", "planning", "--kind", "agent", "--display-name", "Planning", "--agent", "workflow-test", "--prompt", "Plan.")
	workflowNodeAddForTest(t, workflowID, "--key", "review", "--kind", "agent", "--display-name", "Review", "--agent", "workflow-test", "--prompt", "Review.")
	workflowEdgeAddForTest(t, workflowID, "--from", "backlog", "--transition", "start", "--edge-key", "start", "--to", "planning", "--context", "new_session", "--prompt", "Plan.")
	workflowEdgeAddForTest(t, workflowID, "--from", "planning", "--transition", "review", "--edge-key", "review", "--to", "review", "--context", "continue_session", "--context-source", "previous_target_or_new", "--prompt", "Review.")
	reviewLoopEdgeID := workflowEdgeAddForTest(t, workflowID, "--from", "review", "--transition", "loop_review", "--edge-key", "loop_review", "--to", "review", "--context", "continue_session", "--prompt", "Review again.").EdgeID

	def := workflowInspectDefinitionForTest(t, workflowID)
	reviewEdge := workflowEdgeByKeyForTest(t, def, "review")
	if reviewEdge.ContextSource.Kind != "previous_target_or_new" || reviewEdge.ContextSource.NodeKey != "" {
		t.Fatalf("added review edge context source = %+v, want previous_target_or_new", reviewEdge.ContextSource)
	}

	runWorkflowRootCommandOK(t, "workflow", "edge", "update", workflowID, reviewLoopEdgeID, "--json", "--context-source", "previous_target")
	updated := workflowEdgeByKeyForTest(t, workflowInspectDefinitionForTest(t, workflowID), "loop_review")
	if updated.ContextSource.Kind != "previous_target" || updated.ContextSource.NodeKey != "" {
		t.Fatalf("updated review edge context source = %+v, want previous_target", updated.ContextSource)
	}
}

func TestWorkflowEdgeUpdateTogglesRequiresApproval(t *testing.T) {
	cfg, _, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := workflowCreateForTest(t, "Approval Toggle").ID
	workflowNodeAddForTest(t, workflowID, "--key", "implement", "--kind", "agent", "--agent", "workflow-test", "--prompt", "Do work")
	edgeID := workflowEdgeAddForTest(t, workflowID, "--from", "backlog", "--transition", "start", "--edge-key", "start", "--to", "implement", "--context", "new_session", "--prompt", "Go").EdgeID

	runWorkflowRootCommandOK(t, "workflow", "edge", "update", workflowID, edgeID, "--json", "--requires-approval")
	if !workflowEdgeByKeyForTest(t, workflowInspectDefinitionForTest(t, workflowID), "start").RequiresApproval {
		t.Fatal("edge update --requires-approval did not persist the approval gate")
	}

	runWorkflowRootCommandOK(t, "workflow", "edge", "update", workflowID, edgeID, "--json", "--requires-approval=false")
	if workflowEdgeByKeyForTest(t, workflowInspectDefinitionForTest(t, workflowID), "start").RequiresApproval {
		t.Fatal("edge update --requires-approval=false did not clear the approval gate")
	}
}

func TestWorkflowNodeCompletionModeAddAndPreserve(t *testing.T) {
	cfg, _, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := workflowCreateForTest(t, "Completion Mode").ID
	added := workflowNodeAddForTest(t, workflowID, "--key", "implement", "--kind", "agent", "--agent", "workflow-test", "--prompt", "Do work", "--completion-mode", "tool")
	node := workflowNodeByIDForTest(t, workflowInspectDefinitionForTest(t, workflowID), added.NodeID)
	if node.CompletionMode != "tool" {
		t.Fatalf("added node completion mode = %q, want tool", node.CompletionMode)
	}

	runWorkflowRootCommandOK(t, "workflow", "node", "update", workflowID, "implement", "--json", "--display-name", "Implement It")
	node = workflowNodeByIDForTest(t, workflowInspectDefinitionForTest(t, workflowID), added.NodeID)
	if node.CompletionMode != "tool" {
		t.Fatalf("updated node completion mode = %q, want preserved tool", node.CompletionMode)
	}
}

func TestWorkflowNodeScriptPathAddUpdateAndInspect(t *testing.T) {
	cfg, _, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := workflowCreateForTest(t, "Script Path").ID
	added := workflowNodeAddForTest(t, workflowID, "--key", "script", "--kind", "script", "--display-name", "Script", "--script-path", "scripts/complete")
	if added.ScriptPath == nil || *added.ScriptPath != "scripts/complete" {
		t.Fatalf("added script path = %+v, want scripts/complete", added.ScriptPath)
	}
	node := workflowNodeByIDForTest(t, workflowInspectDefinitionForTest(t, workflowID), added.NodeID)
	if node.ScriptPath == nil || *node.ScriptPath != "scripts/complete" {
		t.Fatalf("stored script node = %+v, want script path", node)
	}

	runWorkflowRootCommandOK(t, "workflow", "node", "update", workflowID, "script", "--json", "--display-name", "Renamed Script")
	node = workflowNodeByIDForTest(t, workflowInspectDefinitionForTest(t, workflowID), added.NodeID)
	if node.ScriptPath == nil || *node.ScriptPath != "scripts/complete" {
		t.Fatalf("script path after display update = %+v, want preserved", node.ScriptPath)
	}

	runWorkflowRootCommandOK(t, "workflow", "node", "update", workflowID, "script", "--json", "--script-path=")
	node = workflowNodeByIDForTest(t, workflowInspectDefinitionForTest(t, workflowID), added.NodeID)
	if node.ScriptPath != nil {
		t.Fatalf("script path after clear = %+v, want nil", node.ScriptPath)
	}

	runWorkflowRootCommandOK(t, "workflow", "node", "update", workflowID, "script", "--json", "--script-path", "scripts/fixed")
	inspectOut, _ := runWorkflowRootCommandOK(t, "workflow", "inspect", workflowID)
	if !strings.Contains(inspectOut, "- script (script): Renamed Script  [script: scripts/fixed]") {
		t.Fatalf("workflow inspect output = %q, want script path node line", inspectOut)
	}
}

func TestWorkflowNodeUpdatePreservesCanonicalWiringFields(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &preservingNodeUpdateRemote{}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	_, stderr, code := runWorkflowRootCommand("workflow", "node", "update", workflowSelectorTestUUID, "join", "--json", "--display-name", "Updated Join")
	if code != 0 {
		t.Fatalf("workflow node update exit=%d stderr=%q", code, stderr)
	}
	if remote.updateReq.DisplayName != "Updated Join" {
		t.Fatalf("update request display name = %q, want Updated Join", remote.updateReq.DisplayName)
	}
	if len(remote.updateReq.InputFields) != 1 || remote.updateReq.InputFields[0].Name != "handoff" || remote.updateReq.InputFields[0].Description != "Branch handoff." {
		t.Fatalf("update request input fields = %+v, want existing fields preserved", remote.updateReq.InputFields)
	}
	if len(remote.updateReq.JoinInputProviders) != 1 || remote.updateReq.JoinInputProviders[0].InputName != "handoff" || remote.updateReq.JoinInputProviders[0].ProviderEdgeID != "edge-branch-join" {
		t.Fatalf("update request join providers = %+v, want existing providers preserved", remote.updateReq.JoinInputProviders)
	}
}

func TestWorkflowEdgeUpdatePreservesAndClearsParameters(t *testing.T) {
	cfg, _, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := workflowCreateForTest(t, "Parameter Workflow").ID
	workflowNodeAddForTest(t, workflowID, "--key", "triaging", "--kind", "agent", "--display-name", "Triaging", "--agent", "workflow-test", "--prompt", "Triage.")
	edgeID := workflowEdgeAddForTest(t, workflowID, "--from", "backlog", "--transition", "start", "--edge-key", "start", "--to", "triaging", "--context", "new_session", "--prompt", "Triage.", "--param", "plan_file_path=Path to the plan doc").EdgeID

	ctx := context.Background()
	runWorkflowRootCommandOK(t, "workflow", "edge", "update", workflowID, edgeID, "--json", "--transition-description", "Start review")
	updated := workflowCommandStoredEdgeByID(t, ctx, remote.store, workflowID, edgeID)
	if updated.PromptTemplate != "Triage." || len(updated.Parameters) != 1 || updated.Parameters[0].Key != "plan_file_path" {
		t.Fatalf("edge after metadata update = %+v, want prompt and parameters preserved", updated)
	}

	if _, _, code := runWorkflowRootCommand("workflow", "edge", "update", workflowID, edgeID, "--param", "x=y", "--clear-params"); code != 2 {
		t.Fatalf("combined --param/--clear-params exit=%d, want rejection exit 2", code)
	}
	rejected := workflowCommandStoredEdgeByID(t, ctx, remote.store, workflowID, edgeID)
	if len(rejected.Parameters) != 1 {
		t.Fatalf("edge after rejected update = %+v, want unchanged parameters", rejected)
	}

	runWorkflowRootCommandOK(t, "workflow", "edge", "update", workflowID, edgeID, "--json", "--clear-params")
	cleared := workflowCommandStoredEdgeByID(t, ctx, remote.store, workflowID, edgeID)
	if len(cleared.Parameters) != 0 || cleared.PromptTemplate != "Triage." {
		t.Fatalf("edge after clear = %+v, want cleared parameters and preserved prompt", cleared)
	}
}

func TestWorkflowEdgeUpdateRollsBackTransitionGroupWhenEdgeUpdateFails(t *testing.T) {
	cfg, _, loopback := newWorkflowCommandLoopback(t)
	remote := &failingWorkflowEdgeUpdateRemote{workflowCommandLoopbackRemote: loopback}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := workflowCreateForTest(t, "Rollback Workflow").ID
	workflowNodeAddForTest(t, workflowID, "--key", "triaging", "--kind", "agent", "--display-name", "Triaging", "--agent", "workflow-test", "--prompt", "Triage.")
	edgeID := workflowEdgeAddForTest(t, workflowID, "--from", "backlog", "--transition", "start", "--edge-key", "start", "--to", "triaging", "--context", "new_session", "--prompt", "Triage.").EdgeID
	remote.failUpdateEdge = true

	if _, _, code := runWorkflowRootCommand("workflow", "edge", "update", workflowID, edgeID, "--transition", "changed"); code == 0 {
		t.Fatal("workflow edge update succeeded despite injected edge update failure")
	}
	def, _, err := loopback.store.GetDefinition(context.Background(), workflow.WorkflowID(workflowPersistedIDForTest(t, workflowID)))
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	edge := workflowCommandStoredEdgeByID(t, context.Background(), loopback.store, workflowID, edgeID)
	group := workflow.TransitionGroup{}
	for _, candidate := range def.TransitionGroups {
		if candidate.ID == edge.TransitionGroupID {
			group = candidate
			break
		}
	}
	if group.TransitionID != "start" {
		t.Fatalf("transition group after failed update = %+v, want original start", group)
	}
}

func TestTaskCommandsExposeJSONAndPersistState(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Task Workflow")

	createOut, _ := runWorkflowRootCommandOK(t, "task", "create", "--json", "--title", "Task", "--body", "Body", "--workflow", workflowID, "--project", binding.ProjectID)
	var created taskShowOutput
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("task create --json = %q, want JSON: %v", createOut, err)
	}
	if created.Summary.ID == "" || created.Summary.ShortID == "" || created.Body != "Body" || created.Workflow.WorkflowID != workflowID || created.Summary.WorkflowID != workflowID {
		t.Fatalf("task create --json = %+v, want created task detail", created)
	}

	listOut, _ := runWorkflowRootCommandOK(t, "task", "list", "--project", binding.ProjectID, "--json")
	var list taskListOutput
	if err := json.Unmarshal([]byte(listOut), &list); err != nil {
		t.Fatalf("task list --json = %q, want JSON: %v", listOut, err)
	}
	if list.ProjectID != binding.ProjectID || len(list.Tasks) != 1 || list.Tasks[0].TaskID != created.Summary.ID || list.Tasks[0].ShortID != created.Summary.ShortID {
		t.Fatalf("task list --json = %+v, want created task", list)
	}

	editOut, _ := runWorkflowRootCommandOK(t, "task", "edit", created.Summary.ShortID, "--project", binding.ProjectID, "--json", "--title", "Updated", "--body", "Updated body", "--source-workspace", binding.WorkspaceID)
	var editResp serverapi.WorkflowTaskUpdateResponse
	if err := json.Unmarshal([]byte(editOut), &editResp); err != nil {
		t.Fatalf("task edit --json = %q, want JSON: %v", editOut, err)
	}
	if editResp.Task.Title != "Updated" || editResp.Task.SourceWorkspaceID != binding.WorkspaceID {
		t.Fatalf("task edit response = %+v, want updated title and source workspace", editResp)
	}

	showOut, _ := runWorkflowRootCommandOK(t, "task", "show", "--project", binding.ProjectID, "--json", created.Summary.ShortID)
	var shown taskShowOutput
	if err := json.Unmarshal([]byte(showOut), &shown); err != nil {
		t.Fatalf("task show --json = %q, want JSON: %v", showOut, err)
	}
	if shown.Summary.ID != created.Summary.ID ||
		shown.Summary.Title != "Updated" ||
		shown.Body != "Updated body" ||
		shown.Summary.SourceWorkspaceID != binding.WorkspaceID ||
		shown.Summary.WorkflowID != workflowID ||
		shown.Workflow.WorkflowID != workflowID {
		t.Fatalf("task show --json = %+v, want updated task detail", shown)
	}
	var shownFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(showOut), &shownFields); err != nil {
		t.Fatalf("task show --json raw object = %q, want JSON: %v", showOut, err)
	}
	for _, omitted := range []string{"attention", "placements", "runs", "transitions", "comments"} {
		if _, ok := shownFields[omitted]; ok {
			t.Fatalf("task show --json included unbounded field %q", omitted)
		}
	}
}

func TestTaskCommentAuthorForAddUsesCurrentWorkflowRun(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-workflow")
	oldStartedAt := int64(20)
	currentStartedAt := int64(10)
	remote := &commentAuthorRemote{task: serverapi.WorkflowTaskDetail{
		Status: serverapi.WorkflowTaskStatus{RunIDs: []string{"run-current"}},
		Placements: []serverapi.WorkflowPlacement{
			{NodeID: "node-old", NodeKey: "old"},
			{NodeID: "node-current", NodeKey: "current"},
		},
		Runs: []serverapi.WorkflowRun{
			{ID: "run-old", SessionID: "session-workflow", Role: "old-role", NodeID: "node-old", StartedAtUnixMs: &oldStartedAt},
			{ID: "run-current", SessionID: "session-workflow", Role: "current-role", NodeID: "node-current", StartedAtUnixMs: &currentStartedAt},
		},
	}}
	got := taskCommentAuthorForAdd(context.Background(), remote, "task-1", "", false)
	if got.Kind != "agent" || got.ID != "current-role" {
		t.Fatalf("taskCommentAuthorForAdd = %+v, want current workflow run role", got)
	}
}

func TestTaskCommentAuthorForAddBoundaryCases(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "")
	if got := taskCommentAuthorForAdd(context.Background(), &commentAuthorRemote{}, "task-1", "", false); got.Kind != "user" || got.ID != "" {
		t.Fatalf("taskCommentAuthorForAdd without session = %+v, want user", got)
	}

	t.Setenv(sessionenv.SessionIDEnv, "session-workflow")
	oldStartedAt := int64(10)
	newStartedAt := int64(20)
	nodeFallbackRemote := &commentAuthorRemote{task: serverapi.WorkflowTaskDetail{
		Status:     serverapi.WorkflowTaskStatus{RunIDs: []string{"run-current"}},
		Placements: []serverapi.WorkflowPlacement{{NodeID: "node-current", NodeKey: "current"}},
		Runs:       []serverapi.WorkflowRun{{ID: "run-current", SessionID: "session-workflow", NodeID: "node-current"}},
	}}
	if got := taskCommentAuthorForAdd(context.Background(), nodeFallbackRemote, "task-1", "", false); got.Kind != "agent" || got.ID != "Node current agent" {
		t.Fatalf("taskCommentAuthorForAdd node fallback = %+v, want current node agent", got)
	}

	latestRunRemote := &commentAuthorRemote{task: serverapi.WorkflowTaskDetail{
		Placements: []serverapi.WorkflowPlacement{
			{NodeID: "node-old", NodeKey: "old"},
			{NodeID: "node-new", NodeKey: "new"},
		},
		Runs: []serverapi.WorkflowRun{
			{ID: "run-old", SessionID: "session-workflow", NodeID: "node-old", StartedAtUnixMs: &oldStartedAt},
			{ID: "run-new", SessionID: "session-workflow", NodeID: "node-new", StartedAtUnixMs: &newStartedAt},
		},
	}}
	if got := taskCommentAuthorForAdd(context.Background(), latestRunRemote, "task-1", "", false); got.Kind != "agent" || got.ID != "Node new agent" {
		t.Fatalf("taskCommentAuthorForAdd latest run fallback = %+v, want latest node agent", got)
	}

	t.Setenv(sessionenv.SessionIDEnv, "session-other")
	sessionFallbackRemote := &commentAuthorRemote{sessionName: "triage"}
	if got := taskCommentAuthorForAdd(context.Background(), sessionFallbackRemote, "task-1", "", false); got.Kind != "agent" || got.ID != "Session triage agent" {
		t.Fatalf("taskCommentAuthorForAdd session fallback = %+v, want session-name agent", got)
	}
}

func TestWorkflowHelpSmoke(t *testing.T) {
	_, stderr, code := runWorkflowRootCommand("workflow", "--help")
	if code != 0 {
		t.Fatalf("workflow --help exit=%d, want 0", code)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Fatal("workflow --help output is empty")
	}
}

func TestWorkflowValidateRejectsRemovedProjectFlag(t *testing.T) {
	_, _, code := runWorkflowRootCommand("workflow", "validate", "workflow-id", "--project", "project-id")
	if code != 2 {
		t.Fatalf("workflow validate with removed --project exit=%d, want 2", code)
	}
}

func TestWorkflowCommandValidationErrorsAreExitTwo(t *testing.T) {
	for _, args := range [][]string{
		{"workflow", "create"},
		{"workflow", "node", "add"},
		{"workflow", "edge", "add"},
		{"task", "resume"},
		{"task", "approve"},
		{"task", "move"},
	} {
		_, _, code := runWorkflowRootCommand(args...)
		if code != 2 {
			t.Fatalf("%v exit=%d, want usage/validation exit 2", args, code)
		}
	}
}

func TestParseWorkflowParameters(t *testing.T) {
	params, err := parseWorkflowParameters(repeatedStringFlag{"plan_file_path=Path to plan", "changes=Requested changes"})
	if err != nil {
		t.Fatalf("parseWorkflowParameters: %v", err)
	}
	if len(params) != 2 || params[0].Key != "plan_file_path" || params[1].Description != "Requested changes" {
		t.Fatalf("params = %+v, want parsed key/description pairs", params)
	}

	for _, raw := range []repeatedStringFlag{
		{"missing-separator"},
		{"Invalid=bad key"},
		{"transition=reserved"},
		{"summary=first", "summary=duplicate"},
	} {
		if _, err := parseWorkflowParameters(raw); err == nil {
			t.Fatalf("parseWorkflowParameters(%+v) succeeded, want validation error", raw)
		}
	}
}

func TestWorkflowListPaginatesWithoutSelectorLookup(t *testing.T) {
	const secondWorkflowSelector = "8e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &pagedWorkflowListRemote{
		delayAfterFirstPage: true,
		pages: map[string]serverapi.WorkflowListResponse{
			"": {
				Workflows: []serverapi.WorkflowRecord{
					{ID: "workflow-" + workflowSelectorTestUUID, Name: "First", Version: 1},
				},
				NextPageToken: "next",
			},
			"next": {
				Workflows: []serverapi.WorkflowRecord{
					{ID: "workflow-" + secondWorkflowSelector, Name: "Second", Version: 2},
				},
			},
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("workflow", "list", "--json")
	if code != 0 {
		t.Fatalf("workflow list exit=%d stderr=%q", code, stderr)
	}
	var listed workflowListOutput
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
		t.Fatalf("workflow list --json = %q, want JSON: %v", stdout, err)
	}
	if len(listed.Workflows) != 1 || listed.Workflows[0].ID != workflowSelectorTestUUID || listed.NextPageToken != "next" {
		t.Fatalf("workflow list --json = %+v, want first page plus token", listed)
	}
	if len(remote.requests) != 1 || remote.requests[0].PageToken != "" || remote.requests[0].PageSize != serverapi.WorkflowListMaxPageSize {
		t.Fatalf("workflow list requests = %+v, want single default-sized first page", remote.requests)
	}

	if len(remote.deadlines) != 1 {
		t.Fatalf("list deadlines = %+v, want one bounded list call", remote.deadlines)
	}
}

func TestWorkflowListProjectScopeResolvesPathAndIDAndProjectsMetadata(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Project Workflow")
	out, _, code := runWorkflowRootCommand("workflow", "list", "--project", binding.ProjectID, "--json")
	if code != 0 {
		t.Fatalf("workflow list project id exit=%d", code)
	}
	var byID workflowListOutput
	if err := json.Unmarshal([]byte(out), &byID); err != nil {
		t.Fatalf("decode project-id workflow list: %v", err)
	}
	if byID.ProjectID == nil || *byID.ProjectID != binding.ProjectID || !workflowListContains(byID.Workflows, workflowID) {
		t.Fatalf("project-id response = %+v, want project context and workflow", byID)
	}
	if len(byID.Workflows) != 1 || byID.Workflows[0].ProjectLink == nil {
		t.Fatalf("project-id workflow metadata = %+v, want project link metadata", byID.Workflows)
	}

	out, _, code = runWorkflowRootCommand("workflow", "list", "--project", cfg.WorkspaceRoot, "--json")
	if code != 0 {
		t.Fatalf("workflow list project path exit=%d", code)
	}
	var byPath workflowListOutput
	if err := json.Unmarshal([]byte(out), &byPath); err != nil {
		t.Fatalf("decode project-path workflow list: %v", err)
	}
	if byPath.ProjectID == nil || *byPath.ProjectID != binding.ProjectID || !workflowListContains(byPath.Workflows, workflowID) {
		t.Fatalf("project-path response = %+v, want same project discovery", byPath)
	}
	if len(remote.ListRequests()) != 2 {
		t.Fatalf("workflow list requests = %+v, want one request per scope", remote.ListRequests())
	}
	for _, req := range remote.ListRequests() {
		if req.ProjectID == nil || *req.ProjectID != binding.ProjectID {
			t.Fatalf("workflow list request = %+v, want resolved project id", req)
		}
	}
}

func TestWorkflowListRejectsExplicitBlankProjectBeforeOpeningRemote(t *testing.T) {
	opened := false
	original := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		opened = true
		return config.App{}, nil, nil
	}
	defer func() { workflowCommandRemoteOpener = original }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := workflowListSubcommand([]string{"--project", ""}, &stdout, &stderr)
	if code != 2 || opened || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("workflow list blank project exit=%d opened=%v stdout=%q stderr=%q", code, opened, stdout.String(), stderr.String())
	}
}

func TestWorkflowListProjectResponseRequiresProjectLinkMetadata(t *testing.T) {
	projectID := "project-1"
	remote := &pagedWorkflowListRemote{
		pages: map[string]serverapi.WorkflowListResponse{
			"": {
				ProjectID: &projectID,
				Workflows: []serverapi.WorkflowRecord{{
					ID:                    "workflow-" + workflowSelectorTestUUID,
					Name:                  "Workflow",
					Version:               1,
					ExecutionTargetPolicy: serverapi.WorkflowExecutionTargetConfiguration{Mode: serverapi.WorkflowExecutionTargetModeNone},
				}},
			},
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: "."}, remote)
	defer restore()

	stdout, _, code := runWorkflowRootCommand("workflow", "list", "--project", projectID)
	if code != 1 || stdout != "" {
		t.Fatalf("workflow list exit=%d stdout=%q, want impossible project-link response failure", code, stdout)
	}
}

func TestWorkflowListRejectsResponseScopeMismatch(t *testing.T) {
	requestedProjectID := "project-1"
	otherProjectID := "project-2"
	projectWorkflow := serverapi.WorkflowRecord{
		ID:                    "workflow-" + workflowSelectorTestUUID,
		ProjectLink:           &serverapi.WorkflowListProjectLink{},
		ExecutionTargetPolicy: serverapi.WorkflowExecutionTargetConfiguration{Mode: serverapi.WorkflowExecutionTargetModeNone},
	}
	for name, testCase := range map[string]struct {
		expected *string
		response *string
		records  []serverapi.WorkflowRecord
	}{
		"global response unexpectedly scoped": {
			response: &otherProjectID,
		},
		"project response missing scope": {
			expected: &requestedProjectID,
		},
		"project response mismatches scope": {
			expected: &requestedProjectID,
			response: &otherProjectID,
			records:  []serverapi.WorkflowRecord{projectWorkflow},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateWorkflowListProjectMetadata(workflowListExpectedScope{ProjectID: testCase.expected}, testCase.response, testCase.records); err == nil {
				t.Fatalf("validateWorkflowListProjectMetadata(%s) accepted mismatched response scope", name)
			}
		})
	}
}

func TestWorkflowListProjectContinuationPreservesScopeAndJSONShape(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	setupLinkedWorkflow(t, binding.ProjectID, "First Project Workflow")
	setupLinkedWorkflow(t, binding.ProjectID, "Second Project Workflow")
	firstOut, firstErr, code := runWorkflowRootCommand("workflow", "list", "--project", binding.ProjectID, "--page-size", "1", "--json")
	if code != 0 {
		t.Fatalf("workflow list first project page exit=%d stderr=%q", code, firstErr)
	}
	var first workflowListOutput
	if err := json.Unmarshal([]byte(firstOut), &first); err != nil {
		t.Fatalf("decode first project page: %v", err)
	}
	if first.ProjectID == nil || first.NextPageToken == "" || len(first.Workflows) != 1 {
		t.Fatalf("first project page = %+v, want one row and continuation", first)
	}
	secondOut, secondErr, code := runWorkflowRootCommand("workflow", "list", "--page-size", "1", "--page-token", first.NextPageToken, "--json")
	if code != 0 {
		t.Fatalf("workflow list second project page exit=%d stderr=%q", code, secondErr)
	}
	var second workflowListOutput
	if err := json.Unmarshal([]byte(secondOut), &second); err != nil {
		t.Fatalf("decode second project page: %v", err)
	}
	if second.ProjectID == nil || *second.ProjectID != binding.ProjectID || len(second.Workflows) != 1 {
		t.Fatalf("second project page = %+v, want restored project context", second)
	}
	requests := remote.ListRequests()
	if len(requests) != 2 || requests[1].PageToken != first.NextPageToken || requests[1].ProjectID != nil {
		t.Fatalf("continuation requests = %+v, want token-owned project scope", requests)
	}
}

func TestWorkflowInspectSummaryUsesMetadataListWithoutDefinitionRead(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	const workflowID = "workflow-" + workflowSelectorTestUUID
	remote := &pagedWorkflowListRemote{
		pages: map[string]serverapi.WorkflowListResponse{
			"": {Workflows: []serverapi.WorkflowRecord{{
				ID:                    workflowID,
				Name:                  "Summary Workflow",
				Description:           "Metadata only",
				Version:               7,
				ExecutionTargetPolicy: serverapi.WorkflowExecutionTargetConfiguration{Mode: "head"},
			}}},
		},
		definitions: map[string]serverapi.WorkflowDefinition{},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	out, _, code := runWorkflowRootCommand("workflow", "inspect", workflowSelectorTestUUID, "--summary", "--json")
	if code != 0 {
		t.Fatalf("workflow inspect summary exit=%d", code)
	}
	var summary serverapi.WorkflowRecord
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatalf("decode workflow inspect summary: %v", err)
	}
	if summary.ID != workflowSelectorTestUUID || summary.Name != "Summary Workflow" || summary.Version != 7 {
		t.Fatalf("summary = %+v, want projected metadata record", summary)
	}
	if len(remote.requests) != 1 || remote.requests[0].WorkflowID == nil || *remote.requests[0].WorkflowID != workflowID || remote.requests[0].PageSize != 1 {
		t.Fatalf("summary list requests = %+v, want exact one-row metadata request", remote.requests)
	}
	if remote.getWorkflowCalls != 0 {
		t.Fatalf("summary GetWorkflow calls = %d, want metadata-only path", remote.getWorkflowCalls)
	}
}

func TestWorkflowInspectSummaryRejectsMismatchedResponseScope(t *testing.T) {
	const requestedWorkflowID = "workflow-" + workflowSelectorTestUUID
	const otherWorkflowID = "workflow-8e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	projectID := "project-1"
	for name, response := range map[string]serverapi.WorkflowListResponse{
		"unexpected project scope": {
			ProjectID: &projectID,
			Workflows: []serverapi.WorkflowRecord{{
				ID:          requestedWorkflowID,
				ProjectLink: &serverapi.WorkflowListProjectLink{},
			}},
		},
		"wrong workflow": {
			Workflows: []serverapi.WorkflowRecord{{ID: otherWorkflowID}},
		},
		"multiple workflows": {
			Workflows: []serverapi.WorkflowRecord{{ID: requestedWorkflowID}, {ID: otherWorkflowID}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			remote := &pagedWorkflowListRemote{pages: map[string]serverapi.WorkflowListResponse{"": response}}
			restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
			defer restore()

			stdout, _, code := runWorkflowRootCommand("workflow", "inspect", workflowSelectorTestUUID, "--summary")
			if code != 1 || stdout != "" {
				t.Fatalf("workflow inspect summary mismatch exit=%d stdout=%q", code, stdout)
			}
		})
	}
}

func TestWorkflowProjectPathResolutionRejectsUnboundPath(t *testing.T) {
	cfg, _, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	_, _, code := runWorkflowRootCommand("task", "list", "--project", t.TempDir())
	if code != 1 {
		t.Fatalf("task list unbound path exit=%d, want resolution failure", code)
	}
}

func TestTaskShowCrossProjectShortIDLookupBoundaries(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &crossProjectTaskShowRemote{scopedErr: serverapi.ErrWorkflowTaskNotFound}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	showOut, _ := runWorkflowRootCommandOK(t, "task", "show", "--json", "--project", "project-current", "OTH-1")
	var shown taskShowOutput
	if err := json.Unmarshal([]byte(showOut), &shown); err != nil {
		t.Fatalf("task show --json = %q, want JSON: %v", showOut, err)
	}
	if shown.Summary.ID != "task-other" || remote.unscopedCalls != 1 {
		t.Fatalf("task show fallback = %+v unscopedCalls=%d, want unscoped other-project task", shown, remote.unscopedCalls)
	}

	remote.scopedErr = errors.New("backend unavailable")
	remote.unscopedCalls = 0
	if _, _, code := runWorkflowRootCommand("task", "show", "--json", "--project", "project-current", "OTH-1"); code == 0 {
		t.Fatal("task show succeeded after scoped lookup error")
	}
	if remote.unscopedCalls != 0 {
		t.Fatalf("unscoped calls = %d, want no fallback after scoped lookup error", remote.unscopedCalls)
	}

	remote.scopedErr = serverapi.ErrWorkflowTaskNotFound
	remote.unscopedErr = errors.New("ambiguous short id")
	remote.unscopedCalls = 0
	if _, _, code := runWorkflowRootCommand("task", "show", "--json", "--project", "project-current", "OTH-1"); code == 0 {
		t.Fatal("task show succeeded after unscoped lookup error")
	}
	if remote.unscopedCalls != 1 {
		t.Fatalf("unscoped calls = %d, want one fallback attempt", remote.unscopedCalls)
	}
}

func newWorkflowCommandLoopback(t *testing.T) (config.App, metadata.Binding, *workflowCommandLoopbackRemote) {
	t.Helper()
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENT_PERSISTENCE_ROOT", filepath.Join(home, ".kent"))
	cfg, err := config.Load(workspaceRoot, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if err := metadataStore.SetProjectKey(context.Background(), binding.ProjectID, "WOR"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	resolver := testsetup.QuestionsEnabled("workflow-test")
	store, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(resolver))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	view, err := workflowview.New(metadataStore)
	if err != nil {
		t.Fatalf("workflowview.New: %v", err)
	}
	service, err := workflowsvc.New(store, view, resolver)
	if err != nil {
		t.Fatalf("workflowsvc.New: %v", err)
	}
	remote := &workflowCommandLoopbackRemote{
		WorkflowService:       service,
		cfg:                   cfg,
		binding:               binding,
		projectBindingsByRoot: map[string]serverapi.ProjectBinding{},
		metadataStore:         metadataStore,
		store:                 store,
	}
	return cfg, binding, remote
}

func createWorkflowCommandTestSession(t *testing.T, cfg config.App, binding metadata.Binding, metadataStore *metadata.Store) string {
	t.Helper()
	store, err := session.Create(
		filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
		filepath.Base(cfg.WorkspaceRoot),
		cfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	return store.Meta().SessionID
}

func replaceWorkflowCommandRemoteOpener(t *testing.T, cfg config.App, remote workflowCommandRemote) func() {
	t.Helper()
	original := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return cfg, remote, nil
	}
	return func() { workflowCommandRemoteOpener = original }
}

func setupLinkedWorkflow(t *testing.T, projectID string, name string) string {
	t.Helper()
	workflowID := workflowCreateForTest(t, name).ID
	if workflowID == "" {
		t.Fatal("workflow create did not return a workflow id")
	}
	workflowNodeAddForTest(t, workflowID, "--key", "implement", "--kind", "agent", "--agent", "workflow-test", "--prompt", "Do work")
	workflowEdgeAddForTest(t, workflowID, "--from", "backlog", "--transition", "start", "--edge-key", "start", "--to", "implement", "--context", "new_session", "--prompt", "Do work")
	workflowEdgeAddForTest(t, workflowID, "--from", "implement", "--transition", "done", "--edge-key", "done", "--to", "done", "--context", "new_session")
	workflowLinkForTest(t, projectID, workflowID, "--default")
	return workflowID
}

func runWorkflowRootCommand(args ...string) (string, string, int) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := rootCommand(args, strings.NewReader(""), &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func runWorkflowRootCommandOK(t *testing.T, args ...string) (string, string) {
	t.Helper()
	stdout, stderr, code := runWorkflowRootCommand(args...)
	if code != 0 {
		t.Fatalf("%s exit=%d stdout=%q stderr=%q", strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout, stderr
}

func workflowCreateForTest(t *testing.T, args ...string) serverapi.WorkflowRecord {
	t.Helper()
	full := append([]string{"workflow", "create", "--json"}, args...)
	out, _ := runWorkflowRootCommandOK(t, full...)
	var record serverapi.WorkflowRecord
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatalf("decode workflow create json %q: %v", out, err)
	}
	if _, err := parseWorkflowSelector(record.ID); err != nil {
		t.Fatalf("created workflow id %q: %v", record.ID, err)
	}
	return record
}

func workflowPersistedIDForTest(t *testing.T, selector string) string {
	t.Helper()
	parsed, err := parseWorkflowSelector(selector)
	if err != nil {
		t.Fatalf("workflow selector %q: %v", selector, err)
	}
	return parsed.PersistedID()
}

func workflowNodeAddForTest(t *testing.T, args ...string) workflowNodeOutput {
	t.Helper()
	full := append([]string{"workflow", "node", "add"}, args...)
	full = append(full, "--json")
	out, _ := runWorkflowRootCommandOK(t, full...)
	var node workflowNodeOutput
	if err := json.Unmarshal([]byte(out), &node); err != nil {
		t.Fatalf("decode workflow node add json %q: %v", out, err)
	}
	return node
}

func workflowEdgeAddForTest(t *testing.T, args ...string) workflowEdgeOutput {
	t.Helper()
	full := append([]string{"workflow", "edge", "add"}, args...)
	full = append(full, "--json")
	out, _ := runWorkflowRootCommandOK(t, full...)
	var edge workflowEdgeOutput
	if err := json.Unmarshal([]byte(out), &edge); err != nil {
		t.Fatalf("decode workflow edge add json %q: %v", out, err)
	}
	return edge
}

func workflowLinkForTest(t *testing.T, args ...string) serverapi.ProjectWorkflowLink {
	t.Helper()
	full := append([]string{"workflow", "link"}, args...)
	full = append(full, "--json")
	out, _ := runWorkflowRootCommandOK(t, full...)
	var link serverapi.ProjectWorkflowLink
	if err := json.Unmarshal([]byte(out), &link); err != nil {
		t.Fatalf("decode workflow link json %q: %v", out, err)
	}
	return link
}

func workflowInspectDefinitionForTest(t *testing.T, workflowRef string) serverapi.WorkflowDefinition {
	t.Helper()
	out, _ := runWorkflowRootCommandOK(t, "workflow", "inspect", "--json", workflowRef)
	var def serverapi.WorkflowDefinition
	if err := json.Unmarshal([]byte(out), &def); err != nil {
		t.Fatalf("decode workflow inspect json %q: %v", out, err)
	}
	return def
}

func workflowValidateJSONForTest(t *testing.T, args ...string) (serverapi.WorkflowValidateResponse, int) {
	t.Helper()
	out, _, code := runWorkflowRootCommand(append([]string{"workflow", "validate", "--json"}, args...)...)
	var resp serverapi.WorkflowValidateResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("workflow validate --json %v = %q, want JSON: %v", args, out, err)
	}
	return resp, code
}

func workflowListContains(workflows []serverapi.WorkflowRecord, workflowID string) bool {
	for _, record := range workflows {
		if record.ID == workflowID {
			return true
		}
	}
	return false
}

func workflowNodeByIDForTest(t *testing.T, def serverapi.WorkflowDefinition, nodeID string) serverapi.WorkflowNode {
	t.Helper()
	for _, node := range def.Nodes {
		if node.ID == nodeID {
			return node
		}
	}
	t.Fatalf("node %q not found in definition %+v", nodeID, def.Nodes)
	return serverapi.WorkflowNode{}
}

func workflowEdgeByKeyForTest(t *testing.T, def serverapi.WorkflowDefinition, key string) serverapi.WorkflowEdge {
	t.Helper()
	for _, edge := range def.Edges {
		if edge.Key == key {
			return edge
		}
	}
	t.Fatalf("edge %q not found in definition %+v", key, def.Edges)
	return serverapi.WorkflowEdge{}
}

func workflowNodeKeyForID(def serverapi.WorkflowDefinition, nodeID string) string {
	for _, node := range def.Nodes {
		if node.ID == nodeID {
			return node.Key
		}
	}
	return ""
}

func workflowTransitionGroupForID(def serverapi.WorkflowDefinition, groupID string) serverapi.WorkflowTransitionGroup {
	for _, group := range def.TransitionGroups {
		if group.ID == groupID {
			return group
		}
	}
	return serverapi.WorkflowTransitionGroup{}
}

func workflowCommandStoredEdgeByID(t *testing.T, ctx context.Context, store *workflowstore.Store, workflowID string, edgeID string) workflow.Edge {
	t.Helper()
	def, _, err := store.GetDefinition(ctx, workflow.WorkflowID(workflowPersistedIDForTest(t, workflowID)))
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	for _, edge := range def.Edges {
		if string(edge.ID) == edgeID {
			return edge
		}
	}
	t.Fatalf("missing edge %s in %+v", edgeID, def.Edges)
	return workflow.Edge{}
}

type pagedWorkflowListRemote struct {
	apicontract.WorkflowService
	definitions         map[string]serverapi.WorkflowDefinition
	pages               map[string]serverapi.WorkflowListResponse
	requests            []serverapi.WorkflowListRequest
	deadlines           []time.Time
	getWorkflowCalls    int
	delayAfterFirstPage bool
}

func (r *pagedWorkflowListRemote) Close() error { return nil }

func (r *pagedWorkflowListRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *pagedWorkflowListRemote) ListWorkflows(ctx context.Context, req serverapi.WorkflowListRequest) (serverapi.WorkflowListResponse, error) {
	callIndex := len(r.requests)
	r.requests = append(r.requests, req)
	if deadline, ok := ctx.Deadline(); ok {
		r.deadlines = append(r.deadlines, deadline)
	}
	if r.delayAfterFirstPage && callIndex == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	return r.pages[req.PageToken], nil
}

func (r *pagedWorkflowListRemote) GetWorkflow(ctx context.Context, req serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	r.getWorkflowCalls++
	if deadline, ok := ctx.Deadline(); ok {
		r.deadlines = append(r.deadlines, deadline)
	}
	def, ok := r.definitions[req.WorkflowID]
	if !ok {
		return serverapi.WorkflowGetResponse{}, sql.ErrNoRows
	}
	return serverapi.WorkflowGetResponse{Definition: def}, nil
}

func (r *workflowCommandLoopbackRemote) ListRequests() []serverapi.WorkflowListRequest {
	return append([]serverapi.WorkflowListRequest(nil), r.listRequests...)
}

type preservingNodeUpdateRemote struct {
	apicontract.WorkflowService
	updateReq serverapi.WorkflowNodeUpdateRequest
}

func (r *preservingNodeUpdateRemote) Close() error { return nil }

func (r *preservingNodeUpdateRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *preservingNodeUpdateRemote) ListWorkflows(context.Context, serverapi.WorkflowListRequest) (serverapi.WorkflowListResponse, error) {
	return serverapi.WorkflowListResponse{Workflows: []serverapi.WorkflowRecord{{ID: "workflow-1", Name: "Workflow"}}}, nil
}

func (r *preservingNodeUpdateRemote) GetWorkflow(context.Context, serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	return serverapi.WorkflowGetResponse{Definition: serverapi.WorkflowDefinition{
		Workflow: serverapi.WorkflowRecord{ID: "workflow-" + workflowSelectorTestUUID, Name: "Workflow"},
		Nodes: []serverapi.WorkflowNode{{
			ID:          "node-join",
			WorkflowID:  "workflow-" + workflowSelectorTestUUID,
			Key:         "join",
			Kind:        "join",
			DisplayName: "Join",
			InputFields: []serverapi.WorkflowInputField{{
				Name:        "handoff",
				Description: "Branch handoff.",
			}},
			JoinInputProviders: []serverapi.WorkflowJoinInputProvider{{
				InputName:      "handoff",
				ProviderEdgeID: "edge-branch-join",
			}},
		}},
	}}, nil
}

func (r *preservingNodeUpdateRemote) UpdateWorkflowNode(_ context.Context, req serverapi.WorkflowNodeUpdateRequest) (serverapi.WorkflowNodeUpdateResponse, error) {
	r.updateReq = req
	return serverapi.WorkflowNodeUpdateResponse{Version: 2}, nil
}

type failingWorkflowEdgeUpdateRemote struct {
	*workflowCommandLoopbackRemote
	failUpdateEdge bool
}

func (r *failingWorkflowEdgeUpdateRemote) UpdateWorkflowEdge(ctx context.Context, req serverapi.WorkflowEdgeUpdateRequest) (serverapi.WorkflowEdgeUpdateResponse, error) {
	if r.failUpdateEdge {
		return serverapi.WorkflowEdgeUpdateResponse{}, errors.New("edge update failed")
	}
	return r.workflowCommandLoopbackRemote.UpdateWorkflowEdge(ctx, req)
}
