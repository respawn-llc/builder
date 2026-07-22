package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
)

func TestTaskLabelListReturnsProjectCatalogJSON(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	created, err := remote.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Priority",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}

	stdout, stderr, code := runRootCommand("task", "label", "list", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("task label list exit=%d stderr=%q", code, stderr)
	}
	var output serverapi.WorkflowProjectLabelCatalogResponse
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("task label list JSON = %q: %v", stdout, err)
	}
	if output.Catalog.ProjectID != binding.ProjectID ||
		len(output.Catalog.Labels) != 1 ||
		output.Catalog.Labels[0] != created.Label {
		t.Fatalf("task label list output = %+v, want project catalog with %+v", output, created.Label)
	}
}

func TestTaskLabelListUsesExplicitProjectScope(t *testing.T) {
	cfg, _, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	other, err := remote.metadataStore.CreateProjectForWorkspace(context.Background(), t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	created, err := remote.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: other.ProjectID,
		Name:      "Other",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}

	stdout, stderr, code := runRootCommand("task", "label", "list", "--project", other.ProjectID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("task label list exit=%d stderr=%q", code, stderr)
	}
	var output serverapi.WorkflowProjectLabelCatalogResponse
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("task label list JSON = %q: %v", stdout, err)
	}
	if output.Catalog.ProjectID != other.ProjectID ||
		len(output.Catalog.Labels) != 1 ||
		output.Catalog.Labels[0] != created.Label {
		t.Fatalf("task label list output = %+v, want explicit project catalog", output)
	}
}

func TestTaskLabelCreateReturnsServerLabelJSON(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, stderr, code := runRootCommand("task", "label", "create", "--project", binding.ProjectID, "--json", "Priority")
	if code != 0 || stderr != "" {
		t.Fatalf("task label create exit=%d stderr=%q", code, stderr)
	}
	var output serverapi.WorkflowProjectLabelCreateResponse
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("task label create JSON = %q: %v", stdout, err)
	}
	if output.Label.ID == "" || output.Label.Name != "Priority" {
		t.Fatalf("task label create output = %+v, want created Priority record", output)
	}
	catalog, err := remote.ListWorkflowProjectLabels(context.Background(), serverapi.WorkflowProjectLabelCatalogRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("ListWorkflowProjectLabels: %v", err)
	}
	if len(catalog.Catalog.Labels) != 1 || catalog.Catalog.Labels[0] != output.Label {
		t.Fatalf("catalog after create = %+v, want created label %+v", catalog.Catalog.Labels, output.Label)
	}
}

func TestTaskLabelCreatePreservesTypedServerError(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	if _, err := remote.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Priority",
	}); err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}
	expectedErr := (&serverapi.WorkflowLabelError{
		Reason:    serverapi.WorkflowLabelErrorReasonNameConflict,
		ProjectID: &binding.ProjectID,
	}).Error()

	stdout, stderr, code := runRootCommand("task", "label", "create", "--project", binding.ProjectID, "--json", "Priority")
	if code != 1 || stdout != "" {
		t.Fatalf("task label create exit=%d stdout=%q, want typed server failure without JSON", code, stdout)
	}
	if stderr != expectedErr+"\n" {
		t.Fatalf("task label create stderr = %q, want unchanged typed server error %q", stderr, expectedErr+"\n")
	}
}

func TestTaskLabelRenameResolvesCaseFoldedName(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	created, err := remote.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Priority",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}

	stdout, stderr, code := runRootCommand("task", "label", "rename", "--project", binding.ProjectID, "--label", "pRiOrItY", "--json", "Urgent")
	if code != 0 || stderr != "" {
		t.Fatalf("task label rename exit=%d stderr=%q", code, stderr)
	}
	var output serverapi.WorkflowProjectLabelRenameResponse
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("task label rename JSON = %q: %v", stdout, err)
	}
	if output.Label.ID != created.Label.ID || output.Label.Name != "Urgent" {
		t.Fatalf("task label rename output = %+v, want stable ID and renamed record", output)
	}
}

