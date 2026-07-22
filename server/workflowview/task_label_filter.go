package workflowview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"

	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

type workflowTaskLabelFilterFacts struct {
	Kind     serverapi.WorkflowTaskLabelFilterKind       `json:"kind"`
	Mode     *serverapi.WorkflowTaskNamedLabelFilterMode `json:"mode,omitempty"`
	LabelIDs []string                                    `json:"label_ids"`
}

type workflowProjectLabelByIDReader interface {
	ListProjectLabelsByIDs(context.Context, []string) ([]sqlitegen.ListProjectLabelsByIDsRow, error)
}

type workflowTaskLabelFilterQueryArgs struct {
	kind         string
	mode         sql.NullString
	labelIDsJSON string
}

func (f workflowTaskLabelFilterFacts) queryArgs() (workflowTaskLabelFilterQueryArgs, error) {
	labelIDsJSON, err := json.Marshal(f.LabelIDs)
	if err != nil {
		return workflowTaskLabelFilterQueryArgs{}, err
	}
	return workflowTaskLabelFilterQueryArgs{
		kind:         string(f.Kind),
		mode:         nullableWorkflowTaskLabelFilterMode(f.Mode),
		labelIDsJSON: string(labelIDsJSON),
	}, nil
}

func (f workflowTaskLabelFilterFacts) validCanonical() bool {
	if f.LabelIDs == nil {
		return false
	}
	switch f.Kind {
	case serverapi.WorkflowTaskLabelFilterKindNone, serverapi.WorkflowTaskLabelFilterKindUnlabeled:
		return f.Mode == nil && len(f.LabelIDs) == 0
	case serverapi.WorkflowTaskLabelFilterKindNamed:
		if f.Mode == nil || !sort.StringsAreSorted(f.LabelIDs) {
			return false
		}
		return (serverapi.WorkflowTaskLabelFilter{
			Kind: f.Kind,
			Named: &serverapi.WorkflowTaskNamedLabelFilter{
				Mode:     *f.Mode,
				LabelIDs: f.LabelIDs,
			},
		}).Validate() == nil
	default:
		return false
	}
}

func (f workflowTaskLabelFilterFacts) equal(other workflowTaskLabelFilterFacts) bool {
	return f.Kind == other.Kind &&
		workflowTaskLabelFilterModesEqual(f.Mode, other.Mode) &&
		slices.Equal(f.LabelIDs, other.LabelIDs)
}

func resolveWorkflowTaskLabelFilter(
	ctx context.Context,
	queries workflowProjectLabelByIDReader,
	projectID string,
	filter serverapi.WorkflowTaskLabelFilter,
) (workflowTaskLabelFilterFacts, error) {
	switch filter.Kind {
	case serverapi.WorkflowTaskLabelFilterKindNone:
		return workflowTaskLabelFilterFacts{Kind: filter.Kind, LabelIDs: []string{}}, nil
	case serverapi.WorkflowTaskLabelFilterKindUnlabeled:
		return workflowTaskLabelFilterFacts{Kind: filter.Kind, LabelIDs: []string{}}, nil
	case serverapi.WorkflowTaskLabelFilterKindNamed:
		if filter.Named == nil {
			return workflowTaskLabelFilterFacts{}, errors.New("named task label filter requires named facts")
		}
		labelIDs := append([]string(nil), filter.Named.LabelIDs...)
		sort.Strings(labelIDs)
		rows, err := queries.ListProjectLabelsByIDs(ctx, labelIDs)
		if err != nil {
			return workflowTaskLabelFilterFacts{}, err
		}
		projectByLabelID := make(map[string]string, len(rows))
		for _, row := range rows {
			projectByLabelID[row.ID] = row.ProjectID
		}
		for _, labelID := range labelIDs {
			labelProjectID, exists := projectByLabelID[labelID]
			if !exists {
				projectIDValue := projectID
				labelIDValue := labelID
				return workflowTaskLabelFilterFacts{}, &serverapi.WorkflowLabelError{
					Reason:    serverapi.WorkflowLabelErrorReasonLabelNotFound,
					ProjectID: &projectIDValue,
					LabelID:   &labelIDValue,
				}
			}
			if labelProjectID != projectID {
				projectIDValue := projectID
				labelIDValue := labelID
				return workflowTaskLabelFilterFacts{}, &serverapi.WorkflowLabelError{
					Reason:    serverapi.WorkflowLabelErrorReasonWrongProject,
					ProjectID: &projectIDValue,
					LabelID:   &labelIDValue,
				}
			}
		}
		mode := filter.Named.Mode
		return workflowTaskLabelFilterFacts{
			Kind:     filter.Kind,
			Mode:     &mode,
			LabelIDs: labelIDs,
		}, nil
	default:
		return workflowTaskLabelFilterFacts{}, errors.New("task label filter kind is invalid")
	}
}

func nullableWorkflowTaskLabelFilterMode(mode *serverapi.WorkflowTaskNamedLabelFilterMode) sql.NullString {
	if mode == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(*mode), Valid: true}
}

func workflowTaskLabelFilterModesEqual(
	left *serverapi.WorkflowTaskNamedLabelFilterMode,
	right *serverapi.WorkflowTaskNamedLabelFilterMode,
) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
