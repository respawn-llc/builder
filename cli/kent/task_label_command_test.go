package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
)

const taskLabelCommandTestProjectID = "project-1"

type taskLabelCommandRemote struct {
	apicontract.WorkflowService

	catalogResponse serverapi.WorkflowProjectLabelCatalogResponse
	listRequests    []serverapi.WorkflowProjectLabelCatalogRequest
	reorderRequests []serverapi.WorkflowProjectLabelReorderRequest
	reorderResponse serverapi.WorkflowProjectLabelReorderResponse
}

func (r *taskLabelCommandRemote) ListWorkflowProjectLabels(_ context.Context, req serverapi.WorkflowProjectLabelCatalogRequest) (serverapi.WorkflowProjectLabelCatalogResponse, error) {
	r.listRequests = append(r.listRequests, req)
	return r.catalogResponse, nil
}

func (r *taskLabelCommandRemote) ReorderWorkflowProjectLabels(_ context.Context, req serverapi.WorkflowProjectLabelReorderRequest) (serverapi.WorkflowProjectLabelReorderResponse, error) {
	r.reorderRequests = append(r.reorderRequests, req)
	return r.reorderResponse, nil
}

func (r *taskLabelCommandRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *taskLabelCommandRemote) Close() error {
	return nil
}

func TestTaskLabelListNameUsesUnicodeFoldWithoutUUIDSelectorAmbiguity(t *testing.T) {
	tests := []struct {
		name          string
		requestedName string
		label         serverapi.WorkflowProjectLabel
	}{
		{
			name:          "Unicode-equivalent name",
			requestedName: "STRASSE",
			label: serverapi.WorkflowProjectLabel{
				ID:   "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
				Name: "Straße",
			},
		},
		{
			name:          "canonical UUID-shaped name",
			requestedName: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			label: serverapi.WorkflowProjectLabel{
				ID:   "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
				Name: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &taskLabelCommandRemote{
				catalogResponse: serverapi.WorkflowProjectLabelCatalogResponse{
					Catalog: serverapi.WorkflowProjectLabelCatalog{
						ProjectID: taskLabelCommandTestProjectID,
						Labels:    []serverapi.WorkflowProjectLabel{test.label},
					},
				},
			}
			installWorkflowCommandRemote(t, remote)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := taskSubcommand(
				[]string{
					"label",
					"list",
					"--project",
					taskLabelCommandTestProjectID,
					"--name",
					test.requestedName,
					"--json",
				},
				&stdout,
				&stderr,
			)

			if exitCode != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
			}
			if len(remote.listRequests) != 1 || remote.listRequests[0].ProjectID != taskLabelCommandTestProjectID {
				t.Fatalf("list requests = %+v, want project %q", remote.listRequests, taskLabelCommandTestProjectID)
			}
			var output serverapi.WorkflowProjectLabelCatalogResponse
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatalf("decode output: %v; output=%q", err, stdout.String())
			}
			if len(output.Catalog.Labels) != 1 || output.Catalog.Labels[0] != test.label {
				t.Fatalf("labels = %+v, want %+v", output.Catalog.Labels, test.label)
			}
		})
	}
}