func TestTaskLabelDeleteUsesCanonicalUUIDWithoutConfirmation(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	created, err := remote.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Obsolete",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}

	stdout, stderr, code := runRootCommand("task", "label", "delete", "--project", binding.ProjectID, "--label", created.Label.ID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("task label delete exit=%d stderr=%q", code, stderr)
	}
	var output serverapi.WorkflowProjectLabelDeleteResponse
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("task label delete JSON = %q: %v", stdout, err)
	}
	if output.LabelID != created.Label.ID {
		t.Fatalf("task label delete output = %+v, want deleted ID %q", output, created.Label.ID)
	}
	catalog, err := remote.ListWorkflowProjectLabels(context.Background(), serverapi.WorkflowProjectLabelCatalogRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("ListWorkflowProjectLabels: %v", err)
	}
	if len(catalog.Catalog.Labels) != 0 {
		t.Fatalf("catalog after delete = %+v, want empty", catalog.Catalog.Labels)
	}
}

func TestTaskLabelRenameTreatsWhitespacePaddedUUIDAsALabelName(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	const uuidShapedName = "7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	created, err := remote.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      uuidShapedName,
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}

	stdout, stderr, code := runRootCommand("task", "label", "rename", "--project", binding.ProjectID, "--label", " "+uuidShapedName+" ", "--json", "Named UUID")
	if code != 0 || stderr != "" {
		t.Fatalf("task label rename exit=%d stderr=%q", code, stderr)
	}
	var output serverapi.WorkflowProjectLabelRenameResponse
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("task label rename JSON = %q: %v", stdout, err)
	}
	if output.Label.ID != created.Label.ID || output.Label.Name != "Named UUID" {
		t.Fatalf("task label rename output = %+v, want whitespace-padded UUID-shaped name selector", output)
	}
}

func TestTaskLabelRenameTreatsCommaContainingSelectorAsOneLiteralName(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	created, err := remote.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "needs, review",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}

	stdout, stderr, code := runRootCommand("task", "label", "rename", "--project", binding.ProjectID, "--label", "needs, review", "--json", "Reviewed")
	if code != 0 || stderr != "" {
		t.Fatalf("task label rename exit=%d stderr=%q", code, stderr)
	}
	var output serverapi.WorkflowProjectLabelRenameResponse
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("task label rename JSON = %q: %v", stdout, err)
	}
	if output.Label.ID != created.Label.ID || output.Label.Name != "Reviewed" {
		t.Fatalf("task label rename output = %+v, want literal comma selector", output)
	}
}

