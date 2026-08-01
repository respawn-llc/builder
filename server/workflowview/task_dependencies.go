package workflowview

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type TaskDependencies struct {
	queries   *sqlitegen.Queries
	projector *TaskProjector
	authority *sessionruntime.Authority
	policy    workflow.TaskDependencyPolicy
}

type taskDependencyFactRow struct {
	direction string
	taskID    string
	shortID   string
	title     string
	workflow  string
	status    serverapi.WorkflowTaskStatus
	done      bool
}

type taskDependencyFacts struct {
	rows map[serverapi.WorkflowTaskDependencyDirection][]taskDependencyFactRow
}

type taskDependencyRowKey struct {
	direction serverapi.WorkflowTaskDependencyDirection
	taskID    string
}

func NewTaskDependencies(
	metadataStore *metadata.Store,
	projector *TaskProjector,
	authority *sessionruntime.Authority,
) (*TaskDependencies, error) {
	return newTaskDependencies(metadataStore, projector, authority, false)
}

func NewTaskDependenciesForInspection(
	metadataStore *metadata.Store,
	projector *TaskProjector,
) (*TaskDependencies, error) {
	return newTaskDependencies(metadataStore, projector, nil, true)
}

func newTaskDependencies(
	metadataStore *metadata.Store,
	projector *TaskProjector,
	authority *sessionruntime.Authority,
	allowNilAuthority bool,
) (*TaskDependencies, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if projector == nil {
		return nil, errors.New("task projector is required")
	}
	if authority == nil && !allowNilAuthority {
		return nil, errors.New("session runtime authority is required")
	}
	return &TaskDependencies{
		queries:   metadataStore.Queries(),
		projector: projector,
		authority: authority,
		policy:    workflow.TaskDependencyPolicy{},
	}, nil
}

func (d *TaskDependencies) GetTaskDependencies(ctx context.Context, taskID string) (serverapi.WorkflowTaskDependencies, error) {
	if d == nil {
		return serverapi.WorkflowTaskDependencies{}, errors.New("task dependencies are required")
	}
	trimmedTaskID := strings.TrimSpace(taskID)
	if trimmedTaskID == "" {
		return serverapi.WorkflowTaskDependencies{}, errors.New("task id is required")
	}
	facts, err := d.loadFacts(ctx, trimmedTaskID, false)
	if err != nil {
		return serverapi.WorkflowTaskDependencies{}, err
	}
	return d.projectFacts(facts)
}

func (d *TaskDependencies) CountUnsatisfiedBlockers(ctx context.Context, taskID string) (int, error) {
	if d == nil {
		return 0, errors.New("task dependencies are required")
	}
	trimmedTaskID := strings.TrimSpace(taskID)
	if trimmedTaskID == "" {
		return 0, errors.New("task id is required")
	}
	facts, err := d.loadFacts(ctx, trimmedTaskID, true)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, row := range facts.rows[serverapi.WorkflowTaskDependencyDirectionBlockedBy] {
		if !row.done {
			count++
		}
	}
	return count, nil
}

func (d *TaskDependencies) ListTaskDependencies(ctx context.Context, taskID string, requestedDirection *serverapi.WorkflowTaskDependencyDirection) (serverapi.WorkflowTaskDependencyListResponse, error) {
	if d == nil {
		return serverapi.WorkflowTaskDependencyListResponse{}, errors.New("task dependencies are required")
	}
	trimmedTaskID := strings.TrimSpace(taskID)
	if trimmedTaskID == "" {
		return serverapi.WorkflowTaskDependencyListResponse{}, errors.New("task id is required")
	}
	if requestedDirection != nil {
		switch *requestedDirection {
		case serverapi.WorkflowTaskDependencyDirectionBlockedBy, serverapi.WorkflowTaskDependencyDirectionBlocks:
		default:
			return serverapi.WorkflowTaskDependencyListResponse{}, fmt.Errorf("invalid dependency direction %q", *requestedDirection)
		}
	}
	subject, err := d.queries.GetTask(ctx, trimmedTaskID)
	if err != nil {
		return serverapi.WorkflowTaskDependencyListResponse{}, err
	}
	facts, err := d.loadFacts(ctx, trimmedTaskID, false)
	if err != nil {
		return serverapi.WorkflowTaskDependencyListResponse{}, err
	}
	directions := make([]serverapi.WorkflowTaskDependencyListDirectionProjection, 0, 2)
	for _, direction := range []serverapi.WorkflowTaskDependencyDirection{
		serverapi.WorkflowTaskDependencyDirectionBlockedBy,
		serverapi.WorkflowTaskDependencyDirectionBlocks,
	} {
		if requestedDirection != nil && *requestedDirection != direction {
			continue
		}
		rows := facts.rows[direction]
		if len(rows) == 0 {
			continue
		}
		items := projectDependencyItems(rows, direction == serverapi.WorkflowTaskDependencyDirectionBlockedBy)
		projection := serverapi.WorkflowTaskDependencyListDirectionProjection{
			Direction:  direction,
			TotalCount: len(items),
			Items:      items,
		}
		if direction == serverapi.WorkflowTaskDependencyDirectionBlockedBy {
			unsatisfied := 0
			for _, row := range rows {
				if !row.done {
					unsatisfied++
				}
			}
			projection.UnsatisfiedCount = &unsatisfied
		}
		directions = append(directions, projection)
	}
	return serverapi.WorkflowTaskDependencyListResponse{
		TaskID:     subject.ID,
		ShortID:    subject.ShortID,
		Directions: directions,
	}, nil
}

