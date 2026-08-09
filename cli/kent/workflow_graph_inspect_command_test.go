package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const workflowGraphInspectSelector = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

var workflowGraphInspectID = func() runtimeids.WorkflowID {
	id, err := runtimeids.ParseWorkflowID(workflowGraphInspectSelector)
	if err != nil {
		panic(err)
	}
	return id
}()

type workflowGraphInspectRemote struct {
	apicontract.WorkflowService
	definition  serverapi.WorkflowDefinition
	getError    error
	closeError  error
	getRequests []serverapi.WorkflowGetRequest
	closeCalls  int
}

func (r *workflowGraphInspectRemote) GetWorkflow(_ context.Context, req serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	r.getRequests = append(r.getRequests, req)
	return serverapi.WorkflowGetResponse{Definition: r.definition}, r.getError
}

func (*workflowGraphInspectRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *workflowGraphInspectRemote) Close() error {
	r.closeCalls++
	return r.closeError
}

func TestWorkflowGraphInspectDispatchesHelpWithoutOpeningRemote(t *testing.T) {
	previous := workflowCommandRemoteOpener
	defer func() { workflowCommandRemoteOpener = previous }()
	opened := 0
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		opened++
		return config.App{}, nil, nil
	}

	var stdout, stderr bytes.Buffer
	if exitCode := workflowSubcommand([]string{"graph", "inspect", "--help"}, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if opened != 0 {
		t.Fatalf("remote opened %d times for help", opened)
	}
	if stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("help streams stdout=%q stderr=%q, want usage only on stderr", stdout.String(), stderr.String())
	}
}

func TestWorkflowGraphInspectWritesOneDeterministicDocument(t *testing.T) {
	remote := &workflowGraphInspectRemote{definition: workflowGraphInspectDefinition(workflowGraphInspectID)}
	installWorkflowCommandRemote(t, remote)

	var first, second bytes.Buffer
	for index, stdout := range []*bytes.Buffer{&first, &second} {
		var stderr bytes.Buffer
		if exitCode := workflowSubcommand([]string{"graph", "inspect", workflowGraphInspectSelector}, stdout, &stderr); exitCode != 0 {
			t.Fatalf("run %d exit code = %d, want 0; stderr=%q", index+1, exitCode, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("run %d stderr = %q, want empty", index+1, stderr.String())
		}
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("graph inspect output is not deterministic\nfirst: %s\nsecond: %s", first.Bytes(), second.Bytes())
	}
	decoder := json.NewDecoder(bytes.NewReader(first.Bytes()))
	var document workflowGraphDocument
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode graph inspect document: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("graph inspect emitted more than one JSON document: extra=%v err=%v", extra, err)
	}
	if document.WorkflowID != workflowGraphInspectID || document.ExpectedVersion != 7 {
		t.Fatalf("document identity/version = %s/%d, want %s/7", document.WorkflowID, document.ExpectedVersion, workflowGraphInspectID)
	}
	requireWorkflowGraphIDs(t, document.Graph.Nodes, func(node workflowGraphDocumentNode) string { return node.ID }, []string{"node-alpha", "node-zeta"})
	if len(remote.getRequests) != 2 || remote.getRequests[0].WorkflowID != workflowGraphInspectID || remote.getRequests[1].WorkflowID != workflowGraphInspectID {
		t.Fatalf("GetWorkflow requests = %+v, want selected Workflow twice", remote.getRequests)
	}
	if remote.closeCalls != 2 {
		t.Fatalf("Close calls = %d, want 2", remote.closeCalls)
	}
}