func TestTaskLabelListRejectsDuplicateFoldedCatalogNames(t *testing.T) {
	cfg, binding, loopback := newWorkflowCommandLoopback(t)
	remote := &taskLabelCatalogResponseRemote{
		workflowCommandRemote: loopback,
		response: serverapi.WorkflowProjectLabelCatalogResponse{
			Catalog: serverapi.WorkflowProjectLabelCatalog{
				ProjectID: binding.ProjectID,
				Labels: []serverapi.WorkflowProjectLabel{
					{ID: "11111111-1111-4111-8111-111111111111", Name: "Priority"},
					{ID: "22222222-2222-4222-8222-222222222222", Name: "priority"},
				},
			},
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, _, code := runRootCommand("task", "label", "list", "--project", binding.ProjectID, "--json")
	if code != 1 || stdout != "" {
		t.Fatalf("task label list exit=%d stdout=%q, want catalog conflict failure without JSON", code, stdout)
	}
}

func TestTaskLabelListRejectsMismatchedCatalogProject(t *testing.T) {
	cfg, binding, loopback := newWorkflowCommandLoopback(t)
	remote := &taskLabelCatalogResponseRemote{
		workflowCommandRemote: loopback,
		response: serverapi.WorkflowProjectLabelCatalogResponse{
			Catalog: serverapi.WorkflowProjectLabelCatalog{
				ProjectID: "project-other",
				Labels: []serverapi.WorkflowProjectLabel{{
					ID:   "11111111-1111-4111-8111-111111111111",
					Name: "Priority",
				}},
			},
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, _, code := runRootCommand("task", "label", "list", "--project", binding.ProjectID, "--json")
	if code != 1 || stdout != "" {
		t.Fatalf("task label list exit=%d stdout=%q, want scope mismatch failure without JSON", code, stdout)
	}
}

type taskLabelCatalogResponseRemote struct {
	workflowCommandRemote
	response serverapi.WorkflowProjectLabelCatalogResponse
}

func (r *taskLabelCatalogResponseRemote) ListWorkflowProjectLabels(context.Context, serverapi.WorkflowProjectLabelCatalogRequest) (serverapi.WorkflowProjectLabelCatalogResponse, error) {
	return r.response, nil
}

func TestTaskLabelRenameRejectsMismatchedResponseIdentity(t *testing.T) {
	cfg, binding, loopback := newWorkflowCommandLoopback(t)
	created, err := loopback.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Priority",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}
	remote := &taskLabelMutationResponseRemote{
		workflowCommandRemote: loopback,
		renameResponse: serverapi.WorkflowProjectLabelRenameResponse{
			Label: serverapi.WorkflowProjectLabel{
				ID:   "11111111-1111-4111-8111-111111111111",
				Name: "Urgent",
			},
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, _, code := runRootCommand("task", "label", "rename", "--project", binding.ProjectID, "--label", created.Label.ID, "--json", "Urgent")
	if code != 1 || stdout != "" {
		t.Fatalf("task label rename exit=%d stdout=%q, want identity mismatch failure without JSON", code, stdout)
	}
	if remote.renameCalls != 1 {
		t.Fatalf("RenameWorkflowProjectLabel calls = %d, want 1", remote.renameCalls)
	}
}

func TestTaskLabelDeleteRejectsMismatchedResponseIdentity(t *testing.T) {
	cfg, binding, loopback := newWorkflowCommandLoopback(t)
	created, err := loopback.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Priority",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}
	remote := &taskLabelMutationResponseRemote{
		workflowCommandRemote: loopback,
		deleteResponse: serverapi.WorkflowProjectLabelDeleteResponse{
			LabelID: "11111111-1111-4111-8111-111111111111",
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, _, code := runRootCommand("task", "label", "delete", "--project", binding.ProjectID, "--label", created.Label.ID, "--json")
	if code != 1 || stdout != "" {
		t.Fatalf("task label delete exit=%d stdout=%q, want identity mismatch failure without JSON", code, stdout)
	}
	if remote.deleteCalls != 1 {
		t.Fatalf("DeleteWorkflowProjectLabel calls = %d, want 1", remote.deleteCalls)
	}
}

func TestTaskLabelRenameRejectsUnresolvedSelectorBeforeMutation(t *testing.T) {
	cfg, binding, loopback := newWorkflowCommandLoopback(t)
	remote := &taskLabelMutationResponseRemote{workflowCommandRemote: loopback}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, stderr, code := runRootCommand("task", "label", "rename", "--project", binding.ProjectID, "--label", "missing", "--json", "Urgent")
	if code != 1 || stdout != "" {
		t.Fatalf("task label rename exit=%d stdout=%q, want unresolved selector failure without JSON", code, stdout)
	}
	if !strings.Contains(stderr, "missing") {
		t.Fatalf("task label rename stderr = %q, want unresolved selector identified", stderr)
	}
	if remote.renameCalls != 0 {
		t.Fatalf("RenameWorkflowProjectLabel calls = %d, want no mutation", remote.renameCalls)
	}
}

func TestTaskLabelHelpRoutesAndNoTopLevelLabelCommand(t *testing.T) {
	for _, args := range [][]string{
		{"task", "label", "--help"},
		{"task", "label", "add", "--help"},
		{"task", "label", "create", "--help"},
		{"task", "label", "list", "--help"},
		{"task", "label", "remove", "--help"},
		{"task", "label", "rename", "--help"},
		{"task", "label", "delete", "--help"},
	} {
		if _, stderr, code := runRootCommand(args...); code != 0 {
			t.Fatalf("%q help exit=%d stderr=%q", args, code, stderr)
		}
	}
	if _, _, code := runRootCommand("label", "--help"); code != 2 {
		t.Fatalf("kent label exit=%d, want unknown top-level command", code)
	}
}

func TestTaskLabelAddAppliesResolvedBatchWithOneUpdate(t *testing.T) {
	cfg, binding, loopback := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, loopback)
	defer restore()

	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Assignment Workflow")
	taskOut, taskErr, code := runRootCommand("task", "create", "--title", "Labeled task", "--body", "Body", "--workflow", workflowID, "--project", binding.ProjectID)
	if code != 0 {
		t.Fatalf("task create exit=%d stderr=%q", code, taskErr)
	}
	shortID := taskDetailHeadingShortID(t, taskOut)
	task, err := loopback.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{
		ProjectID: binding.ProjectID,
		ShortID:   shortID,
	})
	if err != nil {
		t.Fatalf("GetWorkflowTask: %v", err)
	}
	priority, err := loopback.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Priority",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel Priority: %v", err)
	}
	urgent, err := loopback.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Urgent",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel Urgent: %v", err)
	}
	remote := &taskLabelMutationResponseRemote{workflowCommandRemote: loopback}
	restoreCaptured := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restoreCaptured()

	stdout, stderr, code := runRootCommand(
		"task", "label", "add", shortID,
		"--project", binding.ProjectID,
		"--label", "pRiOrItY",
		"--label", priority.Label.ID,
		"--label", urgent.Label.ID,
		"--json",
	)
	if code != 0 || stderr != "" {
		t.Fatalf("task label add exit=%d stderr=%q", code, stderr)
	}
	var output serverapi.WorkflowTaskLabelsUpdateResponse
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("task label add JSON = %q: %v", stdout, err)
	}
	wantIDs := []string{priority.Label.ID, urgent.Label.ID}
	if output.Assignment.TaskID != task.Task.Summary.ID || !equalLabelIDMembers(output.Assignment.LabelIDs, wantIDs) {
		t.Fatalf("task label add output = %+v, want task %q labels %v", output, task.Task.Summary.ID, wantIDs)
	}
	if len(remote.updateRequests) != 1 ||
		!equalLabelIDMembers(remote.updateRequests[0].AddLabelIDs, wantIDs) ||
		len(remote.updateRequests[0].RemoveLabelIDs) != 0 {
		t.Fatalf("UpdateWorkflowTaskLabels requests = %+v, want one add-only request for %v", remote.updateRequests, wantIDs)
	}
	persisted, err := loopback.GetWorkflowTaskLabels(context.Background(), serverapi.WorkflowTaskLabelsGetRequest{TaskID: task.Task.Summary.ID})
	if err != nil {
		t.Fatalf("GetWorkflowTaskLabels: %v", err)
	}
	if !equalLabelIDMembers(persisted.Assignment.LabelIDs, wantIDs) {
		t.Fatalf("persisted assignment = %+v, want %v", persisted.Assignment, wantIDs)
	}
}

func TestTaskLabelAssignmentRequiresSelectorBeforeOpeningRemote(t *testing.T) {
	for _, command := range []string{"add", "remove"} {
		t.Run(command, func(t *testing.T) {
			opened := false
			original := workflowCommandRemoteOpener
			workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
				opened = true
				return config.App{}, nil, nil
			}
			defer func() { workflowCommandRemoteOpener = original }()

			_, _, code := runRootCommand("task", "label", command, "task-1")
			if code != 2 {
				t.Fatalf("task label %s exit=%d, want usage failure", command, code)
			}
			if opened {
				t.Fatalf("task label %s opened remote without a label selector", command)
			}
		})
	}
}