func (d *TaskDependencies) loadFacts(ctx context.Context, taskID string, blockedOnly bool) (taskDependencyFacts, error) {
	subject, err := d.queries.GetTask(ctx, taskID)
	if err != nil {
		return taskDependencyFacts{}, err
	}
	rows, err := d.relationshipRows(ctx, taskID, blockedOnly)
	if err != nil {
		return taskDependencyFacts{}, err
	}
	facts := taskDependencyFacts{
		rows: map[serverapi.WorkflowTaskDependencyDirection][]taskDependencyFactRow{
			serverapi.WorkflowTaskDependencyDirectionBlockedBy: {},
			serverapi.WorkflowTaskDependencyDirectionBlocks:    {},
		},
	}
	if len(rows) == 0 {
		return facts, nil
	}
	taskIDs := make([]string, 0, len(rows))
	seen := make(map[taskDependencyRowKey]struct{}, len(rows))
	workflowIDs := make(map[taskDependencyRowKey]runtimeids.WorkflowID, len(rows))
	for _, row := range rows {
		direction := serverapi.WorkflowTaskDependencyDirection(row.Direction)
		switch direction {
		case serverapi.WorkflowTaskDependencyDirectionBlockedBy, serverapi.WorkflowTaskDependencyDirectionBlocks:
		default:
			return taskDependencyFacts{}, fmt.Errorf("task %q dependency projection returned invalid direction %q", taskID, row.Direction)
		}
		if strings.TrimSpace(row.TaskID) == "" || strings.TrimSpace(row.ShortID) == "" {
			return taskDependencyFacts{}, fmt.Errorf("task %q dependency projection returned incomplete related Task %q", taskID, row.TaskID)
		}
		workflowID := runtimeids.WorkflowID{}
		if err := workflowID.Scan(row.WorkflowID); err != nil {
			return taskDependencyFacts{}, fmt.Errorf("task %q dependency projection returned invalid Workflow ID for related Task %q: %w", taskID, row.TaskID, err)
		}
		key := taskDependencyRowKey{direction: direction, taskID: row.TaskID}
		if _, duplicate := seen[key]; duplicate {
			return taskDependencyFacts{}, fmt.Errorf("task %q dependency projection returned duplicate related Task %q", taskID, row.TaskID)
		}
		seen[key] = struct{}{}
		workflowIDs[key] = workflowID
		taskIDs = append(taskIDs, row.TaskID)
	}
	statuses, err := loadWorkflowTaskStatusFacts(ctx, d.queries, d.projector, taskIDs)
	if err != nil {
		return taskDependencyFacts{}, err
	}
	var live map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot
	if d.authority != nil {
		live, err = d.authority.CurrentProjectTaskExecutionSnapshots(subject.ProjectID)
		if err != nil {
			return taskDependencyFacts{}, err
		}
	}
	for _, row := range rows {
		direction := serverapi.WorkflowTaskDependencyDirection(row.Direction)
		workflowID := workflowIDs[taskDependencyRowKey{direction: direction, taskID: row.TaskID}]
		durable := statuses[row.TaskID]
		taskLive := live[workflow.TaskID(row.TaskID)].Executions
		projectedStatus := taskDetailStatusFact(durable, taskLive)
		facts.rows[direction] = append(facts.rows[direction], taskDependencyFactRow{
			direction: row.Direction,
			taskID:    row.TaskID,
			shortID:   row.ShortID,
			title:     row.Title,
			workflow:  workflowID.String(),
			status:    projectedStatus.Status,
			done:      durable.Done,
		})
	}
	for direction := range facts.rows {
		sort.Slice(facts.rows[direction], func(i, j int) bool {
			left, right := facts.rows[direction][i], facts.rows[direction][j]
			if left.done != right.done {
				return !left.done
			}
			if left.shortID != right.shortID {
				return left.shortID < right.shortID
			}
			return left.taskID < right.taskID
		})
	}
	return facts, nil
}

