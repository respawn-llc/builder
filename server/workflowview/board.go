package workflowview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/serverapi"
)

type Board struct {
	metadata     *metadata.Store
	queries      *sqlitegen.Queries
	definitions  *DefinitionProjection
	roleResolver workflow.RoleResolver
}

func NewBoard(metadataStore *metadata.Store, definitions *DefinitionProjection, roleResolver workflow.RoleResolver) (*Board, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if definitions == nil {
		return nil, errors.New("definition projection is required")
	}
	if roleResolver == nil {
		return nil, errors.New("role resolver is required")
	}
	return &Board{
		metadata:     metadataStore,
		queries:      metadataStore.Queries(),
		definitions:  definitions,
		roleResolver: roleResolver,
	}, nil
}

func (b *Board) Get(ctx context.Context, req serverapi.WorkflowBoardRequest) (serverapi.WorkflowBoard, error) {
	if b == nil {
		return serverapi.WorkflowBoard{}, errors.New("board is required")
	}
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		return serverapi.WorkflowBoard{}, errors.New("project_id is required")
	}
	definitions, picker, err := b.selectionInputs(ctx, projectID)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	project, err := b.metadata.GetProjectOverview(ctx, projectID)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	workspaceContext := boardProjectWorkspaceContext(project)
	selected := workflowBoardSelectorFromRequest(req).selectFrom(picker)
	if selected == nil {
		return serverapi.WorkflowBoard{
			ProjectID:         projectID,
			Project:           projectBoardProject(project, workspaceContext),
			WorkflowPicker:    picker,
			GeneratedAtUnixMs: time.Now().UTC().UnixMilli(),
		}, nil
	}
	snapshot := definitions[selected.WorkflowID]
	definition := snapshot.api
	groups := boardGroups(definition)
	columns := boardColumns(snapshot)
	if err := b.applyColumnTaskCounts(ctx, columns, projectID, selected.WorkflowID, canceledBoardTerminalNodeID(definition)); err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	return serverapi.WorkflowBoard{
		ProjectID:         projectID,
		Project:           projectBoardProject(project, workspaceContext),
		SelectedWorkflow:  selected,
		WorkflowPicker:    picker,
		Groups:            groups,
		Columns:           columns,
		GeneratedAtUnixMs: time.Now().UTC().UnixMilli(),
	}, nil
}

