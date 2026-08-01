package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"core/shared/apicontract"
	"core/shared/serverapi"
)

type taskDependencyCommandRemote struct {
	apicontract.WorkflowService

	tasks          map[string]serverapi.WorkflowTaskDetail
	getRequests    []serverapi.WorkflowTaskGetRequest
	addRequests    []serverapi.WorkflowTaskDependencyAddRequest
	removeRequests []serverapi.WorkflowTaskDependencyRemoveRequest
	listRequests   []serverapi.WorkflowTaskDependencyListRequest
	addResponse    serverapi.WorkflowTaskDependencyAddResponse
	removeResponse serverapi.WorkflowTaskDependencyRemoveResponse
	listResponse   serverapi.WorkflowTaskDependencyListResponse
	getError       map[string]error
}

func (r *taskDependencyCommandRemote) GetWorkflowTask(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	r.getRequests = append(r.getRequests, req)
	key := req.TaskID
	if key == "" {
		key = req.ShortID
	}
	if err := r.getError[key]; err != nil {
		return serverapi.WorkflowTaskGetResponse{}, err
	}
	task, ok := r.tasks[key]
	if !ok {
		return serverapi.WorkflowTaskGetResponse{}, serverapi.ErrWorkflowTaskNotFound
	}
	return serverapi.WorkflowTaskGetResponse{Task: task}, nil
}

func (r *taskDependencyCommandRemote) AddWorkflowTaskDependency(_ context.Context, req serverapi.WorkflowTaskDependencyAddRequest) (serverapi.WorkflowTaskDependencyAddResponse, error) {
	r.addRequests = append(r.addRequests, req)
	return r.addResponse, nil
}

func (r *taskDependencyCommandRemote) RemoveWorkflowTaskDependency(_ context.Context, req serverapi.WorkflowTaskDependencyRemoveRequest) (serverapi.WorkflowTaskDependencyRemoveResponse, error) {
	r.removeRequests = append(r.removeRequests, req)
	return r.removeResponse, nil
}

func (r *taskDependencyCommandRemote) ListWorkflowTaskDependencies(_ context.Context, req serverapi.WorkflowTaskDependencyListRequest) (serverapi.WorkflowTaskDependencyListResponse, error) {
	r.listRequests = append(r.listRequests, req)
	return r.listResponse, nil
}

func (r *taskDependencyCommandRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *taskDependencyCommandRemote) Close() error { return nil }

func taskDependencyTestDetail(id, shortID string) serverapi.WorkflowTaskDetail {
	return serverapi.WorkflowTaskDetail{
		Summary: serverapi.WorkflowTaskSummary{ID: id, ShortID: shortID, ProjectID: "project-1"},
	}
}

func TestTaskDependencyAddResolvesBothSelectorsBeforeMutationAndRendersLockedOutputs(t *testing.T) {
	for _, tt := range []struct {
		name       string
		json       bool
		outcome    serverapi.WorkflowTaskDependencyOutcome
		wantOutput string
	}{
		{name: "plain added", outcome: serverapi.WorkflowTaskDependencyOutcomeAdded, wantOutput: "done\n"},
		{name: "plain idempotent", outcome: serverapi.WorkflowTaskDependencyOutcomeAlreadyPresent, wantOutput: "done\n"},
		{name: "json", json: true, outcome: serverapi.WorkflowTaskDependencyOutcomeAdded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := serverapi.WorkflowTaskDependencyAddResponse{
				Outcome:        tt.outcome,
				BlockerTaskID:  "task-1",
				BlockerShortID: "KENT-1",
				BlockedTaskID:  "task-2",
				BlockedShortID: "KENT-2",
			}
			remote := &taskDependencyCommandRemote{
				tasks: map[string]serverapi.WorkflowTaskDetail{
					"KENT-1": taskDependencyTestDetail("task-1", "KENT-1"),
					"KENT-2": taskDependencyTestDetail("task-2", "KENT-2"),
				},
				addResponse: response,
			}
			installWorkflowCommandRemote(t, remote)

			args := []string{"add", "--blocker", "KENT-1", "--blocked", "KENT-2", "--project", "project-1"}
			if tt.json {
				args = append(args, "--json")
			}
			var stdout, stderr bytes.Buffer
			exitCode := taskDependencySubcommand(args, &stdout, &stderr)

			if exitCode != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
			}
			if len(remote.getRequests) != 2 || len(remote.addRequests) != 1 {
				t.Fatalf("gets=%+v adds=%+v", remote.getRequests, remote.addRequests)
			}
			if got := remote.addRequests[0]; got.BlockerTaskID != "task-1" || got.BlockedTaskID != "task-2" {
				t.Fatalf("add request = %+v", got)
			}
			if !tt.json {
				if stdout.String() != tt.wantOutput {
					t.Fatalf("stdout=%q, want %q", stdout.String(), tt.wantOutput)
				}
				return
			}
			var output map[string]json.RawMessage
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatalf("decode output: %v", err)
			}
			wantKeys := []string{"outcome", "blocker_task_id", "blocker_short_id", "blocked_task_id", "blocked_short_id"}
			if len(output) != len(wantKeys) {
				t.Fatalf("JSON keys=%v", output)
			}
			for _, key := range wantKeys {
				if _, ok := output[key]; !ok {
					t.Fatalf("JSON omitted %q: %s", key, stdout.String())
				}
			}
		})
	}
}