func TestTaskLabelRemoveIsIdempotentAndPersistsAuthoritativeAssignments(t *testing.T) {
	cfg, binding, loopback := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, loopback)
	defer restore()

	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Removal Workflow")
	taskOut, taskErr, code := runRootCommand("task", "create", "--title", "Labeled task", "--body", "Body", "--workflow", workflowID, "--project", binding.ProjectID)
	if code != 0 {
		t.Fatalf("task create exit=%d stderr=%q", code, taskErr)
	}
	shortID := taskDetailHeadingShortID(t, taskOut)
	task, err := loopback.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{
		ProjectID: binding.ProjectID,
		ShortID:   shortID,
	})
	if err != nil {
		t.Fatalf("GetWorkflowTask: %v", err)
	}
	priority, err := loopback.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Priority",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel Priority: %v", err)
	}
	urgent, err := loopback.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Urgent",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel Urgent: %v", err)
	}
	if _, err := loopback.UpdateWorkflowTaskLabels(context.Background(), serverapi.WorkflowTaskLabelsUpdateRequest{
		TaskID:      task.Task.Summary.ID,
		AddLabelIDs: []string{priority.Label.ID, urgent.Label.ID},
	}); err != nil {
		t.Fatalf("seed task labels: %v", err)
	}
	remote := &taskLabelMutationResponseRemote{workflowCommandRemote: loopback}
	restoreCaptured := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restoreCaptured()

	stdout, stderr, code := runRootCommand(
		"task", "label", "remove", shortID,
		"--project", binding.ProjectID,
		"--label", "priority",
		"--label", urgent.Label.ID,
		"--json",
	)
	if code != 0 || stderr != "" {
		t.Fatalf("task label remove exit=%d stderr=%q", code, stderr)
	}
	var output serverapi.WorkflowTaskLabelsUpdateResponse
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("task label remove JSON = %q: %v", stdout, err)
	}
	if output.Assignment.TaskID != task.Task.Summary.ID || len(output.Assignment.LabelIDs) != 0 {
		t.Fatalf("task label remove output = %+v, want empty assignment for task %q", output, task.Task.Summary.ID)
	}
	humanOut, humanErr, humanCode := runRootCommand(
		"task", "label", "remove", shortID,
		"--project", binding.ProjectID,
		"--label", priority.Label.ID,
		"--label", urgent.Label.ID,
	)
	if humanCode != 0 || humanErr != "" || humanOut == "" {
		t.Fatalf("idempotent task label remove exit=%d stdout=%q stderr=%q", humanCode, humanOut, humanErr)
	}
	if len(remote.updateRequests) != 2 ||
		len(remote.updateRequests[0].AddLabelIDs) != 0 ||
		!equalLabelIDMembers(remote.updateRequests[0].RemoveLabelIDs, []string{priority.Label.ID, urgent.Label.ID}) ||
		len(remote.updateRequests[1].AddLabelIDs) != 0 ||
		!equalLabelIDMembers(remote.updateRequests[1].RemoveLabelIDs, []string{priority.Label.ID, urgent.Label.ID}) {
		t.Fatalf("UpdateWorkflowTaskLabels requests = %+v, want one remove-only request per command", remote.updateRequests)
	}
	persisted, err := loopback.GetWorkflowTaskLabels(context.Background(), serverapi.WorkflowTaskLabelsGetRequest{TaskID: task.Task.Summary.ID})
	if err != nil {
		t.Fatalf("GetWorkflowTaskLabels: %v", err)
	}
	if len(persisted.Assignment.LabelIDs) != 0 {
		t.Fatalf("persisted assignment = %+v, want empty", persisted.Assignment)
	}
}

