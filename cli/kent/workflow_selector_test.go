package main

import (
	"bytes"
	"context"
	"testing"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
)

const workflowSelectorTestUUID = "7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"

func TestParseWorkflowSelectorConvertsCanonicalUUIDv4ToPersistedID(t *testing.T) {
	selector, err := parseWorkflowSelector(workflowSelectorTestUUID)
	if err != nil {
		t.Fatalf("parseWorkflowSelector: %v", err)
	}
	if got := selector.PersistedID(); got != "workflow-"+workflowSelectorTestUUID {
		t.Fatalf("PersistedID = %q, want workflow-prefixed selector", got)
	}
}

func TestParseWorkflowSelectorRejectsEveryNonCanonicalPublicForm(t *testing.T) {
	for _, raw := range []string{
		"",
		" ",
		"workflow-" + workflowSelectorTestUUID,
		"Workflow Name",
		" " + workflowSelectorTestUUID,
		workflowSelectorTestUUID + " ",
		"7E8D24D2-8A98-4DCF-A197-6214DB1CB3C0",
		"7e8d24d2-8a98-1dcf-a197-6214db1cb3c0",
	} {
		if _, err := parseWorkflowSelector(raw); err == nil {
			t.Fatalf("parseWorkflowSelector(%q) succeeded", raw)
		}
	}
}

func TestWorkflowRecordForCLIProjectsBareIDWithoutMutatingServerRecord(t *testing.T) {
	serverRecord := serverapi.WorkflowRecord{
		ID:      "workflow-" + workflowSelectorTestUUID,
		Name:    "Workflow",
		Version: 3,
	}
	projected, err := workflowRecordForCLI(serverRecord)
	if err != nil {
		t.Fatalf("workflowRecordForCLI: %v", err)
	}
	if projected.ID != workflowSelectorTestUUID {
		t.Fatalf("projected id = %q, want bare UUID", projected.ID)
	}
	if serverRecord.ID != "workflow-"+workflowSelectorTestUUID {
		t.Fatalf("server record mutated to %q", serverRecord.ID)
	}
}

func TestWorkflowRecordForCLIFailsMalformedPersistedID(t *testing.T) {
	if _, err := workflowRecordForCLI(serverapi.WorkflowRecord{ID: workflowSelectorTestUUID}); err == nil {
		t.Fatal("workflowRecordForCLI accepted a bare server workflow id")
	}
}

func TestWorkflowDefinitionForCLIProjectsEveryWorkflowIdentity(t *testing.T) {
	persistedID := "workflow-" + workflowSelectorTestUUID
	serverDefinition := serverapi.WorkflowDefinition{
		Workflow:         serverapi.WorkflowRecord{ID: persistedID},
		NodeGroups:       []serverapi.WorkflowNodeGroup{{WorkflowID: persistedID}},
		Nodes:            []serverapi.WorkflowNode{{WorkflowID: persistedID}},
		TransitionGroups: []serverapi.WorkflowTransitionGroup{{WorkflowID: persistedID}},
		Edges:            []serverapi.WorkflowEdge{{WorkflowID: persistedID}},
		DerivedWiring: serverapi.WorkflowDerivedWiring{
			Diagnostics: []serverapi.WorkflowValidationError{{WorkflowID: &persistedID}},
		},
	}
	projected, err := workflowDefinitionForCLI(serverDefinition)
	if err != nil {
		t.Fatalf("workflowDefinitionForCLI: %v", err)
	}
	if projected.DerivedWiring.Diagnostics[0].WorkflowID == nil {
		t.Fatal("projected diagnostic workflow id is absent")
	}
	for label, got := range map[string]string{
		"record":           projected.Workflow.ID,
		"node group":       projected.NodeGroups[0].WorkflowID,
		"node":             projected.Nodes[0].WorkflowID,
		"transition group": projected.TransitionGroups[0].WorkflowID,
		"edge":             projected.Edges[0].WorkflowID,
		"diagnostic":       *projected.DerivedWiring.Diagnostics[0].WorkflowID,
	} {
		if got != workflowSelectorTestUUID {
			t.Fatalf("%s workflow id = %q, want bare UUID", label, got)
		}
	}
	if serverDefinition.Nodes[0].WorkflowID != persistedID {
		t.Fatal("workflowDefinitionForCLI mutated server definition")
	}
}

func TestProjectWorkflowLinkForCLIProjectsBareWorkflowID(t *testing.T) {
	serverLink := serverapi.ProjectWorkflowLink{
		ID:         "link-1",
		ProjectID:  "project-1",
		WorkflowID: "workflow-" + workflowSelectorTestUUID,
		Default:    true,
	}
	projected, err := projectWorkflowLinkForCLI(serverLink)
	if err != nil {
		t.Fatalf("projectWorkflowLinkForCLI: %v", err)
	}
	if projected.WorkflowID != workflowSelectorTestUUID {
		t.Fatalf("projected workflow id = %q, want bare UUID", projected.WorkflowID)
	}
	if serverLink.WorkflowID != "workflow-"+workflowSelectorTestUUID {
		t.Fatal("projectWorkflowLinkForCLI mutated server link")
	}
}