func (d *TaskDependencies) relationshipRows(ctx context.Context, taskID string, blockedOnly bool) ([]sqlitegen.ListTaskDependencyProjectionRowsRow, error) {
	if blockedOnly {
		rows, err := d.queries.ListTaskDependencyBlockedByProjectionRows(ctx, taskID)
		if err != nil {
			return nil, fmt.Errorf("list blocked-by task dependencies for %q: %w", taskID, err)
		}
		if len(rows) > workflow.MaxTaskDependencies {
			return nil, fmt.Errorf("task %q has %d blocked-by dependencies, exceeding invariant limit %d", taskID, len(rows), workflow.MaxTaskDependencies)
		}
		out := make([]sqlitegen.ListTaskDependencyProjectionRowsRow, len(rows))
		for index, row := range rows {
			out[index] = sqlitegen.ListTaskDependencyProjectionRowsRow{
				Direction:  row.Direction,
				TaskID:     row.TaskID,
				ShortID:    row.ShortID,
				Title:      row.Title,
				WorkflowID: row.WorkflowID,
			}
		}
		return out, nil
	}
	rows, err := d.queries.ListTaskDependencyProjectionRows(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("list task dependencies for %q: %w", taskID, err)
	}
	if len(rows) > workflow.MaxTaskDependencies*2 {
		return nil, fmt.Errorf("task %q has %d direct dependencies, exceeding invariant limit %d per direction", taskID, len(rows), workflow.MaxTaskDependencies)
	}
	return rows, nil
}

func (d *TaskDependencies) projectFacts(facts taskDependencyFacts) (serverapi.WorkflowTaskDependencies, error) {
	blockedBy := facts.rows[serverapi.WorkflowTaskDependencyDirectionBlockedBy]
	blocks := facts.rows[serverapi.WorkflowTaskDependencyDirectionBlocks]
	blockedByAvailability, err := d.addAvailability(len(blockedBy))
	if err != nil {
		return serverapi.WorkflowTaskDependencies{}, err
	}
	blocksAvailability, err := d.addAvailability(len(blocks))
	if err != nil {
		return serverapi.WorkflowTaskDependencies{}, err
	}
	unsatisfied := 0
	for _, row := range blockedBy {
		if !row.done {
			unsatisfied++
		}
	}
	unsatisfiedCount := unsatisfied
	return serverapi.WorkflowTaskDependencies{
		BlockerCount:             len(blockedBy),
		UnsatisfiedBlockerCount:  unsatisfied,
		DirectlyBlockedTaskCount: len(blocks),
		Directions: []serverapi.WorkflowTaskDependencyDirectionProjection{
			{
				Direction:        serverapi.WorkflowTaskDependencyDirectionBlockedBy,
				TotalCount:       len(blockedBy),
				UnsatisfiedCount: &unsatisfiedCount,
				Items:            projectDependencyItems(blockedBy, true),
				AddAvailability:  blockedByAvailability,
			},
			{
				Direction:       serverapi.WorkflowTaskDependencyDirectionBlocks,
				TotalCount:      len(blocks),
				Items:           projectDependencyItems(blocks, false),
				AddAvailability: blocksAvailability,
			},
		},
	}, nil
}

func projectDependencyItems(rows []taskDependencyFactRow, includeSatisfaction bool) []serverapi.WorkflowTaskDependencyItem {
	items := make([]serverapi.WorkflowTaskDependencyItem, 0, len(rows))
	for _, row := range rows {
		item := serverapi.WorkflowTaskDependencyItem{
			TaskID:     row.taskID,
			ShortID:    row.shortID,
			Title:      row.title,
			WorkflowID: row.workflow,
			Status:     row.status,
		}
		if includeSatisfaction {
			satisfaction := serverapi.WorkflowTaskDependencyUnsatisfied
			if row.done {
				satisfaction = serverapi.WorkflowTaskDependencySatisfied
			}
			item.Satisfaction = &satisfaction
		}
		items = append(items, item)
	}
	return items
}

func (d *TaskDependencies) addAvailability(count int) (*serverapi.WorkflowTaskDependencyAddAvailability, error) {
	availability, err := d.policy.AddAvailability(int64(count))
	if err != nil {
		return nil, err
	}
	switch availability.Kind {
	case workflow.TaskDependencyAddAvailable:
		if availability.RemainingCapacity == nil {
			return nil, errors.New("task dependency availability omitted remaining capacity")
		}
		return &serverapi.WorkflowTaskDependencyAddAvailability{
			Available: &serverapi.WorkflowTaskDependencyAvailable{RemainingCapacity: int(*availability.RemainingCapacity)},
		}, nil
	case workflow.TaskDependencyAddLimitReached:
		return &serverapi.WorkflowTaskDependencyAddAvailability{
			LimitReached: &serverapi.WorkflowTaskDependencyLimitReached{},
		}, nil
	default:
		return nil, fmt.Errorf("task dependency availability has invalid kind %q", availability.Kind)
	}
}
