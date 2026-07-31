package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"core/shared/client"
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
	project := validProjectHomeSummaryForBindingMutationTest()
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

func validProjectHomeSummaryForBindingMutationTest() serverapi.ProjectHomeSummary {
	return serverapi.ProjectHomeSummary{
		ProjectID:   "project-1",
		ProjectKey:  "project",
		DisplayName: "Authoritative",
		PrimaryWorkspace: serverapi.ProjectWorkspaceSummary{
			WorkspaceID:  "workspace-1",
			DisplayName:  "Workspace",
			RootPath:     "/workspace",
			Availability: "available",
		},
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

func TestDetachUnexpectedIncompleteResponseEmitsJSONFailure(t *testing.T) {
	originalOpener := bindingCommandRemoteOpener
	bindingCommandRemoteOpener = func(context.Context, string) (config.App, *client.Remote, error) {
		return config.App{}, nil, nil
	}
	t.Cleanup(func() {
		bindingCommandRemoteOpener = originalOpener
	})

	workspaceID := "workspace-1"
	arguments := bindingMutationArguments{
		ProjectID:   "project-1",
		WorkspaceID: &workspaceID,
		JSON:        true,
	}
	var stdout, stderr bytes.Buffer
	code := runBindingMutationCommand(
		arguments,
		&stdout,
		&stderr,
		func(context.Context, *client.Remote, serverapi.ProjectWorkspaceSelector) (bindingMutationResult, error) {
			return bindingMutationResultFromDetachResponse(serverapi.ProjectWorkspaceUnlinkResponse{})
		},
		false,
	)
	if code != 1 {
		t.Fatalf("command exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var envelope struct {
		Status string `json:"status"`
		Error  struct {
			Code        string `json:"code"`
			Message     string `json:"message"`
			ProjectID   string `json:"project_id"`
			WorkspaceID string `json:"workspace_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode failure envelope: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if envelope.Status != "error" || envelope.Error.Code != "request_failed" {
		t.Fatalf("envelope = %+v, want request_failed error", envelope)
	}
	if envelope.Error.ProjectID != "" || envelope.Error.WorkspaceID != "" {
		t.Fatalf("envelope = %+v, want no resolved identity", envelope)
	}
}

func TestProjectDefaultMalformedResponseEmitsJSONFailure(t *testing.T) {
	assertBindingMutationJSONFailure(t, func(context.Context, *client.Remote, serverapi.ProjectWorkspaceSelector) (bindingMutationResult, error) {
		return bindingMutationResult{Project: &serverapi.ProjectHomeSummary{}}, nil
	}, true)
}

func TestProjectDefaultBlankWorkflowFieldsEmitJSONFailure(t *testing.T) {
	t.Run("blank workflow ID", func(t *testing.T) {
		project := validProjectHomeSummaryForBindingMutationTest()
		blank := ""
		name := "Workflow"
		project.DefaultWorkflowID = &blank
		project.DefaultWorkflowName = &name
		project.DefaultWorkflowValid = true
		assertBindingMutationJSONFailure(t, func(context.Context, *client.Remote, serverapi.ProjectWorkspaceSelector) (bindingMutationResult, error) {
			return bindingMutationResult{Project: &project}, nil
		}, true)
	})
	t.Run("blank workflow name", func(t *testing.T) {
		project := validProjectHomeSummaryForBindingMutationTest()
		id := "workflow-1"
		blank := ""
		project.DefaultWorkflowID = &id
		project.DefaultWorkflowName = &blank
		project.DefaultWorkflowValid = true
		assertBindingMutationJSONFailure(t, func(context.Context, *client.Remote, serverapi.ProjectWorkspaceSelector) (bindingMutationResult, error) {
			return bindingMutationResult{Project: &project}, nil
		}, true)
	})
}

func TestDetachMalformedBlockerResponseEmitsJSONFailure(t *testing.T) {
	assertBindingMutationJSONFailure(t, func(context.Context, *client.Remote, serverapi.ProjectWorkspaceSelector) (bindingMutationResult, error) {
		return bindingMutationResultFromDetachResponse(serverapi.ProjectWorkspaceUnlinkResponse{
			ProjectID:   "project-1",
			WorkspaceID: "workspace-1",
			Blockers: []serverapi.ProjectWorkspaceUnlinkBlocker{{
				Message: "malformed blocker",
			}},
		})
	}, false)
}

func assertBindingMutationJSONFailure(
	t *testing.T,
	mutate func(context.Context, *client.Remote, serverapi.ProjectWorkspaceSelector) (bindingMutationResult, error),
	defaultMutation bool,
) {
	t.Helper()
	originalOpener := bindingCommandRemoteOpener
	bindingCommandRemoteOpener = func(context.Context, string) (config.App, *client.Remote, error) {
		return config.App{}, nil, nil
	}
	t.Cleanup(func() {
		bindingCommandRemoteOpener = originalOpener
	})

	workspaceID := "workspace-1"
	arguments := bindingMutationArguments{
		ProjectID:   "project-1",
		WorkspaceID: &workspaceID,
		JSON:        true,
	}
	var stdout, stderr bytes.Buffer
	code := runBindingMutationCommand(arguments, &stdout, &stderr, mutate, defaultMutation)
	if code != 1 {
		t.Fatalf("command exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var envelope struct {
		Status string `json:"status"`
		Error  struct {
			Code        string  `json:"code"`
			ProjectID   *string `json:"project_id"`
			WorkspaceID *string `json:"workspace_id"`
		} `json:"error"`
	}
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode failure envelope: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout contains more than one JSON object: extra=%v err=%v output=%q", extra, err, stdout.String())
	}
	if envelope.Status != "error" || envelope.Error.Code != "request_failed" {
		t.Fatalf("envelope = %+v, want request_failed error", envelope)
	}
	if envelope.Error.ProjectID != nil || envelope.Error.WorkspaceID != nil {
		t.Fatalf("envelope = %+v, want no resolved identity", envelope)
	}
}

type bindingMutationFailingWriter struct{}

func (bindingMutationFailingWriter) Write([]byte) (int, error) {
	return 0, errors.New("output write failed")
}

func TestBindingMutationPlainOutputFailureReturnsNonZero(t *testing.T) {
	projectID := "project-1"
	workspaceID := "workspace-1"
	var stderr bytes.Buffer
	code := writeBindingMutationPlainResult(
		bindingMutationFailingWriter{},
		&stderr,
		bindingMutationResult{ProjectID: &projectID, WorkspaceID: &workspaceID},
		false,
	)
	if code != 1 {
		t.Fatalf("plain output exit code = %d, want 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "output write failed") {
		t.Fatalf("stderr = %q, want output write failure", stderr.String())
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
				projection, err := projectWorkspaceMutationErrorProjection(test.err, "requested-project", false)
				if err != nil {
					t.Fatalf("project projection: %v", err)
				}
				if projection.Code != test.code || projection.ProjectID != nil || projection.WorkspaceID != nil {
					t.Fatalf("projection = %+v, want %s without identity", projection, test.code)
				}
			})
		}
	})

	t.Run("path identity failure adds workspace fallback only", func(t *testing.T) {
		err := serverapi.WorkspacePathIdentityError{WorkspaceRoot: "/missing", Cause: errors.New("unavailable")}
		projection, projectionErr := projectWorkspaceMutationErrorProjection(err, "project-1", false)
		if projectionErr != nil {
			t.Fatalf("project path identity projection: %v", projectionErr)
		}
		encoded, marshalErr := json.Marshal(projection)
		if marshalErr != nil {
			t.Fatalf("marshal projection: %v", marshalErr)
		}
		if projection.Code != "request_failed" || strings.Contains(string(encoded), "remediation") {
			t.Fatalf("projection = %+v, want request_failed without remediation field", projection)
		}
		if !strings.Contains(projection.Message, "--workspace <workspace-id>") {
			t.Fatalf("projection message = %q, want workspace-ID fallback guidance", projection.Message)
		}
		generic, projectionErr := projectWorkspaceMutationErrorProjection(errors.New("remote failed"), "project-1", false)
		if projectionErr != nil {
			t.Fatalf("project generic projection: %v", projectionErr)
		}
		genericEncoded, marshalErr := json.Marshal(generic)
		if marshalErr != nil {
			t.Fatalf("marshal generic projection: %v", marshalErr)
		}
		if strings.Contains(string(genericEncoded), "remediation") {
			t.Fatalf("generic projection = %+v, want no remediation field", generic)
		}
	})

	t.Run("resolved mutation failure carries authoritative identity", func(t *testing.T) {
		err := &serverapi.WorkspaceMutationError{
			ProjectID:   "resolved-project",
			WorkspaceID: "resolved-workspace",
			Cause:       errors.New("write failed"),
		}
		projection, projectionErr := projectWorkspaceMutationErrorProjection(err, "requested-project", false)
		if projectionErr != nil {
			t.Fatalf("project mutation projection: %v", projectionErr)
		}
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
		projection, projectionErr := projectWorkspaceMutationErrorProjection(err, "project-1", false)
		if projectionErr != nil {
			t.Fatalf("project conflict projection: %v", projectionErr)
		}
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
	projection, projectionErr := projectWorkspaceMutationErrorProjection(err, "project-1", false)
	if projectionErr != nil {
		t.Fatalf("project blocker projection: %v", projectionErr)
	}
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
	if !strings.Contains(projection.Blockers[0].Guidance, "--workspace <replacement-workspace-id>") {
		t.Fatalf("default-workspace guidance = %q, want workspace-ID remediation", projection.Blockers[0].Guidance)
	}
}

func TestBlockerGuidanceUsesTypedActions(t *testing.T) {
	defaultGuidance, err := blockerGuidanceFor("default_workspace", "project-1")
	if err != nil {
		t.Fatalf("default guidance: %v", err)
	}
	if defaultGuidance.Kind != blockerGuidanceDefaultWorkspace ||
		defaultGuidance.PathCommand == nil ||
		defaultGuidance.WorkspaceIDCommand == nil {
		t.Fatalf("default guidance = %+v, want path and workspace-ID actions", defaultGuidance)
	}
	if got := strings.Join(*defaultGuidance.PathCommand, " "); got != "kent project default --project project-1 <replacement-path>" {
		t.Fatalf("default path command = %q, want typed path command", got)
	}
	if got := strings.Join(*defaultGuidance.WorkspaceIDCommand, " "); got != "kent project default --project project-1 --workspace <replacement-workspace-id>" {
		t.Fatalf("default workspace-ID command = %q, want typed workspace-ID command", got)
	}
	unknownGuidance, err := blockerGuidanceFor("future_code", "project-1")
	if err != nil {
		t.Fatalf("unknown guidance: %v", err)
	}
	if unknownGuidance.Kind != blockerGuidanceUnknown ||
		unknownGuidance.PathCommand != nil ||
		unknownGuidance.WorkspaceIDCommand != nil {
		t.Fatalf("unknown guidance = %+v, want commandless unknown action", unknownGuidance)
	}
	if _, err := blockerGuidanceFor("default_workspace", " "); err == nil {
		t.Fatal("blank project ID accepted for default-workspace guidance")
	}
}

func TestBindingMutationEnvelopeProjectsExactDetachSuccess(t *testing.T) {
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
	if len(decoded) != 2 {
		t.Fatalf("success envelope keys = %v, want exactly status and result", decoded)
	}
	if _, present := decoded["status"]; !present {
		t.Fatalf("success envelope = %s, want status", stdout.String())
	}
	if _, present := decoded["result"]; !present {
		t.Fatalf("success envelope = %s, want result", stdout.String())
	}
	if _, present := decoded["error"]; present {
		t.Fatalf("success envelope contains error: %s", stdout.String())
	}
	var status string
	if err := json.Unmarshal(decoded["status"], &status); err != nil || status != "ok" {
		t.Fatalf("status = %q, want ok", status)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(decoded["result"], &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("detach result keys = %v, want exactly project_id and workspace_id", result)
	}
	if _, present := result["project_id"]; !present {
		t.Fatalf("detach result = %s, want project_id", stdout.String())
	}
	if _, present := result["workspace_id"]; !present {
		t.Fatalf("detach result = %s, want workspace_id", stdout.String())
	}
	var gotProjectID, gotWorkspaceID string
	if err := json.Unmarshal(result["project_id"], &gotProjectID); err != nil {
		t.Fatalf("decode result project_id: %v", err)
	}
	if err := json.Unmarshal(result["workspace_id"], &gotWorkspaceID); err != nil {
		t.Fatalf("decode result workspace_id: %v", err)
	}
	if gotProjectID != projectID || gotWorkspaceID != workspaceID {
		t.Fatalf("detach result IDs = %q/%q, want %q/%q", gotProjectID, gotWorkspaceID, projectID, workspaceID)
	}
}

func TestProjectDefaultJSONSuccessOmitsAbsentWorkflowFields(t *testing.T) {
	envelope := bindingMutationEnvelope{
		Status: "ok",
		Result: &bindingMutationResult{
			Project: &serverapi.ProjectHomeSummary{
				ProjectID:   "project-1",
				ProjectKey:  "project",
				DisplayName: "Authoritative",
				PrimaryWorkspace: serverapi.ProjectWorkspaceSummary{
					WorkspaceID:  "workspace-1",
					DisplayName:  "Workspace",
					RootPath:     "/workspace",
					Availability: "available",
				},
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