func (b *Board) selectionInputs(ctx context.Context, projectID string) (map[string]definitionSnapshot, []serverapi.WorkflowPickerItem, error) {
	links, err := b.queries.ListProjectWorkflowLinks(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	taskActivityRows, err := b.queries.ListProjectWorkflowTaskActivity(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	workflowIDs := make([]string, 0, len(links))
	linkByWorkflowID := map[string]sqlitegen.ProjectWorkflowLinkRecord{}
	for _, link := range links {
		if _, exists := linkByWorkflowID[link.WorkflowID]; exists {
			continue
		}
		linkByWorkflowID[link.WorkflowID] = link
		workflowIDs = append(workflowIDs, link.WorkflowID)
	}
	activityByWorkflowID := map[string]int64{}
	for _, activity := range taskActivityRows {
		if _, linked := linkByWorkflowID[activity.WorkflowID]; linked {
			activityByWorkflowID[activity.WorkflowID] = activity.LatestUpdatedAtUnixMs
		}
	}
	definitions := make(map[string]definitionSnapshot, len(workflowIDs))
	picker := make([]serverapi.WorkflowPickerItem, 0, len(workflowIDs))
	for _, workflowID := range workflowIDs {
		snapshot, err := b.definitions.snapshot(ctx, workflowID)
		if err != nil {
			return nil, nil, err
		}
		definitions[workflowID] = snapshot
		link, linked := linkByWorkflowID[workflowID]
		if !linked {
			return nil, nil, fmt.Errorf("workflow selection invariant violated: active link missing for project_id=%q workflow_id=%q", projectID, workflowID)
		}
		validation := definitionExecutionValidation(snapshot.domain, b.roleResolver)
		picker = append(picker, serverapi.WorkflowPickerItem{
			WorkflowID:           workflowID,
			DisplayName:          snapshot.api.Workflow.Name,
			Description:          snapshot.api.Workflow.Description,
			Version:              snapshot.api.Workflow.Version,
			IsProjectDefault:     link.IsDefault != 0,
			ValidForTaskCreation: !validation.HasBlockingErrors(),
			ValidationErrors:     ValidationErrors(snapshot.api.Workflow.ID, validation.Errors),
		})
	}
	sort.SliceStable(picker, func(i, j int) bool {
		if picker[i].IsProjectDefault != picker[j].IsProjectDefault {
			return picker[i].IsProjectDefault
		}
		if activityByWorkflowID[picker[i].WorkflowID] != activityByWorkflowID[picker[j].WorkflowID] {
			return activityByWorkflowID[picker[i].WorkflowID] > activityByWorkflowID[picker[j].WorkflowID]
		}
		return strings.ToLower(picker[i].DisplayName) < strings.ToLower(picker[j].DisplayName)
	})
	return definitions, picker, nil
}

func (b *Board) applyColumnTaskCounts(ctx context.Context, columns []serverapi.WorkflowBoardColumn, projectID string, workflowID string, canceledTerminalNodeID *string) error {
	rows, err := b.queries.ListBoardColumnTaskCounts(ctx, sqlitegen.ListBoardColumnTaskCountsParams{
		ProjectID:              projectID,
		WorkflowID:             workflowID,
		CanceledTerminalNodeID: nullableWorkflowViewString(canceledTerminalNodeID),
	})
	if err != nil {
		return err
	}
	indexByNodeID := map[string]int{}
	for index, column := range columns {
		indexByNodeID[column.Node.NodeID] = index
	}
	for _, row := range rows {
		nodeID := strings.TrimSpace(row.NodeID.String)
		if !row.NodeID.Valid || nodeID == "" {
			continue
		}
		if index, ok := indexByNodeID[nodeID]; ok {
			columns[index].TaskCount = int(row.TaskCount)
		}
	}
	return nil
}

type workflowBoardSelector interface {
	selectFrom(picker []serverapi.WorkflowPickerItem) *serverapi.WorkflowPickerItem
}

type workflowBoardDefaultSelector struct{}

func (workflowBoardDefaultSelector) selectFrom(picker []serverapi.WorkflowPickerItem) *serverapi.WorkflowPickerItem {
	return defaultWorkflowBoardSelection(picker)
}

type workflowBoardExplicitSelector struct {
	workflowID string
}

func (s workflowBoardExplicitSelector) selectFrom(picker []serverapi.WorkflowPickerItem) *serverapi.WorkflowPickerItem {
	if selected := exactWorkflowBoardSelection(picker, s.workflowID); selected != nil {
		return selected
	}
	return defaultWorkflowBoardSelection(picker)
}

func workflowBoardSelectorFromRequest(req serverapi.WorkflowBoardRequest) workflowBoardSelector {
	if req.WorkflowID != nil {
		return workflowBoardExplicitSelector{workflowID: *req.WorkflowID}
	}
	return workflowBoardDefaultSelector{}
}

func exactWorkflowBoardSelection(picker []serverapi.WorkflowPickerItem, workflowID string) *serverapi.WorkflowPickerItem {
	for index := range picker {
		if picker[index].WorkflowID == workflowID {
			return &picker[index]
		}
	}
	return nil
}

func defaultWorkflowBoardSelection(picker []serverapi.WorkflowPickerItem) *serverapi.WorkflowPickerItem {
	for index := range picker {
		if picker[index].IsProjectDefault {
			return &picker[index]
		}
	}
	if len(picker) == 0 {
		return nil
	}
	return &picker[0]
}

func boardGroups(def serverapi.WorkflowDefinition) []serverapi.WorkflowBoardGroup {
	columnNodes := boardColumnNodes(def)
	groups := make([]serverapi.WorkflowBoardGroup, 0, len(def.NodeGroups))
	for _, group := range def.NodeGroups {
		dto := serverapi.WorkflowBoardGroup{
			GroupID:     group.GroupID,
			Key:         group.GroupKey,
			DisplayName: group.DisplayName,
			SortOrder:   group.SortOrder,
		}
		for _, node := range columnNodes {
			if node.GroupID == group.GroupID {
				dto.NodeIDs = append(dto.NodeIDs, node.ID)
			}
		}
		if len(dto.NodeIDs) > 0 {
			groups = append(groups, dto)
		}
	}
	return groups
}

func boardColumns(snapshot definitionSnapshot) []serverapi.WorkflowBoardColumn {
	columns := make([]serverapi.WorkflowBoardColumn, 0, len(snapshot.api.Nodes))
	derived := workflow.DeriveWiring(snapshot.domain)
	for index, node := range boardColumnNodes(snapshot.api) {
		columns = append(columns, serverapi.WorkflowBoardColumn{
			Node: serverapi.WorkflowBoardNodeSummary{
				NodeID:                 node.ID,
				Key:                    node.Key,
				Kind:                   node.Kind,
				DisplayName:            node.DisplayName,
				AssigneeRole:           node.SubagentRole,
				SortOrder:              index,
				OutputFields:           OutputFields(derived.PossibleProvisionFieldsForNode(workflow.NodeID(node.ID))),
				TransitionOutputFields: OutputFields(workflow.TransitionOutputFieldsForTargetNode(snapshot.domain, derived, workflow.NodeID(node.ID))),
			},
			GroupID:   node.GroupID,
			SortOrder: index,
			IsBacklog: node.Kind == string(workflow.NodeKindStart),
			IsDone:    node.Kind == string(workflow.NodeKindTerminal),
		})
	}
	return columns
}

func boardVisibleNodeKind(kind string) bool {
	return workflow.NodeKind(kind) != workflow.NodeKindJoin
}

func nullableWorkflowViewString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}
