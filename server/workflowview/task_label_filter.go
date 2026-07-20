package workflowview

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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

type workflowTaskLabelFilterQueryArgs struct {
	kind         string
	mode         string
	labelIDsJSON string
}

func (f workflowTaskLabelFilterFacts) queryArgs() (workflowTaskLabelFilterQueryArgs, error) {
	labelIDsJSON, err := json.Marshal(f.LabelIDs)
	if err != nil {
		return workflowTaskLabelFilterQueryArgs{}, err
	}
	return workflowTaskLabelFilterQueryArgs{
		kind:         string(f.Kind),
		mode:         string(f.Mode),
		labelIDsJSON: string(labelIDsJSON),
	}, nil
}

func (f workflowTaskLabelFilterFacts) validCanonical() bool {
	if f.LabelIDs == nil {
		return false
	}
	switch f.Kind {
	case serverapi.WorkflowTaskLabelFilterKindNone, serverapi.WorkflowTaskLabelFilterKindUnlabeled:
		return f.Mode == "" && len(f.LabelIDs) == 0
	case serverapi.WorkflowTaskLabelFilterKindNamed:
		if !sort.StringsAreSorted(f.LabelIDs) {
			return false
		}
		return (serverapi.WorkflowTaskLabelFilter{
			Kind: f.Kind,
			Named: &serverapi.WorkflowTaskNamedLabelFilter{
				Mode:     f.Mode,
				LabelIDs: f.LabelIDs,
			},
		}).Validate() == nil
	default:
		return false
	}
}

func (f workflowTaskLabelFilterFacts) equal(other workflowTaskLabelFilterFacts) bool {
	return f.Kind == other.Kind &&
		f.Mode == other.Mode &&
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
