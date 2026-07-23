package workflowview

import (
	"context"
	"reflect"
	"testing"

	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

type countingWorkflowProjectLabelByIDReader struct {
	calls    int
	labelIDs []string
	rows     []sqlitegen.ListProjectLabelsByIDsRow
}

func (r *countingWorkflowProjectLabelByIDReader) ListProjectLabelsByIDs(_ context.Context, labelIDs []string) ([]sqlitegen.ListProjectLabelsByIDsRow, error) {
	r.calls++
	r.labelIDs = append([]string(nil), labelIDs...)
	return append([]sqlitegen.ListProjectLabelsByIDsRow(nil), r.rows...), nil
}

func TestResolveWorkflowTaskLabelFilterValidatesOneCanonicalProjectSet(t *testing.T) {
	const (
		projectID = "project-1"
		alphaID   = "00000000-0000-4000-8000-000000000001"
		betaID    = "00000000-0000-4000-8000-000000000002"
	)
	reader := &countingWorkflowProjectLabelByIDReader{
		rows: []sqlitegen.ListProjectLabelsByIDsRow{
			{ID: alphaID, ProjectID: projectID},
			{ID: betaID, ProjectID: projectID},
		},
	}

	facts, err := resolveWorkflowTaskLabelFilter(
		t.Context(),
		reader,
		projectID,
		serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNamed,
			Named: &serverapi.WorkflowTaskNamedLabelFilter{
				Mode:     serverapi.WorkflowTaskNamedLabelFilterModeAny,
				LabelIDs: []string{betaID, alphaID},
			},
		},
	)
	if err != nil {
		t.Fatalf("resolveWorkflowTaskLabelFilter: %v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("project label reads = %d, want one", reader.calls)
	}
	wantIDs := []string{alphaID, betaID}
	if !reflect.DeepEqual(reader.labelIDs, wantIDs) {
		t.Fatalf("validated label IDs = %v, want canonical %v", reader.labelIDs, wantIDs)
	}
	if facts.Kind != serverapi.WorkflowTaskLabelFilterKindNamed ||
		facts.Mode == nil ||
		*facts.Mode != serverapi.WorkflowTaskNamedLabelFilterModeAny ||
		!reflect.DeepEqual(facts.LabelIDs, wantIDs) {
		t.Fatalf("resolved facts = %+v, want named any %v", facts, wantIDs)
	}
	namedArgs, err := facts.queryArgs()
	if err != nil {
		t.Fatalf("named query args: %v", err)
	}
	if !namedArgs.mode.Valid || namedArgs.mode.String != string(serverapi.WorkflowTaskNamedLabelFilterModeAny) {
		t.Fatalf("named mode query arg = %+v", namedArgs.mode)
	}
	noneArgs, err := (workflowTaskLabelFilterFacts{
		Kind:     serverapi.WorkflowTaskLabelFilterKindNone,
		LabelIDs: []string{},
	}).queryArgs()
	if err != nil {
		t.Fatalf("none query args: %v", err)
	}
	if noneArgs.mode.Valid {
		t.Fatalf("none mode query arg = %+v, want SQL null", noneArgs.mode)
	}
}
