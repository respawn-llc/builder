package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const prefixedWorkflowSelector = "workflow-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

func TestWorkflowSelectorInventoryRejectsPrefixedIDsBeforeOpeningRemote(t *testing.T) {
	tests := []struct {
		name string
		run  func(*bytes.Buffer, *bytes.Buffer) int
	}{
		{"workflow delete", func(stdout, stderr *bytes.Buffer) int {
			return workflowSubcommand([]string{"delete", prefixedWorkflowSelector}, stdout, stderr)
		}},
		{"workflow update", func(stdout, stderr *bytes.Buffer) int {
			return workflowSubcommand([]string{"update", prefixedWorkflowSelector, "--name", "name"}, stdout, stderr)
		}},
		{"workflow node add", func(stdout, stderr *bytes.Buffer) int {
			return workflowSubcommand([]string{"node", "add", prefixedWorkflowSelector, "--key", "node", "--kind", "agent"}, stdout, stderr)
		}},
		{"workflow node update", func(stdout, stderr *bytes.Buffer) int {
			return workflowSubcommand([]string{"node", "update", prefixedWorkflowSelector, "node"}, stdout, stderr)
		}},
		{"workflow edge add", func(stdout, stderr *bytes.Buffer) int {
			return workflowSubcommand([]string{"edge", "add", prefixedWorkflowSelector, "--from", "source", "--transition", "next", "--edge-key", "edge", "--to", "target", "--context", "new_session"}, stdout, stderr)
		}},
		{"workflow edge update", func(stdout, stderr *bytes.Buffer) int {
			return workflowSubcommand([]string{"edge", "update", prefixedWorkflowSelector, "edge"}, stdout, stderr)
		}},
		{"workflow link", func(stdout, stderr *bytes.Buffer) int {
			return workflowSubcommand([]string{"link", "project-1", prefixedWorkflowSelector}, stdout, stderr)
		}},
		{"workflow unlink", func(stdout, stderr *bytes.Buffer) int {
			return workflowSubcommand([]string{"unlink", "project-1", prefixedWorkflowSelector}, stdout, stderr)
		}},
		{"workflow default", func(stdout, stderr *bytes.Buffer) int {
			return workflowSubcommand([]string{"default", "project-1", prefixedWorkflowSelector}, stdout, stderr)
		}},
		{"workflow validate", func(stdout, stderr *bytes.Buffer) int {
			return workflowSubcommand([]string{"validate", prefixedWorkflowSelector}, stdout, stderr)
		}},
		{"workflow inspect", func(stdout, stderr *bytes.Buffer) int {
			return workflowSubcommand([]string{"inspect", prefixedWorkflowSelector}, stdout, stderr)
		}},
		{"workflow graph inspect", func(stdout, stderr *bytes.Buffer) int {
			return workflowSubcommand([]string{"graph", "inspect", prefixedWorkflowSelector}, stdout, stderr)
		}},
		{"task create --workflow", func(stdout, stderr *bytes.Buffer) int {
			return taskSubcommand([]string{"create", "--project", "project-1", "--title", "task", "--body", "body", "--workflow", prefixedWorkflowSelector}, stdout, stderr)
		}},
		{"task list --workflow", func(stdout, stderr *bytes.Buffer) int {
			return taskSubcommand([]string{"list", "--project", "project-1", "--workflow", prefixedWorkflowSelector}, stdout, stderr)
		}},
	}

	previous := workflowCommandRemoteOpener
	defer func() { workflowCommandRemoteOpener = previous }()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opened := 0
			workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
				opened++
				return config.App{}, nil, nil
			}
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if exitCode := test.run(&stdout, &stderr); exitCode != 2 {
				t.Fatalf("exit code = %d, want 2; stderr=%q", exitCode, stderr.String())
			}
			if got := strings.TrimSpace(stderr.String()); got != "invalid workflow ID" {
				t.Fatalf("stderr = %q, want exact invalid workflow ID", got)
			}
			if opened != 0 {
				t.Fatalf("opened %d remote sessions for invalid selector", opened)
			}
		})
	}
}

type workflowSelectorInventoryRemote struct {
	apicontract.WorkflowService
	expected runtimeids.WorkflowID
	seen     []runtimeids.WorkflowID
}

