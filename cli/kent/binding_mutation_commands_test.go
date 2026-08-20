package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"core/shared/config"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/serverapi"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestBindingMutationArgumentsAndSelector(t *testing.T) {
	var stderr bytes.Buffer
	args, ok, code := parseBindingMutationArguments(
		"detach",
		detachUsage,
		[]string{"--project", " project-1 ", "--workspace", " workspace-1 ", "--json"},
		&stderr,
	)
	if !ok || code != 0 || stderr.Len() != 0 {
		t.Fatalf("parse=(%+v,%t,%d), stderr=%q", args, ok, code, stderr.String())
	}
	if args.ProjectID != "project-1" || args.WorkspaceID == nil || *args.WorkspaceID != "workspace-1" || !args.JSON {
		t.Fatalf("arguments=%+v", args)
	}
	selector, err := bindingMutationSelector(config.App{}, args)
	if err != nil || selector.WorkspaceIDValue() == nil || *selector.WorkspaceIDValue() != "workspace-1" {
		t.Fatalf("selector=%+v err=%v", selector, err)
	}

	root := filepath.Join(t.TempDir(), "workspace")
	pathSelector, err := bindingMutationSelector(config.App{WorkspaceRoot: root}, bindingMutationArguments{
		ProjectID: "project-1",
		Workspace: ".",
	})
	if err != nil || pathSelector.WorkspaceRootValue() == nil || *pathSelector.WorkspaceRootValue() != root {
		t.Fatalf("path selector=%+v err=%v", pathSelector, err)
	}

	for _, invalid := range [][]string{
		{"."},
		{"--project", " "},
		{"--project", "project-1", "--workspace", " "},
		{"--project", "project-1", "--workspace", "workspace-1", "."},
		{"--project", "project-1", "one", "two"},
	} {
		stderr.Reset()
		if _, ok, code := parseBindingMutationArguments("detach", detachUsage, invalid, &stderr); ok || code != 2 || stderr.Len() == 0 {
			t.Fatalf("args=%q parse=(%t,%d), stderr=%q", invalid, ok, code, stderr.String())
		}
	}
}

func TestBindingMutationResultValidation(t *testing.T) {
	projectID, workspaceID := "project-1", "workspace-1"
	project := validBindingMutationProject()
	valid := []bindingMutationResult{
		{ProjectID: &projectID, WorkspaceID: &workspaceID},
		{Project: project},
	}
	for _, result := range valid {
		if err := result.validate(); err != nil {
			t.Fatalf("valid result %+v rejected: %v", result, err)
		}
	}
	invalid := []bindingMutationResult{
		{},
		{ProjectID: &projectID},
		{ProjectID: &projectID, WorkspaceID: &workspaceID, Project: project},
	}
	for _, result := range invalid {
		if err := result.validate(); err == nil {
			t.Fatalf("invalid result %+v accepted", result)
		}
	}
}