func TestTaskLabelListPreservesAuthoritativeCatalogOrder(t *testing.T) {
	labels := []serverapi.WorkflowProjectLabel{
		{ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Name: "Zulu"},
		{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Name: "alpha"},
	}
	remote := &taskLabelCommandRemote{
		catalogResponse: serverapi.WorkflowProjectLabelCatalogResponse{
			Catalog: serverapi.WorkflowProjectLabelCatalog{
				ProjectID: taskLabelCommandTestProjectID,
				Labels:    labels,
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand(
		[]string{"label", "list", "--project", taskLabelCommandTestProjectID, "--json"},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	var output serverapi.WorkflowProjectLabelCatalogResponse
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v; output=%q", err, stdout.String())
	}
	if !reflect.DeepEqual(output.Catalog.Labels, labels) {
		t.Fatalf("labels = %+v, want authoritative order %+v", output.Catalog.Labels, labels)
	}
}

func TestTaskLabelMoveFirstJSONSubmitsCompletePermutation(t *testing.T) {
	labels := []serverapi.WorkflowProjectLabel{
		{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Name: "Alpha"},
		{ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Name: "Beta"},
		{ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", Name: "Gamma"},
	}
	expected := []string{labels[2].ID, labels[0].ID, labels[1].ID}
	remote := &taskLabelCommandRemote{
		catalogResponse: serverapi.WorkflowProjectLabelCatalogResponse{
			Catalog: serverapi.WorkflowProjectLabelCatalog{
				ProjectID: taskLabelCommandTestProjectID,
				Labels:    labels,
			},
		},
		reorderResponse: serverapi.WorkflowProjectLabelReorderResponse{
			Catalog: serverapi.WorkflowProjectLabelCatalog{
				ProjectID: taskLabelCommandTestProjectID,
				Labels:    []serverapi.WorkflowProjectLabel{labels[2], labels[0], labels[1]},
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand(
		[]string{
			"label", "move",
			"--project", taskLabelCommandTestProjectID,
			"--label", labels[2].Name,
			"--first",
			"--json",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.listRequests) != 1 || remote.listRequests[0].ProjectID != taskLabelCommandTestProjectID {
		t.Fatalf("list requests = %+v, want one request for project %q", remote.listRequests, taskLabelCommandTestProjectID)
	}
	if len(remote.reorderRequests) != 1 {
		t.Fatalf("reorder requests = %+v, want one request", remote.reorderRequests)
	}
	if got := remote.reorderRequests[0]; got.ProjectID != taskLabelCommandTestProjectID || !reflect.DeepEqual(got.LabelIDs, expected) {
		t.Fatalf("reorder request = %+v, want project %q and IDs %+v", got, taskLabelCommandTestProjectID, expected)
	}
	var output serverapi.WorkflowProjectLabelReorderResponse
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v; output=%q", err, stdout.String())
	}
	if !reflect.DeepEqual(output, remote.reorderResponse) {
		t.Fatalf("output = %+v, want %+v", output, remote.reorderResponse)
	}
}

func TestTaskLabelMovePlacementsAndNoOpSubmitOneAuthoritativeOrder(t *testing.T) {
	labels := []serverapi.WorkflowProjectLabel{
		{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Name: "Alpha"},
		{ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Name: "Beta"},
		{ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", Name: "Gamma"},
	}
	tests := []struct {
		name       string
		placement  []string
		moved      string
		expected   []string
		jsonOutput bool
	}{
		{name: "last", placement: []string{"--last"}, moved: "Alpha", expected: []string{labels[1].ID, labels[2].ID, labels[0].ID}},
		{name: "before", placement: []string{"--before", "Alpha"}, moved: "Gamma", expected: []string{labels[2].ID, labels[0].ID, labels[1].ID}},
		{name: "after", placement: []string{"--after", "Alpha"}, moved: "Gamma", expected: []string{labels[0].ID, labels[2].ID, labels[1].ID}},
		{name: "no-op", placement: []string{"--before", "Beta"}, moved: "Beta", expected: []string{labels[0].ID, labels[1].ID, labels[2].ID}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &taskLabelCommandRemote{
				catalogResponse: serverapi.WorkflowProjectLabelCatalogResponse{
					Catalog: serverapi.WorkflowProjectLabelCatalog{
						ProjectID: taskLabelCommandTestProjectID,
						Labels:    labels,
					},
				},
				reorderResponse: serverapi.WorkflowProjectLabelReorderResponse{
					Catalog: serverapi.WorkflowProjectLabelCatalog{
						ProjectID: taskLabelCommandTestProjectID,
						Labels:    labelsForIDs(labels, test.expected),
					},
				},
			}
			installWorkflowCommandRemote(t, remote)
			args := []string{"label", "move", "--project", taskLabelCommandTestProjectID, "--label", test.moved}
			args = append(args, test.placement...)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if exitCode := taskSubcommand(args, &stdout, &stderr); exitCode != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
			}
			if len(remote.listRequests) != 1 || len(remote.reorderRequests) != 1 {
				t.Fatalf("list requests = %d, reorder requests = %d, want one each", len(remote.listRequests), len(remote.reorderRequests))
			}
			if !reflect.DeepEqual(remote.reorderRequests[0].LabelIDs, test.expected) {
				t.Fatalf("reorder IDs = %+v, want %+v", remote.reorderRequests[0].LabelIDs, test.expected)
			}
			if test.name == "no-op" && stdout.String() != taskLabelMovePlainAcknowledgement+"\n" {
				t.Fatalf("plain output = %q, want product acknowledgement", stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestTaskLabelMoveRejectsGrammarBeforeOpeningSession(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing label", args: []string{"label", "move", "--first"}},
		{name: "positional", args: []string{"label", "move", "--label", "Alpha", "--first", "extra"}},
		{name: "zero placement", args: []string{"label", "move", "--label", "Alpha"}},
		{name: "first and last", args: []string{"label", "move", "--label", "Alpha", "--first", "--last"}},
		{name: "first and before", args: []string{"label", "move", "--label", "Alpha", "--first", "--before", "Beta"}},
		{name: "last and after", args: []string{"label", "move", "--label", "Alpha", "--last", "--after", "Beta"}},
		{name: "before and after", args: []string{"label", "move", "--label", "Alpha", "--before", "Beta", "--after", "Gamma"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opened := false
			previous := workflowCommandRemoteOpener
			workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
				opened = true
				return config.App{}, nil, nil
			}
			t.Cleanup(func() { workflowCommandRemoteOpener = previous })
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if exitCode := taskSubcommand(test.args, &stdout, &stderr); exitCode != 2 {
				t.Fatalf("exit code = %d, want 2; stderr=%q", exitCode, stderr.String())
			}
			if opened {
				t.Fatal("remote opener called for invalid command grammar")
			}
		})
	}
}

func labelsForIDs(labels []serverapi.WorkflowProjectLabel, ids []string) []serverapi.WorkflowProjectLabel {
	byID := make(map[string]serverapi.WorkflowProjectLabel, len(labels))
	for _, label := range labels {
		byID[label.ID] = label
	}
	result := make([]serverapi.WorkflowProjectLabel, 0, len(ids))
	for _, id := range ids {
		result = append(result, byID[id])
	}
	return result
}

func TestTaskLabelMoveRejectsInvalidSettlementBeforeOutput(t *testing.T) {
	labels := []serverapi.WorkflowProjectLabel{
		{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Name: "Alpha"},
		{ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Name: "Beta"},
	}
	tests := []struct {
		name     string
		response serverapi.WorkflowProjectLabelCatalog
	}{
		{
			name: "wrong project",
			response: serverapi.WorkflowProjectLabelCatalog{
				ProjectID: "project-2",
				Labels:    labels,
			},
		},
		{
			name: "invalid catalog shape",
			response: serverapi.WorkflowProjectLabelCatalog{
				ProjectID: taskLabelCommandTestProjectID,
				Labels:    []serverapi.WorkflowProjectLabel{{ID: "not-a-uuid", Name: "Alpha"}},
			},
		},
		{
			name: "folded name collision",
			response: serverapi.WorkflowProjectLabelCatalog{
				ProjectID: taskLabelCommandTestProjectID,
				Labels: []serverapi.WorkflowProjectLabel{
					labels[0],
					{ID: labels[1].ID, Name: "alpha"},
				},
			},
		},
		{
			name: "missing label ID",
			response: serverapi.WorkflowProjectLabelCatalog{
				ProjectID: taskLabelCommandTestProjectID,
				Labels:    []serverapi.WorkflowProjectLabel{labels[0]},
			},
		},
		{
			name: "extra label ID",
			response: serverapi.WorkflowProjectLabelCatalog{
				ProjectID: taskLabelCommandTestProjectID,
				Labels: append(append([]serverapi.WorkflowProjectLabel(nil), labels...), serverapi.WorkflowProjectLabel{
					ID:   "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
					Name: "Gamma",
				}),
			},
		},
		{
			name: "reordered label IDs",
			response: serverapi.WorkflowProjectLabelCatalog{
				ProjectID: taskLabelCommandTestProjectID,
				Labels:    []serverapi.WorkflowProjectLabel{labels[0], labels[1]},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &taskLabelCommandRemote{
				catalogResponse: serverapi.WorkflowProjectLabelCatalogResponse{
					Catalog: serverapi.WorkflowProjectLabelCatalog{
						ProjectID: taskLabelCommandTestProjectID,
						Labels:    labels,
					},
				},
				reorderResponse: serverapi.WorkflowProjectLabelReorderResponse{Catalog: test.response},
			}
			installWorkflowCommandRemote(t, remote)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := taskSubcommand([]string{
				"label", "move", "--project", taskLabelCommandTestProjectID,
				"--label", "Beta", "--first", "--json",
			}, &stdout, &stderr)
			if exitCode == 0 {
				t.Fatalf("exit code = 0, want failure; stdout=%q", stdout.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestTaskLabelMoveAggregatesUnresolvedMovedAndRelativeSelectors(t *testing.T) {
	remote := &taskLabelCommandRemote{
		catalogResponse: serverapi.WorkflowProjectLabelCatalogResponse{
			Catalog: serverapi.WorkflowProjectLabelCatalog{
				ProjectID: taskLabelCommandTestProjectID,
				Labels: []serverapi.WorkflowProjectLabel{
					{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Name: "Alpha"},
				},
			},
		},
	}
	installWorkflowCommandRemote(t, remote)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{
		"label", "move", "--project", taskLabelCommandTestProjectID,
		"--label", "missing-moved", "--before", "missing-relative",
	}, &stdout, &stderr)
	if exitCode == 0 || stdout.Len() != 0 {
		t.Fatalf("exit code = %d stdout=%q, want selector failure before output", exitCode, stdout.String())
	}
	if !strings.Contains(stderr.String(), "missing-moved") || !strings.Contains(stderr.String(), "missing-relative") {
		t.Fatalf("stderr = %q, want both unresolved selectors", stderr.String())
	}
}
