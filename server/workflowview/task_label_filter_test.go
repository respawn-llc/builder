package workflowview

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

type workflowProjectLabelByIDReaderStub struct {
	rows []sqlitegen.ListProjectLabelsByIDsRow
}

func (r *workflowProjectLabelByIDReaderStub) ListProjectLabelsByIDs(_ context.Context, labelIDs []string) ([]sqlitegen.ListProjectLabelsByIDsRow, error) {
	return append([]sqlitegen.ListProjectLabelsByIDsRow(nil), r.rows...), nil
}

func TestResolveWorkflowTaskLabelFilterCanonicalizesIncludedAndExcludedPartitions(t *testing.T) {
	const (
		projectID = "project-1"
		alphaID   = "00000000-0000-4000-8000-000000000001"
		betaID    = "00000000-0000-4000-8000-000000000002"
		gammaID   = "00000000-0000-4000-8000-000000000003"
		deltaID   = "00000000-0000-4000-8000-000000000004"
	)
	reader := &workflowProjectLabelByIDReaderStub{
		rows: []sqlitegen.ListProjectLabelsByIDsRow{
			{ID: betaID, ProjectID: projectID},
			{ID: alphaID, ProjectID: projectID},
			{ID: deltaID, ProjectID: projectID},
			{ID: gammaID, ProjectID: projectID},
		},
	}

	facts, err := resolveWorkflowTaskLabelFilter(
		t.Context(),
		reader,
		projectID,
		serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNamed,
			Named: &serverapi.WorkflowTaskNamedLabelFilter{
				Mode:             serverapi.WorkflowTaskNamedLabelFilterModeAny,
				LabelIDs:         []string{deltaID, betaID},
				ExcludedLabelIDs: []string{gammaID, alphaID},
			},
		},
	)
	if err != nil {
		t.Fatalf("resolveWorkflowTaskLabelFilter: %v", err)
	}
	wantIDs := []string{betaID, deltaID}
	wantExcludedIDs := []string{alphaID, gammaID}
	if facts.Kind != serverapi.WorkflowTaskLabelFilterKindNamed ||
		facts.Mode == nil ||
		*facts.Mode != serverapi.WorkflowTaskNamedLabelFilterModeAny ||
		!reflect.DeepEqual(facts.LabelIDs, wantIDs) ||
		!reflect.DeepEqual(facts.ExcludedLabelIDs, wantExcludedIDs) {
		t.Fatalf("resolved facts = %+v, want named any included %v excluded %v", facts, wantIDs, wantExcludedIDs)
	}
	namedArgs, err := facts.queryArgs()
	if err != nil {
		t.Fatalf("named query args: %v", err)
	}
	if !namedArgs.mode.Valid || namedArgs.mode.String != string(serverapi.WorkflowTaskNamedLabelFilterModeAny) {
		t.Fatalf("named mode query arg = %+v", namedArgs.mode)
	}
	if namedArgs.excludedLabelIDsJSON != `["00000000-0000-4000-8000-000000000001","00000000-0000-4000-8000-000000000003"]` {
		t.Fatalf("excluded label IDs query arg = %q", namedArgs.excludedLabelIDsJSON)
	}
	noneArgs, err := (workflowTaskLabelFilterFacts{
		Kind:             serverapi.WorkflowTaskLabelFilterKindNone,
		LabelIDs:         []string{},
		ExcludedLabelIDs: []string{},
	}).queryArgs()
	if err != nil {
		t.Fatalf("none query args: %v", err)
	}
	if noneArgs.mode.Valid {
		t.Fatalf("none mode query arg = %+v, want SQL null", noneArgs.mode)
	}
}

func TestResolveWorkflowTaskLabelFilterRetainsTypedMissingAndWrongProjectErrorsAcrossPartitions(t *testing.T) {
	const (
		projectID = "project-1"
		labelID   = "00000000-0000-4000-8000-000000000001"
	)
	filter := serverapi.WorkflowTaskLabelFilter{
		Kind: serverapi.WorkflowTaskLabelFilterKindNamed,
		Named: &serverapi.WorkflowTaskNamedLabelFilter{
			Mode:             serverapi.WorkflowTaskNamedLabelFilterModeAny,
			ExcludedLabelIDs: []string{labelID},
		},
	}
	for _, tt := range []struct {
		name   string
		rows   []sqlitegen.ListProjectLabelsByIDsRow
		reason serverapi.WorkflowLabelErrorReason
	}{
		{
			name:   "missing label",
			reason: serverapi.WorkflowLabelErrorReasonLabelNotFound,
		},
		{
			name: "wrong project label",
			rows: []sqlitegen.ListProjectLabelsByIDsRow{{
				ID:        labelID,
				ProjectID: "other-project",
			}},
			reason: serverapi.WorkflowLabelErrorReasonWrongProject,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveWorkflowTaskLabelFilter(t.Context(), &workflowProjectLabelByIDReaderStub{rows: tt.rows}, projectID, filter)
			var typed *serverapi.WorkflowLabelError
			if !errors.As(err, &typed) || typed.Reason != tt.reason {
				t.Fatalf("resolveWorkflowTaskLabelFilter() error = %T %+v, want %q", err, err, tt.reason)
			}
		})
	}
}

func TestWorkflowTaskLabelFilterFactsEqualityIncludesExcludedPartition(t *testing.T) {
	const (
		alphaID = "00000000-0000-4000-8000-000000000001"
		betaID  = "00000000-0000-4000-8000-000000000002"
	)
	mode := serverapi.WorkflowTaskNamedLabelFilterModeAny
	base := workflowTaskLabelFilterFacts{
		Kind:             serverapi.WorkflowTaskLabelFilterKindNamed,
		Mode:             &mode,
		LabelIDs:         []string{alphaID},
		ExcludedLabelIDs: []string{betaID},
	}
	if !base.validCanonical() {
		t.Fatalf("base canonical facts rejected: %+v", base)
	}
	if !base.equal(base) {
		t.Fatal("canonical facts are not equal to themselves")
	}
	changedExclusion := base
	changedExclusion.ExcludedLabelIDs = []string{alphaID}
	if base.equal(changedExclusion) {
		t.Fatalf("facts with different exclusions compare equal: %+v and %+v", base, changedExclusion)
	}
}
