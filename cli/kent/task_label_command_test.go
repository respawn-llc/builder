package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"core/shared/apicontract"
	"core/shared/serverapi"
)

const taskLabelCommandTestProjectID = "project-1"

type taskLabelCommandRemote struct {
	apicontract.WorkflowService

	catalogResponse serverapi.WorkflowProjectLabelCatalogResponse
	listRequests    []serverapi.WorkflowProjectLabelCatalogRequest
}

func (r *taskLabelCommandRemote) ListWorkflowProjectLabels(_ context.Context, req serverapi.WorkflowProjectLabelCatalogRequest) (serverapi.WorkflowProjectLabelCatalogResponse, error) {
	r.listRequests = append(r.listRequests, req)
	return r.catalogResponse, nil
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
