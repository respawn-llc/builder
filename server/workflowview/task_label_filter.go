package workflowview

import (
	"context"
	"errors"
	"sort"

	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

type workflowTaskLabelFilterFacts struct {
	Kind     serverapi.WorkflowTaskLabelFilterKind      `json:"kind"`
	Mode     serverapi.WorkflowTaskNamedLabelFilterMode `json:"mode,omitempty"`
	LabelIDs []string                                   `json:"label_ids"`
}

type workflowProjectLabelByIDReader interface {
	ListProjectLabelsByIDs(context.Context, []string) ([]sqlitegen.ListProjectLabelsByIDsRow, error)
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
				return workflowTaskLabelFilterFacts{}, &serverapi.WorkflowLabelError{
					Reason:    serverapi.WorkflowLabelErrorReasonLabelNotFound,
					ProjectID: projectID,
					LabelID:   labelID,
				}
			}
			if labelProjectID != projectID {
				return workflowTaskLabelFilterFacts{}, &serverapi.WorkflowLabelError{
					Reason:    serverapi.WorkflowLabelErrorReasonWrongProject,
					ProjectID: projectID,
					LabelID:   labelID,
				}
			}
		}
		return workflowTaskLabelFilterFacts{
			Kind:     filter.Kind,
			Mode:     filter.Named.Mode,
			LabelIDs: labelIDs,
		}, nil
	default:
		return workflowTaskLabelFilterFacts{}, errors.New("task label filter kind is invalid")
	}
}
