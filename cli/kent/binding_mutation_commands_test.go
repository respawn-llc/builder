package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
)

func TestBindingMutationArgumentParsing(t *testing.T) {
	t.Run("defaults to current workspace path", func(t *testing.T) {
		var stderr bytes.Buffer
		arguments, ok, exitCode := parseBindingMutationArguments(
			"detach",
			detachUsage,
			[]string{"--project", "project-1"},
			&stderr,
		)
		if !ok || exitCode != 0 {
			t.Fatalf("parse = (%+v, %t, %d), stderr=%q", arguments, ok, exitCode, stderr.String())
		}
		if arguments.ProjectID != "project-1" || arguments.Workspace != "." || arguments.WorkspaceID != nil {
			t.Fatalf("arguments = %+v, want project and current-path selector", arguments)
		}
	})

	t.Run("accepts workspace ID", func(t *testing.T) {
		var stderr bytes.Buffer
		arguments, ok, exitCode := parseBindingMutationArguments(
			"project default",
			projectDefaultUsage,
			[]string{"--project", "project-1", "--workspace", "workspace-1", "--json"},
			&stderr,
		)
		if !ok || exitCode != 0 || arguments.WorkspaceID == nil || *arguments.WorkspaceID != "workspace-1" || !arguments.JSON {
			t.Fatalf("parse = (%+v, %t, %d), stderr=%q", arguments, ok, exitCode, stderr.String())
		}
	})

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing project", args: []string{"."}},
		{name: "blank project", args: []string{"--project", " "}},
		{name: "blank workspace ID", args: []string{"--project", "project-1", "--workspace", " "}},
		{name: "both selectors", args: []string{"--project", "project-1", "--workspace", "workspace-1", "."}},
		{name: "too many paths", args: []string{"--project", "project-1", "one", "two"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			_, ok, exitCode := parseBindingMutationArguments("detach", detachUsage, test.args, &stderr)
			if ok || exitCode != 2 || strings.TrimSpace(stderr.String()) == "" {
				t.Fatalf("parse = (%t, %d), stderr=%q; want usage failure", ok, exitCode, stderr.String())
			}
		})
	}
}

func TestBindingMutationSelectorNormalizesPathAfterOpeningCurrentWorkspace(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	arguments := bindingMutationArguments{
		ProjectID: "project-1",
		Workspace: ".",
	}
	selector, err := bindingMutationSelector(config.App{WorkspaceRoot: workspaceRoot}, arguments)
	if err != nil {
		t.Fatalf("build selector: %v", err)
	}
	root, selectedByRoot := selector.WorkspaceRootValue()
	if !selectedByRoot || root != workspaceRoot || !filepath.IsAbs(root) {
		t.Fatalf("selector = %+v, want absolute opened workspace root", selector)
	}
}

func TestBindingMutationResultValidationRejectsMixedAndMissingIdentity(t *testing.T) {
	projectID := "project-1"
	workspaceID := "workspace-1"
	project := serverapi.ProjectHomeSummary{ProjectID: projectID}
	for name, result := range map[string]bindingMutationResult{
		"missing detach identity": {},
		"mixed result":            {ProjectID: &projectID, WorkspaceID: &workspaceID, Project: &project},
	} {
		t.Run(name, func(t *testing.T) {
			if err := result.validate(); err == nil {
				t.Fatal("validate succeeded; want structural error")
			}
		})
	}
	if err := (bindingMutationResult{ProjectID: &projectID, WorkspaceID: &workspaceID}).validate(); err != nil {
		t.Fatalf("valid detach result rejected: %v", err)
	}
	if err := (bindingMutationResult{Project: &project}).validate(); err != nil {
		t.Fatalf("valid default result rejected: %v", err)
	}
}

func TestProjectDefaultUsageValidationDoesNotOpenRemote(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := projectDefaultSubcommand([]string{"."}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want usage error 2; stderr=%q", code, stderr.String())
	}
}

func TestDetachUsageValidationDoesNotOpenRemote(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := detachSubcommand([]string{"."}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want usage error 2; stderr=%q", code, stderr.String())
	}
}