func TestWorkflowValidationForCLIProjectsWorkflowIDs(t *testing.T) {
	persistedID := "workflow-" + workflowSelectorTestUUID
	serverResponse := serverapi.WorkflowValidateResponse{
		Errors: []serverapi.WorkflowValidationError{{WorkflowID: &persistedID}, {}},
	}
	projected, err := workflowValidationForCLI(serverResponse)
	if err != nil {
		t.Fatalf("workflowValidationForCLI: %v", err)
	}
	if projected.Errors[0].WorkflowID == nil || *projected.Errors[0].WorkflowID != workflowSelectorTestUUID {
		t.Fatalf("projected workflow id = %v, want bare UUID", projected.Errors[0].WorkflowID)
	}
	if serverResponse.Errors[0].WorkflowID == nil || *serverResponse.Errors[0].WorkflowID != persistedID {
		t.Fatal("workflowValidationForCLI mutated server response")
	}
	if projected.Errors[1].WorkflowID != nil {
		t.Fatalf("absent projected workflow id = %v, want nil", projected.Errors[1].WorkflowID)
	}
}

func TestWorkflowTaskDetailForCLIProjectsSummaryAndPickerWorkflowIDs(t *testing.T) {
	persistedID := "workflow-" + workflowSelectorTestUUID
	serverDetail := serverapi.WorkflowTaskDetail{
		Summary: serverapi.WorkflowTaskSummary{WorkflowID: persistedID},
		Workflow: serverapi.WorkflowPickerItem{
			WorkflowID:       persistedID,
			ValidationErrors: []serverapi.WorkflowValidationError{{WorkflowID: &persistedID}, {}},
		},
	}
	projected, err := workflowTaskDetailForCLI(serverDetail)
	if err != nil {
		t.Fatalf("workflowTaskDetailForCLI: %v", err)
	}
	if projected.Workflow.ValidationErrors[0].WorkflowID == nil {
		t.Fatal("projected optional workflow identity is absent")
	}
	for label, got := range map[string]string{
		"summary":    projected.Summary.WorkflowID,
		"picker":     projected.Workflow.WorkflowID,
		"validation": *projected.Workflow.ValidationErrors[0].WorkflowID,
	} {
		if got != workflowSelectorTestUUID {
			t.Fatalf("%s workflow id = %q, want bare UUID", label, got)
		}
	}
	if serverDetail.Summary.WorkflowID != persistedID {
		t.Fatal("workflowTaskDetailForCLI mutated server detail")
	}
	if projected.Workflow.ValidationErrors[1].WorkflowID != nil {
		t.Fatal("absent optional workflow identities became present")
	}
}

func TestWorkflowProjectionRejectsPresentBlankOptionalWorkflowIdentity(t *testing.T) {
	blank := ""
	if _, err := workflowValidationForCLI(serverapi.WorkflowValidateResponse{
		Errors: []serverapi.WorkflowValidationError{{WorkflowID: &blank}},
	}); err == nil {
		t.Fatal("workflowValidationForCLI accepted a present blank workflow identity")
	}
}

func TestTaskCreateRejectsInvalidWorkflowSelectorBeforeOpeningRemote(t *testing.T) {
	called := false
	original := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		called = true
		return config.App{}, nil, nil
	}
	defer func() { workflowCommandRemoteOpener = original }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := taskCreateSubcommand([]string{
		"--project", "project-1",
		"--workflow", "workflow-" + workflowSelectorTestUUID,
		"--title", "Task",
	}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("task create code=%d stderr=%q, want usage failure", code, stderr.String())
	}
	if called {
		t.Fatal("task create opened remote for invalid workflow selector")
	}
}

func TestWorkflowValidateConvertsSelectorBeforeRemoteCall(t *testing.T) {
	remote := &selectorCapturingRemote{}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
	defer restore()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := workflowValidateSubcommand([]string{workflowSelectorTestUUID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("workflow validate code=%d stderr=%q", code, stderr.String())
	}
	if remote.validateCalls != 1 || remote.workflowID != "workflow-"+workflowSelectorTestUUID {
		t.Fatalf("validate calls=%d workflowID=%q", remote.validateCalls, remote.workflowID)
	}
}

func TestWorkflowCommandRejectsInvalidSelectorBeforeOpeningRemote(t *testing.T) {
	called := false
	original := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		called = true
		return config.App{}, nil, nil
	}
	defer func() { workflowCommandRemoteOpener = original }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := workflowValidateSubcommand([]string{"Workflow Name"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("workflow validate code=%d stderr=%q, want usage failure", code, stderr.String())
	}
	if called {
		t.Fatal("workflow validate opened remote for invalid workflow selector")
	}
}

func TestWorkflowListFailsMalformedServerWorkflowID(t *testing.T) {
	remote := &pagedWorkflowListRemote{
		pages: map[string]serverapi.WorkflowListResponse{
			"": {Workflows: []serverapi.WorkflowRecord{{ID: "workflow-malformed"}}},
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
	defer restore()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := workflowListSubcommand([]string{"--json"}, &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("workflow list code=%d stdout=%q stderr=%q, want operator-visible projection failure", code, stdout.String(), stderr.String())
	}
}

type selectorCapturingRemote struct {
	apicontract.WorkflowService
	validateCalls int
	workflowID    string
}

func (r *selectorCapturingRemote) Close() error { return nil }

func (r *selectorCapturingRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *selectorCapturingRemote) ValidateWorkflow(_ context.Context, req serverapi.WorkflowValidateRequest) (serverapi.WorkflowValidateResponse, error) {
	r.validateCalls++
	r.workflowID = req.WorkflowID
	return serverapi.WorkflowValidateResponse{Valid: true}, nil
}