func TestWorkflowGraphInspectRejectsInvalidUsageBeforeOpeningRemote(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing selector", args: []string{"graph", "inspect"}},
		{name: "extra selector", args: []string{"graph", "inspect", workflowGraphInspectSelector, workflowGraphInspectSelector}},
		{name: "json flag", args: []string{"graph", "inspect", workflowGraphInspectSelector, "--json"}},
		{name: "prefixed selector", args: []string{"graph", "inspect", "workflow-" + workflowGraphInspectSelector}},
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
			var stdout, stderr bytes.Buffer
			if exitCode := workflowSubcommand(test.args, &stdout, &stderr); exitCode != 2 {
				t.Fatalf("exit code = %d, want 2; stderr=%q", exitCode, stderr.String())
			}
			if opened != 0 {
				t.Fatalf("remote opened %d times for invalid usage", opened)
			}
			if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("invalid usage streams stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestWorkflowGraphInspectReportsRemoteAndIdentityFailures(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		previous := workflowCommandRemoteOpener
		defer func() { workflowCommandRemoteOpener = previous }()
		workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
			return config.App{}, nil, errors.New("open failed")
		}
		var stdout, stderr bytes.Buffer
		if exitCode := workflowSubcommand([]string{"graph", "inspect", workflowGraphInspectSelector}, &stdout, &stderr); exitCode != 1 {
			t.Fatalf("exit code = %d, want 1", exitCode)
		}
		if stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("open failure streams stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	})

	tests := []struct {
		name   string
		remote *workflowGraphInspectRemote
	}{
		{name: "get", remote: &workflowGraphInspectRemote{getError: errors.New("get failed")}},
		{name: "mismatched identity", remote: &workflowGraphInspectRemote{definition: workflowGraphInspectDefinition(runtimeids.NewWorkflowID())}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installWorkflowCommandRemote(t, test.remote)
			var stdout, stderr bytes.Buffer
			if exitCode := workflowSubcommand([]string{"graph", "inspect", workflowGraphInspectSelector}, &stdout, &stderr); exitCode != 1 {
				t.Fatalf("exit code = %d, want 1", exitCode)
			}
			if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("%s failure streams stdout=%q stderr=%q", test.name, stdout.String(), stderr.String())
			}
			if len(test.remote.getRequests) != 1 || test.remote.closeCalls != 1 {
				t.Fatalf("%s calls get=%d close=%d, want one each", test.name, len(test.remote.getRequests), test.remote.closeCalls)
			}
		})
	}
}

func TestWorkflowGraphInspectReportsJSONWriteAndRemoteCloseFailures(t *testing.T) {
	t.Run("JSON writer", func(t *testing.T) {
		remote := &workflowGraphInspectRemote{definition: workflowGraphInspectDefinition(workflowGraphInspectID)}
		installWorkflowCommandRemote(t, remote)
		var stderr bytes.Buffer
		if exitCode := workflowSubcommand([]string{"graph", "inspect", workflowGraphInspectSelector}, bindingMutationFailingWriter{}, &stderr); exitCode != 1 {
			t.Fatalf("exit code = %d, want 1", exitCode)
		}
		if stderr.Len() == 0 || remote.closeCalls != 1 {
			t.Fatalf("writer failure stderr=%q close=%d, want diagnostic and one close", stderr.String(), remote.closeCalls)
		}
	})

	t.Run("remote close", func(t *testing.T) {
		remote := &workflowGraphInspectRemote{
			definition: workflowGraphInspectDefinition(workflowGraphInspectID),
			closeError: errors.New("close failed"),
		}
		installWorkflowCommandRemote(t, remote)
		var stdout, stderr bytes.Buffer
		if exitCode := workflowSubcommand([]string{"graph", "inspect", workflowGraphInspectSelector}, &stdout, &stderr); exitCode != 0 {
			t.Fatalf("exit code = %d, want 0", exitCode)
		}
		if stdout.Len() == 0 || stderr.Len() == 0 || remote.closeCalls != 1 {
			t.Fatalf("close failure streams stdout=%q stderr=%q close=%d", stdout.String(), stderr.String(), remote.closeCalls)
		}
	})
}

func workflowGraphInspectDefinition(workflowID runtimeids.WorkflowID) serverapi.WorkflowDefinition {
	return serverapi.WorkflowDefinition{
		Workflow: serverapi.WorkflowRecord{ID: workflowID, Name: "Workflow", Version: 7},
		Nodes: []serverapi.WorkflowNode{
			{ID: "node-zeta", WorkflowID: workflowID, Key: "zeta", Kind: "terminal", DisplayName: "Zeta"},
			{ID: "node-alpha", WorkflowID: workflowID, Key: "alpha", Kind: "start", DisplayName: "Alpha"},
		},
		TransitionGroups: []serverapi.WorkflowTransitionGroup{},
		Edges:            []serverapi.WorkflowEdge{},
	}
}
