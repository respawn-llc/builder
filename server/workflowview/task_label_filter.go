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
	Kind             serverapi.WorkflowTaskLabelFilterKind       `json:"kind"`
	Mode             *serverapi.WorkflowTaskNamedLabelFilterMode `json:"mode,omitempty"`
	LabelIDs         []string                                    `json:"label_ids"`
	ExcludedLabelIDs []string                                    `json:"excluded_label_ids"`
}

type workflowProjectLabelByIDReader interface {
	ListProjectLabelsByIDs(context.Context, []string) ([]sqlitegen.ListProjectLabelsByIDsRow, error)
}

type workflowTaskLabelFilterQueryArgs struct {
	kind                 string
	mode                 sql.NullString
	labelIDsJSON         string
	excludedLabelIDsJSON string
}

func (f workflowTaskLabelFilterFacts) queryArgs() (workflowTaskLabelFilterQueryArgs, error) {
	labelIDsJSON, err := json.Marshal(f.LabelIDs)
	if err != nil {
		return workflowTaskLabelFilterQueryArgs{}, err
	}
	excludedLabelIDsJSON, err := json.Marshal(f.ExcludedLabelIDs)
	if err != nil {
		return workflowTaskLabelFilterQueryArgs{}, err
	}
	return workflowTaskLabelFilterQueryArgs{
		kind:                 string(f.Kind),
		mode:                 nullableWorkflowTaskLabelFilterMode(f.Mode),
		labelIDsJSON:         string(labelIDsJSON),
		excludedLabelIDsJSON: string(excludedLabelIDsJSON),
	}, nil
}

func (f workflowTaskLabelFilterFacts) validCanonical() bool {
	if f.LabelIDs == nil || f.ExcludedLabelIDs == nil {
		return false
	}
	switch f.Kind {
	case serverapi.WorkflowTaskLabelFilterKindNone, serverapi.WorkflowTaskLabelFilterKindUnlabeled:
		return f.Mode == nil && len(f.LabelIDs) == 0 && len(f.ExcludedLabelIDs) == 0
	case serverapi.WorkflowTaskLabelFilterKindNamed:
		if f.Mode == nil || !sort.StringsAreSorted(f.LabelIDs) || !sort.StringsAreSorted(f.ExcludedLabelIDs) {
			return false
		}
		return (serverapi.WorkflowTaskLabelFilter{
			Kind: f.Kind,
			Named: &serverapi.WorkflowTaskNamedLabelFilter{
				Mode:             *f.Mode,
				LabelIDs:         f.LabelIDs,
				ExcludedLabelIDs: f.ExcludedLabelIDs,
			},
		}).Validate() == nil
	default:
		return false
	}
}

func (f workflowTaskLabelFilterFacts) equal(other workflowTaskLabelFilterFacts) bool {
	return f.Kind == other.Kind &&
		workflowTaskLabelFilterModesEqual(f.Mode, other.Mode) &&
		slices.Equal(f.LabelIDs, other.LabelIDs) &&
		slices.Equal(f.ExcludedLabelIDs, other.ExcludedLabelIDs)
}

func resolveWorkflowTaskLabelFilter(
	ctx context.Context,
	queries workflowProjectLabelByIDReader,
	projectID string,
	filter serverapi.WorkflowTaskLabelFilter,
) (workflowTaskLabelFilterFacts, error) {
	switch filter.Kind {
	case serverapi.WorkflowTaskLabelFilterKindNone:
		return workflowTaskLabelFilterFacts{Kind: filter.Kind, LabelIDs: []string{}, ExcludedLabelIDs: []string{}}, nil
	case serverapi.WorkflowTaskLabelFilterKindUnlabeled:
		return workflowTaskLabelFilterFacts{Kind: filter.Kind, LabelIDs: []string{}, ExcludedLabelIDs: []string{}}, nil
	case serverapi.WorkflowTaskLabelFilterKindNamed:
		if filter.Named == nil {
			return workflowTaskLabelFilterFacts{}, errors.New("named task label filter requires named facts")
		}
		labelIDs := append([]string{}, filter.Named.LabelIDs...)
		excludedLabelIDs := append([]string{}, filter.Named.ExcludedLabelIDs...)
		sort.Strings(labelIDs)
		sort.Strings(excludedLabelIDs)
		allLabelIDs := make([]string, 0, len(labelIDs)+len(excludedLabelIDs))
		allLabelIDs = append(allLabelIDs, labelIDs...)
		allLabelIDs = append(allLabelIDs, excludedLabelIDs...)
		sort.Strings(allLabelIDs)
		rows, err := queries.ListProjectLabelsByIDs(ctx, allLabelIDs)
		if err != nil {
			return workflowTaskLabelFilterFacts{}, err
		}
		projectByLabelID := make(map[string]string, len(rows))
		for _, row := range rows {
			projectByLabelID[row.ID] = row.ProjectID
		}
		for _, labelID := range allLabelIDs {
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
			Kind:             filter.Kind,
			Mode:             &mode,
			LabelIDs:         labelIDs,
			ExcludedLabelIDs: excludedLabelIDs,
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