func (r *workflowSelectorInventoryRemote) record(id runtimeids.WorkflowID) {
	r.seen = append(r.seen, id)
}
func (r *workflowSelectorInventoryRemote) Close() error { return nil }
func (r *workflowSelectorInventoryRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{Binding: &serverapi.ProjectBinding{ProjectID: "project-1"}}, nil
}
func (r *workflowSelectorInventoryRemote) definition() serverapi.WorkflowDefinition {
	return serverapi.WorkflowDefinition{Workflow: serverapi.WorkflowRecord{ID: r.expected, Name: "Workflow", Version: 1}, Nodes: []serverapi.WorkflowNode{{ID: "start", WorkflowID: r.expected, Key: "source", Kind: "agent", DisplayName: "Source"}, {ID: "target", WorkflowID: r.expected, Key: "target", Kind: "terminal", DisplayName: "Target"}}, TransitionGroups: []serverapi.WorkflowTransitionGroup{{ID: "group", WorkflowID: r.expected, SourceNodeID: "start", TransitionID: "next", DisplayName: "Next"}}, Edges: []serverapi.WorkflowEdge{{ID: "edge", WorkflowID: r.expected, TransitionGroupID: "group", Key: "edge", TargetNodeID: "target", AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session", ContextSource: serverapi.WorkflowContextSource{Kind: "immediate_source"}}}}
}
func (r *workflowSelectorInventoryRemote) GetWorkflow(context.Context, serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	r.record(r.expected)
	return serverapi.WorkflowGetResponse{Definition: r.definition()}, nil
}
func (r *workflowSelectorInventoryRemote) PreviewWorkflowDelete(_ context.Context, req serverapi.WorkflowDeletePreviewRequest) (serverapi.WorkflowDeletePreviewResponse, error) {
	r.record(req.WorkflowID)
	return serverapi.WorkflowDeletePreviewResponse{Impact: serverapi.WorkflowDeleteImpact{WorkflowID: req.WorkflowID, Version: 1}}, nil
}

func (r *workflowSelectorInventoryRemote) SaveWorkflowGraph(_ context.Context, req serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error) {
	r.record(req.WorkflowID)
	def := r.definition()
	return serverapi.WorkflowGraphSaveResponse{
		Saved:          true,
		Changed:        true,
		Definition:     &def,
		CurrentVersion: 2,
		Impact:         serverapi.WorkflowGraphSaveImpact{RemovedEntities: []serverapi.WorkflowGraphEntityReference{}},
		Blockers:       []serverapi.WorkflowGraphSaveBlocker{},
		CanSave:        true,
	}, nil
}
func (r *workflowSelectorInventoryRemote) PreviewWorkflowGraphSave(_ context.Context, req serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error) {
	r.record(req.WorkflowID)
	return serverapi.WorkflowGraphSavePreviewResponse{
		CurrentVersion: 1,
		Changed:        true,
		Impact:         serverapi.WorkflowGraphSaveImpact{RemovedEntities: []serverapi.WorkflowGraphEntityReference{}},
		Blockers:       []serverapi.WorkflowGraphSaveBlocker{},
		CanSave:        true,
	}, nil
}
func (r *workflowSelectorInventoryRemote) AddWorkflowNode(_ context.Context, req serverapi.WorkflowNodeAddRequest) (serverapi.WorkflowNodeAddResponse, error) {
	r.record(req.WorkflowID)
	return serverapi.WorkflowNodeAddResponse{Version: 2}, nil
}
func (r *workflowSelectorInventoryRemote) UpdateWorkflowNode(_ context.Context, req serverapi.WorkflowNodeUpdateRequest) (serverapi.WorkflowNodeUpdateResponse, error) {
	r.record(req.WorkflowID)
	return serverapi.WorkflowNodeUpdateResponse{Version: 2}, nil
}
func (r *workflowSelectorInventoryRemote) AddWorkflowTransitionGroup(_ context.Context, req serverapi.WorkflowTransitionGroupAddRequest) (serverapi.WorkflowTransitionGroupAddResponse, error) {
	r.record(req.WorkflowID)
	return serverapi.WorkflowTransitionGroupAddResponse{Version: 2}, nil
}
func (r *workflowSelectorInventoryRemote) AddWorkflowEdge(_ context.Context, req serverapi.WorkflowEdgeAddRequest) (serverapi.WorkflowEdgeAddResponse, error) {
	r.record(req.WorkflowID)
	return serverapi.WorkflowEdgeAddResponse{Version: 2}, nil
}
func (r *workflowSelectorInventoryRemote) UpdateWorkflowEdge(_ context.Context, req serverapi.WorkflowEdgeUpdateRequest) (serverapi.WorkflowEdgeUpdateResponse, error) {
	r.record(req.WorkflowID)
	return serverapi.WorkflowEdgeUpdateResponse{Version: 2}, nil
}
func (r *workflowSelectorInventoryRemote) LinkWorkflowToProject(_ context.Context, req serverapi.WorkflowLinkProjectRequest) (serverapi.WorkflowLinkProjectResponse, error) {
	r.record(req.WorkflowID)
	return serverapi.WorkflowLinkProjectResponse{Link: serverapi.ProjectWorkflowLink{ID: "link", ProjectID: req.ProjectID, WorkflowID: req.WorkflowID}}, nil
}
func (r *workflowSelectorInventoryRemote) ListProjectWorkflowLinks(context.Context, serverapi.WorkflowListProjectLinksRequest) (serverapi.WorkflowListProjectLinksResponse, error) {
	return serverapi.WorkflowListProjectLinksResponse{Links: []serverapi.ProjectWorkflowLink{{ID: "link", ProjectID: "project-1", WorkflowID: r.expected}}}, nil
}
func (r *workflowSelectorInventoryRemote) UnlinkWorkflowFromProject(context.Context, serverapi.WorkflowUnlinkProjectRequest) (serverapi.WorkflowUnlinkProjectResponse, error) {
	r.record(r.expected)
	return serverapi.WorkflowUnlinkProjectResponse{Unlinked: true}, nil
}
func (r *workflowSelectorInventoryRemote) SetDefaultProjectWorkflowLink(_ context.Context, req serverapi.WorkflowSetDefaultProjectLinkRequest) (serverapi.WorkflowSetDefaultProjectLinkResponse, error) {
	r.record(req.WorkflowID)
	return serverapi.WorkflowSetDefaultProjectLinkResponse{Link: serverapi.ProjectWorkflowLink{ID: "link", ProjectID: req.ProjectID, WorkflowID: req.WorkflowID, Default: true}}, nil
}
func (r *workflowSelectorInventoryRemote) ValidateWorkflow(_ context.Context, req serverapi.WorkflowValidateRequest) (serverapi.WorkflowValidateResponse, error) {
	r.record(req.WorkflowID)
	return serverapi.WorkflowValidateResponse{Valid: true}, nil
}
func (r *workflowSelectorInventoryRemote) ListWorkflows(_ context.Context, req serverapi.WorkflowListRequest) (serverapi.WorkflowListResponse, error) {
	if req.WorkflowID != nil {
		r.record(*req.WorkflowID)
	}
	return serverapi.WorkflowListResponse{Workflows: []serverapi.WorkflowRecord{{ID: r.expected, Name: "Workflow", Version: 1}}}, nil
}
func (r *workflowSelectorInventoryRemote) CreateWorkflowTask(_ context.Context, req serverapi.WorkflowTaskCreateRequest) (serverapi.WorkflowTaskCreateResponse, error) {
	if req.WorkflowID != nil {
		r.record(*req.WorkflowID)
	}
	return serverapi.WorkflowTaskCreateResponse{Task: serverapi.WorkflowTaskSummary{ID: "task", ProjectID: req.ProjectID, WorkflowID: r.expected, ShortID: "KNT-1", Title: req.Title}}, nil
}
func (r *workflowSelectorInventoryRemote) GetWorkflowTask(context.Context, serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return serverapi.WorkflowTaskGetResponse{Task: serverapi.WorkflowTaskDetail{Summary: serverapi.WorkflowTaskSummary{ID: "task", ProjectID: "project-1", WorkflowID: r.expected, ShortID: "KNT-1", Title: "Task"}, Workflow: serverapi.WorkflowTaskWorkflowSummary{WorkflowID: r.expected, DisplayName: "Workflow"}, Status: serverapi.WorkflowTaskStatus{Kind: serverapi.WorkflowTaskStatusKindActive, NativeState: serverapi.WorkflowTaskNativeStateActive}}}, nil
}

func (r *workflowSelectorInventoryRemote) ListWorkflowTasks(_ context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	if req.WorkflowID != nil {
		r.record(*req.WorkflowID)
	}
	columns := []string{}
	return serverapi.WorkflowTaskListResponse{Scope: serverapi.WorkflowTaskListScope{ProjectID: "project-1", WorkflowID: req.WorkflowID}, MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne, Tasks: []serverapi.WorkflowTaskListItem{{TaskID: "task", WorkflowID: r.expected, ShortID: "KNT-1", Title: "Task", ColumnKeys: &columns, Status: serverapi.WorkflowTaskStatus{Kind: serverapi.WorkflowTaskStatusKindActive, NativeState: serverapi.WorkflowTaskNativeStateActive}}}}, nil
}

func TestWorkflowSelectorInventoryAcceptsCanonicalIDsAndForwardsTypedIdentity(t *testing.T) {
	selector := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	expected, err := runtimeids.ParseWorkflowID(selector)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		run  func(*bytes.Buffer, *bytes.Buffer) int
	}{
		{"workflow delete", func(o, e *bytes.Buffer) int { return workflowSubcommand([]string{"delete", selector}, o, e) }},
		{"workflow update", func(o, e *bytes.Buffer) int {
			return workflowSubcommand([]string{"update", selector, "--name", "Workflow"}, o, e)
		}},
		{"workflow node add", func(o, e *bytes.Buffer) int {
			return workflowSubcommand([]string{"node", "add", selector, "--key", "new", "--kind", "agent"}, o, e)
		}},
		{"workflow node update", func(o, e *bytes.Buffer) int {
			return workflowSubcommand([]string{"node", "update", selector, "source"}, o, e)
		}},
		{"workflow edge add", func(o, e *bytes.Buffer) int {
			return workflowSubcommand([]string{"edge", "add", selector, "--from", "source", "--transition", "next", "--edge-key", "new", "--to", "target", "--context", "new_session"}, o, e)
		}},
		{"workflow edge update", func(o, e *bytes.Buffer) int {
			return workflowSubcommand([]string{"edge", "update", selector, "edge"}, o, e)
		}},
		{"workflow link", func(o, e *bytes.Buffer) int { return workflowSubcommand([]string{"link", "project-1", selector}, o, e) }},
		{"workflow unlink", func(o, e *bytes.Buffer) int {
			return workflowSubcommand([]string{"unlink", "project-1", selector}, o, e)
		}},
		{"workflow default", func(o, e *bytes.Buffer) int {
			return workflowSubcommand([]string{"default", "project-1", selector}, o, e)
		}},
		{"workflow validate", func(o, e *bytes.Buffer) int { return workflowSubcommand([]string{"validate", selector}, o, e) }},
		{"workflow inspect", func(o, e *bytes.Buffer) int {
			return workflowSubcommand([]string{"inspect", selector, "--summary"}, o, e)
		}},
		{"workflow graph inspect", func(o, e *bytes.Buffer) int {
			return workflowSubcommand([]string{"graph", "inspect", selector}, o, e)
		}},
		{"task create --workflow", func(o, e *bytes.Buffer) int {
			return taskSubcommand([]string{"create", "--project", "project-1", "--title", "Task", "--body", "body", "--workflow", selector}, o, e)
		}},
		{"task list --workflow", func(o, e *bytes.Buffer) int {
			return taskSubcommand([]string{"list", "--project", "project-1", "--workflow", selector}, o, e)
		}},
	}
	previous := workflowCommandRemoteOpener
	defer func() { workflowCommandRemoteOpener = previous }()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote := &workflowSelectorInventoryRemote{expected: expected}
			workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
				return config.App{}, remote, nil
			}
			var stdout, stderr bytes.Buffer
			want := 0
			if tt.name == "workflow delete" {
				want = 1
			}
			if code := tt.run(&stdout, &stderr); code != want {
				t.Fatalf("exit code = %d, want %d; stderr=%q", code, want, stderr.String())
			}
			if len(remote.seen) == 0 || remote.seen[len(remote.seen)-1] != expected {
				t.Fatalf("typed workflow IDs observed = %+v, want %s", remote.seen, expected)
			}
		})
	}
}