func TestBindingMutationTypedErrorProjection(t *testing.T) {
	count := int32(1)
	blocked, err := newBindingMutationBlockedError(" project-1 ", " workspace-1 ", []*projectpb.WorkspaceUnlinkBlocker{
		{Code: "non_terminal_tasks", Count: &count},
		{Code: "default_workspace"},
	})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := projectWorkspaceMutationErrorProjection(blocked, "ignored", false)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Code != "workspace_detach_blocked" ||
		projection.ProjectID == nil || *projection.ProjectID != "project-1" ||
		projection.WorkspaceID == nil || *projection.WorkspaceID != "workspace-1" ||
		len(projection.Blockers) != 2 ||
		projection.Blockers[0].Count == nil || *projection.Blockers[0].Count != 1 ||
		projection.Blockers[1].Count != nil {
		t.Fatalf("projection=%+v", projection)
	}
	if strings.TrimSpace(projection.Blockers[0].Guidance) == "" ||
		strings.TrimSpace(projection.Blockers[1].Guidance) == "" {
		t.Fatalf("guidance=%+v", projection.Blockers)
	}
	guidance, err := blockerGuidanceFor("default_workspace", "project-1")
	if err != nil ||
		guidance.Kind != blockerGuidanceDefaultWorkspace ||
		guidance.PathCommand == nil ||
		guidance.WorkspaceIDCommand == nil ||
		!slices.Equal(*guidance.PathCommand, []string{
			config.Command, "project", "default", "--project", "project-1", "<replacement-path>",
		}) ||
		!slices.Equal(*guidance.WorkspaceIDCommand, []string{
			config.Command, "project", "default", "--project", "project-1",
			"--workspace", "<replacement-workspace-id>",
		}) {
		t.Fatalf("typed guidance=%+v err=%v", guidance, err)
	}

	retryable, err := projectWorkspaceMutationErrorProjection(&serverapi.WorkspaceDetachConflictError{
		ProjectID: "project-1", WorkspaceID: "workspace-1",
	}, "ignored", false)
	if err != nil || retryable.Code != "workspace_detach_conflict" || retryable.Retryable == nil || !*retryable.Retryable {
		t.Fatalf("retryable=%+v err=%v", retryable, err)
	}

	for input, code := range map[error]string{
		serverapi.ErrProjectNotFound:            "project_not_found",
		serverapi.ErrWorkspaceNotRegistered:     "workspace_not_attached",
		errors.New("unclassified remote error"): "request_failed",
	} {
		got, err := projectWorkspaceMutationErrorProjection(input, "requested-project", false)
		if err != nil || got.Code != code {
			t.Fatalf("input=%v projection=%+v err=%v", input, got, err)
		}
		if code != "request_failed" && (got.ProjectID != nil || got.WorkspaceID != nil) {
			t.Fatalf("unresolved lookup leaked requested identity: %+v", got)
		}
	}
}

func TestBindingMutationJSONShapesAndOutputFailure(t *testing.T) {
	projectID, workspaceID := "project-1", "workspace-1"
	var stdout, stderr bytes.Buffer
	if code := writeBindingMutationEnvelope(&stdout, &stderr, bindingMutationEnvelope{
		Status: "ok",
		Result: &bindingMutationResult{ProjectID: &projectID, WorkspaceID: &workspaceID},
	}); code != 0 {
		t.Fatalf("write exit=%d stderr=%q", code, stderr.String())
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 2 || envelope["status"] == nil || envelope["result"] == nil || envelope["error"] != nil {
		t.Fatalf("envelope=%s", stdout.String())
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(envelope["result"], &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || result["project_id"] == nil || result["workspace_id"] == nil || result["project"] != nil {
		t.Fatalf("result=%s", envelope["result"])
	}

	stdout.Reset()
	project := validBindingMutationProject()
	if code := writeBindingMutationEnvelope(&stdout, &stderr, bindingMutationEnvelope{
		Status: "ok",
		Result: &bindingMutationResult{Project: project},
	}); code != 0 {
		t.Fatalf("write project exit=%d stderr=%q", code, stderr.String())
	}
	var decoded struct {
		Result struct {
			Project map[string]json.RawMessage `json:"project"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if _, present := decoded.Result.Project["default_workflow_id"]; present {
		t.Fatal("absent default_workflow_id was encoded")
	}
	if _, present := decoded.Result.Project["default_workflow_name"]; present {
		t.Fatal("absent default_workflow_name was encoded")
	}

	stderr.Reset()
	if code := writeBindingMutationPlainResult(failingCLIWriter{}, &stderr, bindingMutationResult{
		ProjectID: &projectID, WorkspaceID: &workspaceID,
	}, false); code != 1 || stderr.Len() == 0 {
		t.Fatalf("write failure exit=%d stderr=%q", code, stderr.String())
	}
}

type failingCLIWriter struct{}

func (failingCLIWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func validBindingMutationProject() *projectpb.ProjectHomeSummary {
	updatedAt := timestamppb.New(time.UnixMilli(1))
	return &projectpb.ProjectHomeSummary{
		ProjectId: "project-1", ProjectKey: "PROJECT", DisplayName: "Project",
		PrimaryWorkspace: &projectpb.ProjectHomeWorkspaceSummary{
			WorkspaceId:  "workspace-1",
			DisplayName:  "Workspace",
			RootPath:     "/workspace",
			Availability: projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE,
			UpdatedAt:    updatedAt,
		},
		UpdatedAt: updatedAt,
	}
}