func TestTaskDependencyRemoveDoesNotMutateWhenEitherSelectorFails(t *testing.T) {
	remote := &taskDependencyCommandRemote{
		tasks: map[string]serverapi.WorkflowTaskDetail{
			"KENT-1": taskDependencyTestDetail("task-1", "KENT-1"),
		},
		getError: map[string]error{"KENT-2": errors.New("blocked lookup failed")},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskDependencySubcommand(
		[]string{"remove", "--blocker", "KENT-1", "--blocked", "KENT-2", "--project", "project-1"},
		&stdout,
		&stderr,
	)

	if exitCode != 1 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if len(remote.getRequests) != 2 || len(remote.removeRequests) != 0 {
		t.Fatalf("gets=%+v removes=%+v", remote.getRequests, remote.removeRequests)
	}
}

func TestTaskDependencyListRejectsExplicitBlankDirection(t *testing.T) {
	remote := &taskDependencyCommandRemote{}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskDependencySubcommand(
		[]string{"list", "KENT-2", "--direction", ""},
		&stdout,
		&stderr,
	)

	if exitCode != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if len(remote.getRequests) != 0 || len(remote.listRequests) != 0 {
		t.Fatalf("gets=%+v lists=%+v", remote.getRequests, remote.listRequests)
	}
}

func TestTaskDependencyRemoveRendersIdempotentOutcome(t *testing.T) {
	remote := &taskDependencyCommandRemote{
		tasks: map[string]serverapi.WorkflowTaskDetail{
			"KENT-1": taskDependencyTestDetail("task-1", "KENT-1"),
			"KENT-2": taskDependencyTestDetail("task-2", "KENT-2"),
		},
		removeResponse: serverapi.WorkflowTaskDependencyRemoveResponse{
			Outcome:        serverapi.WorkflowTaskDependencyOutcomeAlreadyAbsent,
			BlockerTaskID:  "task-1",
			BlockerShortID: "KENT-1",
			BlockedTaskID:  "task-2",
			BlockedShortID: "KENT-2",
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskDependencySubcommand(
		[]string{"remove", "--blocker", "KENT-1", "--blocked", "KENT-2", "--project", "project-1", "--json"},
		&stdout,
		&stderr,
	)

	if exitCode != 0 || stderr.Len() != 0 || len(remote.removeRequests) != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q removes=%+v", exitCode, stdout.String(), stderr.String(), remote.removeRequests)
	}
	var output serverapi.WorkflowTaskDependencyMutationResponse
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output.Outcome != serverapi.WorkflowTaskDependencyOutcomeAlreadyAbsent {
		t.Fatalf("outcome=%q", output.Outcome)
	}
}

func TestTaskDependencyAliasesUseCanonicalHandlerButStayHiddenFromTaskHelp(t *testing.T) {
	var canonical bytes.Buffer
	if exitCode := taskSubcommand([]string{"dep", "--help"}, &canonical, &canonical); exitCode != 0 {
		t.Fatalf("canonical help exit=%d output=%q", exitCode, canonical.String())
	}
	for _, alias := range []string{"deps", "dependency", "dependencies"} {
		var output bytes.Buffer
		if exitCode := taskSubcommand([]string{alias, "--help"}, &output, &output); exitCode != 0 {
			t.Fatalf("%s help exit=%d output=%q", alias, exitCode, output.String())
		}
		if output.String() != canonical.String() {
			t.Fatalf("%s help=%q, want canonical %q", alias, output.String(), canonical.String())
		}
	}

	var taskHelp bytes.Buffer
	if exitCode := taskSubcommand([]string{"--help"}, &taskHelp, &taskHelp); exitCode != 0 {
		t.Fatalf("task help exit=%d output=%q", exitCode, taskHelp.String())
	}
	if !strings.Contains(taskHelp.String(), "kent task dep ") {
		t.Fatalf("task help omits canonical group: %q", taskHelp.String())
	}
	for _, hidden := range []string{"kent task deps ", "kent task dependency ", "kent task dependencies "} {
		if strings.Contains(taskHelp.String(), hidden) {
			t.Fatalf("task help advertises hidden alias %q: %q", hidden, taskHelp.String())
		}
	}
}

func TestTaskDependencyListRendersBothDirectionsInServerOrder(t *testing.T) {
	unsatisfied := serverapi.WorkflowTaskDependencyUnsatisfied
	satisfied := serverapi.WorkflowTaskDependencySatisfied
	remote := &taskDependencyCommandRemote{
		tasks: map[string]serverapi.WorkflowTaskDetail{
			"KENT-2": taskDependencyTestDetail("task-2", "KENT-2"),
		},
		listResponse: serverapi.WorkflowTaskDependencyListResponse{
			TaskID:  "task-2",
			ShortID: "KENT-2",
			Directions: []serverapi.WorkflowTaskDependencyListDirectionProjection{
				{
					Direction:  serverapi.WorkflowTaskDependencyDirectionBlocks,
					TotalCount: 1,
					Items: []serverapi.WorkflowTaskDependencyItem{{
						TaskID:     "task-4",
						ShortID:    "KENT-4",
						Title:      "Ship follow-up",
						WorkflowID: "workflow-1",
						Status:     taskDependencyTestStatus(serverapi.WorkflowTaskStatusKindBacklog),
					}},
				},
				{
					Direction:        serverapi.WorkflowTaskDependencyDirectionBlockedBy,
					TotalCount:       2,
					UnsatisfiedCount: intTestPointer(1),
					Items: []serverapi.WorkflowTaskDependencyItem{
						{
							TaskID:       "task-1",
							ShortID:      "KENT-1",
							Title:        "Build foundation",
							WorkflowID:   "workflow-1",
							Status:       taskDependencyTestStatus(serverapi.WorkflowTaskStatusKindActive),
							Satisfaction: &unsatisfied,
						},
						{
							TaskID:       "task-3",
							ShortID:      "KENT-3",
							Title:        "Approve design",
							WorkflowID:   "workflow-1",
							Status:       taskDependencyTestStatus(serverapi.WorkflowTaskStatusKindDone),
							Satisfaction: &satisfied,
						},
					},
				},
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskDependencySubcommand(
		[]string{"list", "KENT-2", "--project", "project-1"},
		&stdout,
		&stderr,
	)

	const want = "Blocks 1 tasks:\nKENT-4: Ship follow-up (backlog)\nBlocked by:\nKENT-1: Build foundation (active)\nKENT-3: Approve design (done)\n"
	if exitCode != 0 || stderr.Len() != 0 || stdout.String() != want {
		t.Fatalf("exit=%d stdout=%q want=%q stderr=%q", exitCode, stdout.String(), want, stderr.String())
	}
	if len(remote.listRequests) != 1 || remote.listRequests[0].Direction != nil {
		t.Fatalf("list requests=%+v", remote.listRequests)
	}
}

func TestTaskDependencyListDirectionJSONUsesFocusedEnvelope(t *testing.T) {
	remote := &taskDependencyCommandRemote{
		tasks: map[string]serverapi.WorkflowTaskDetail{
			"KENT-2": taskDependencyTestDetail("task-2", "KENT-2"),
		},
		listResponse: serverapi.WorkflowTaskDependencyListResponse{
			TaskID:     "task-2",
			ShortID:    "KENT-2",
			Directions: []serverapi.WorkflowTaskDependencyListDirectionProjection{},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskDependencySubcommand(
		[]string{"list", "KENT-2", "--direction", "blocked-by", "--project", "project-1", "--json"},
		&stdout,
		&stderr,
	)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if len(remote.listRequests) != 1 || remote.listRequests[0].Direction == nil ||
		*remote.listRequests[0].Direction != serverapi.WorkflowTaskDependencyDirectionBlockedBy {
		t.Fatalf("list requests=%+v", remote.listRequests)
	}
	var output map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(output) != 3 || output["task_id"] == nil || output["short_id"] == nil || output["directions"] == nil {
		t.Fatalf("JSON output=%s", stdout.String())
	}
	if bytes.Contains(stdout.Bytes(), []byte("add_availability")) {
		t.Fatalf("JSON exposes detail-only availability: %s", stdout.String())
	}
}

func TestTaskDependencyHumanRendererOmitsEmptyDirections(t *testing.T) {
	var output bytes.Buffer
	if err := writeTaskDependencyDirections(&output, []serverapi.WorkflowTaskDependencyListDirectionProjection{}); err != nil {
		t.Fatalf("render empty directions: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("empty directions output=%q", output.String())
	}
}

func taskDependencyTestStatus(kind serverapi.WorkflowTaskStatusKind) serverapi.WorkflowTaskStatus {
	native, ok := kind.NativeState()
	if !ok {
		panic("invalid test status")
	}
	return serverapi.WorkflowTaskStatus{Kind: kind, NativeState: native}
}

func intTestPointer(value int) *int { return &value }