func TestTaskLabelAddAggregatesUnresolvedSelectorsBeforeMutation(t *testing.T) {
	cfg, binding, loopback := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, loopback)
	defer restore()

	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Unresolved Label Workflow")
	taskOut, taskErr, code := runRootCommand("task", "create", "--title", "Unlabeled task", "--body", "Body", "--workflow", workflowID, "--project", binding.ProjectID)
	if code != 0 {
		t.Fatalf("task create exit=%d stderr=%q", code, taskErr)
	}
	shortID := taskDetailHeadingShortID(t, taskOut)
	task, err := loopback.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{
		ProjectID: binding.ProjectID,
		ShortID:   shortID,
	})
	if err != nil {
		t.Fatalf("GetWorkflowTask: %v", err)
	}
	if _, err := loopback.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Priority",
	}); err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}
	remote := &taskLabelMutationResponseRemote{workflowCommandRemote: loopback}
	restoreCaptured := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restoreCaptured()

	const firstMissing = "missing, one"
	const secondMissing = "missing two"
	stdout, stderr, code := runRootCommand(
		"task", "label", "add", shortID,
		"--project", binding.ProjectID,
		"--label", "Priority",
		"--label", firstMissing,
		"--label", secondMissing,
		"--json",
	)
	if code != 1 || stdout != "" {
		t.Fatalf("task label add exit=%d stdout=%q, want unresolved batch failure without JSON", code, stdout)
	}
	firstIndex := strings.Index(stderr, firstMissing)
	secondIndex := strings.Index(stderr, secondMissing)
	if firstIndex < 0 || secondIndex < 0 || firstIndex > secondIndex {
		t.Fatalf("task label add stderr = %q, want unresolved selectors in input order", stderr)
	}
	if len(remote.updateRequests) != 0 {
		t.Fatalf("UpdateWorkflowTaskLabels requests = %+v, want no mutation for unresolved batch", remote.updateRequests)
	}
	persisted, err := loopback.GetWorkflowTaskLabels(context.Background(), serverapi.WorkflowTaskLabelsGetRequest{TaskID: task.Task.Summary.ID})
	if err != nil {
		t.Fatalf("GetWorkflowTaskLabels: %v", err)
	}
	if len(persisted.Assignment.LabelIDs) != 0 {
		t.Fatalf("persisted assignment = %+v, want no partial mutation", persisted.Assignment)
	}
}