func TestProjectWorkspaceMutationErrorProjection(t *testing.T) {
	t.Run("project and workspace lookup failures omit requested identity", func(t *testing.T) {
		for _, test := range []struct {
			name string
			err  error
			code string
		}{
			{name: "project", err: serverapi.ErrProjectNotFound, code: "project_not_found"},
			{name: "workspace", err: serverapi.ErrWorkspaceNotRegistered, code: "workspace_not_attached"},
		} {
			t.Run(test.name, func(t *testing.T) {
				projection := projectWorkspaceMutationErrorProjection(test.err, "requested-project", false)
				if projection.Code != test.code || projection.ProjectID != nil || projection.WorkspaceID != nil {
					t.Fatalf("projection = %+v, want %s without identity", projection, test.code)
				}
			})
		}
	})

	t.Run("path identity failure adds workspace fallback only", func(t *testing.T) {
		err := serverapi.WorkspacePathIdentityError{WorkspaceRoot: "/missing", Cause: errors.New("unavailable")}
		projection := projectWorkspaceMutationErrorProjection(err, "project-1", false)
		if projection.Code != "request_failed" || projection.Remediation == nil || projection.Remediation.Kind != "use_workspace_id" {
			t.Fatalf("projection = %+v, want use_workspace_id remediation", projection)
		}
		generic := projectWorkspaceMutationErrorProjection(errors.New("remote failed"), "project-1", false)
		if generic.Remediation != nil {
			t.Fatalf("generic projection = %+v, want no remediation", generic)
		}
	})

	t.Run("resolved mutation failure carries authoritative identity", func(t *testing.T) {
		err := &serverapi.WorkspaceMutationError{
			ProjectID:   "resolved-project",
			WorkspaceID: "resolved-workspace",
			Cause:       errors.New("write failed"),
		}
		projection := projectWorkspaceMutationErrorProjection(err, "requested-project", false)
		if projection.ProjectID == nil || projection.WorkspaceID == nil ||
			*projection.ProjectID != "resolved-project" || *projection.WorkspaceID != "resolved-workspace" {
			t.Fatalf("projection = %+v, want carried identity", projection)
		}
	})

	t.Run("detach conflict is retryable", func(t *testing.T) {
		err := &serverapi.WorkspaceDetachConflictError{
			ProjectID:   "project-1",
			WorkspaceID: "workspace-1",
		}
		projection := projectWorkspaceMutationErrorProjection(err, "project-1", false)
		if projection.Code != "workspace_detach_conflict" || projection.Retryable == nil || !*projection.Retryable {
			t.Fatalf("projection = %+v, want retryable detach conflict", projection)
		}
	})
}

func TestDetachBlockerProjectionIncludesAllBlockersAndPositiveCounts(t *testing.T) {
	err, constructionErr := newBindingMutationBlockedError("project-1", "workspace-1", []serverapi.ProjectWorkspaceUnlinkBlocker{
		{Code: "default_workspace", Message: "default", Count: 1},
		{Code: "active_sessions", Message: "active", Count: 0},
	})
	if constructionErr != nil {
		t.Fatalf("construct blocked error: %v", constructionErr)
	}
	projection := projectWorkspaceMutationErrorProjection(err, "project-1", false)
	if projection.Code != "workspace_detach_blocked" || len(projection.Blockers) != 2 {
		t.Fatalf("projection = %+v, want two blockers", projection)
	}
	if projection.Blockers[0].Count == nil || *projection.Blockers[0].Count != 1 {
		t.Fatalf("positive blocker count = %+v, want 1", projection.Blockers[0].Count)
	}
	if projection.Blockers[1].Count != nil {
		t.Fatalf("zero blocker count = %+v, want omitted", projection.Blockers[1].Count)
	}
	if strings.TrimSpace(projection.Blockers[0].Guidance) == "" || strings.TrimSpace(projection.Blockers[1].Guidance) == "" {
		t.Fatalf("blocker guidance = %+v, want non-blank guidance", projection.Blockers)
	}
}

func TestBlockerGuidanceUsesTypedActions(t *testing.T) {
	defaultGuidance := blockerGuidanceFor("default_workspace", "project-1")
	if defaultGuidance.Kind != blockerGuidanceDefaultWorkspace || defaultGuidance.Command == nil {
		t.Fatalf("default guidance = %+v, want command action", defaultGuidance)
	}
	unknownGuidance := blockerGuidanceFor("future_code", "project-1")
	if unknownGuidance.Kind != blockerGuidanceUnknown || unknownGuidance.Command != nil {
		t.Fatalf("unknown guidance = %+v, want commandless unknown action", unknownGuidance)
	}
}

func TestBindingMutationEnvelopeOmitsOptionalSuccessFields(t *testing.T) {
	projectID := "project-1"
	workspaceID := "workspace-1"
	envelope := bindingMutationEnvelope{
		Status: "ok",
		Result: &bindingMutationResult{ProjectID: &projectID, WorkspaceID: &workspaceID},
	}
	var stdout, stderr bytes.Buffer
	if code := writeBindingMutationEnvelope(&stdout, &stderr, envelope); code != 0 {
		t.Fatalf("write envelope exit code = %d, stderr=%q", code, stderr.String())
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if _, present := decoded["error"]; present {
		t.Fatalf("success envelope contains error: %s", stdout.String())
	}
}

func TestProjectDefaultJSONSuccessOmitsAbsentWorkflowFields(t *testing.T) {
	envelope := bindingMutationEnvelope{
		Status: "ok",
		Result: &bindingMutationResult{
			Project: &serverapi.ProjectHomeSummary{
				ProjectID:   "project-1",
				DisplayName: "Authoritative",
			},
		},
	}
	var stdout, stderr bytes.Buffer
	if code := writeBindingMutationEnvelope(&stdout, &stderr, envelope); code != 0 {
		t.Fatalf("write default envelope exit code = %d, stderr=%q", code, stderr.String())
	}
	var decoded struct {
		Result struct {
			Project map[string]json.RawMessage `json:"project"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode default envelope: %v", err)
	}
	if _, present := decoded.Result.Project["default_workflow_id"]; present {
		t.Fatal("default JSON encoded absent default_workflow_id")
	}
	if _, present := decoded.Result.Project["default_workflow_name"]; present {
		t.Fatal("default JSON encoded absent default_workflow_name")
	}
}