func TestTaskLabelAddFullTaskIDUsesActualTaskProjectForLabelResolution(t *testing.T) {
	cfg, binding, loopback := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, loopback)
	defer restore()

	other, err := loopback.metadataStore.CreateProjectForWorkspace(context.Background(), t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	workflowID := setupLinkedWorkflow(t, other.ProjectID, "Other Assignment Workflow")
	taskOut, taskErr, code := runRootCommand("task", "create", "--title", "Other task", "--body", "Body", "--workflow", workflowID, "--project", other.ProjectID)
	if code != 0 {
		t.Fatalf("task create exit=%d stderr=%q", code, taskErr)
	}
	shortID := taskDetailHeadingShortID(t, taskOut)
	task, err := loopback.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{
		ProjectID: other.ProjectID,
		ShortID:   shortID,
	})
	if err != nil {
		t.Fatalf("GetWorkflowTask: %v", err)
	}
	otherLabel, err := loopback.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: other.ProjectID,
		Name:      "Other",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}

	stdout, stderr, code := runRootCommand(
		"task", "label", "add", task.Task.Summary.ID,
		"--project", binding.ProjectID,
		"--label", otherLabel.Label.Name,
		"--json",
	)
	if code != 0 || stderr != "" {
		t.Fatalf("task label add exit=%d stderr=%q", code, stderr)
	}
	var output serverapi.WorkflowTaskLabelsUpdateResponse
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("task label add JSON = %q: %v", stdout, err)
	}
	if output.Assignment.TaskID != task.Task.Summary.ID || !equalLabelIDMembers(output.Assignment.LabelIDs, []string{otherLabel.Label.ID}) {
		t.Fatalf("task label add output = %+v, want other Project assignment", output)
	}
}

func TestTaskLabelAddRejectsMismatchedResponseTaskID(t *testing.T) {
	cfg, binding, loopback := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, loopback)
	defer restore()

	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Mismatched Assignment Workflow")
	taskOut, taskErr, code := runRootCommand("task", "create", "--title", "Task", "--body", "Body", "--workflow", workflowID, "--project", binding.ProjectID)
	if code != 0 {
		t.Fatalf("task create exit=%d stderr=%q", code, taskErr)
	}
	shortID := taskDetailHeadingShortID(t, taskOut)
	label, err := loopback.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Priority",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}
	remote := &taskLabelMutationResponseRemote{
		workflowCommandRemote: loopback,
		updateResponse: &serverapi.WorkflowTaskLabelsUpdateResponse{
			Assignment: serverapi.WorkflowTaskAssignedLabelIDs{
				TaskID:   "task-other",
				LabelIDs: []string{},
			},
		},
	}
	restoreCaptured := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restoreCaptured()

	stdout, _, code := runRootCommand(
		"task", "label", "add", shortID,
		"--project", binding.ProjectID,
		"--label", label.Label.ID,
		"--json",
	)
	if code != 1 || stdout != "" {
		t.Fatalf("task label add exit=%d stdout=%q, want task-ID mismatch failure without JSON", code, stdout)
	}
	if len(remote.updateRequests) != 1 {
		t.Fatalf("UpdateWorkflowTaskLabels requests = %+v, want one request", remote.updateRequests)
	}
}

type taskLabelMutationResponseRemote struct {
	workflowCommandRemote
	renameResponse serverapi.WorkflowProjectLabelRenameResponse
	deleteResponse serverapi.WorkflowProjectLabelDeleteResponse
	renameCalls    int
	deleteCalls    int
	updateRequests []serverapi.WorkflowTaskLabelsUpdateRequest
	updateResponse *serverapi.WorkflowTaskLabelsUpdateResponse
}

func (r *taskLabelMutationResponseRemote) RenameWorkflowProjectLabel(context.Context, serverapi.WorkflowProjectLabelRenameRequest) (serverapi.WorkflowProjectLabelRenameResponse, error) {
	r.renameCalls++
	return r.renameResponse, nil
}

func (r *taskLabelMutationResponseRemote) DeleteWorkflowProjectLabel(context.Context, serverapi.WorkflowProjectLabelDeleteRequest) (serverapi.WorkflowProjectLabelDeleteResponse, error) {
	r.deleteCalls++
	return r.deleteResponse, nil
}

func (r *taskLabelMutationResponseRemote) UpdateWorkflowTaskLabels(ctx context.Context, req serverapi.WorkflowTaskLabelsUpdateRequest) (serverapi.WorkflowTaskLabelsUpdateResponse, error) {
	r.updateRequests = append(r.updateRequests, req)
	if r.updateResponse != nil {
		return *r.updateResponse, nil
	}
	return r.workflowCommandRemote.UpdateWorkflowTaskLabels(ctx, req)
}

func equalLabelIDMembers(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	gotMembers := make(map[string]struct{}, len(got))
	for _, id := range got {
		gotMembers[id] = struct{}{}
	}
	if len(gotMembers) != len(got) {
		return false
	}
	for _, id := range want {
		if _, exists := gotMembers[id]; !exists {
			return false
		}
	}
	return true
}
