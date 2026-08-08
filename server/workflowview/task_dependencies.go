package workflowview

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type TaskDependencies struct {
	queries    *sqlitegen.Queries
	projection *TaskStatusProjection
	counter    *TaskDependencyCounter
	policy     workflow.TaskDependencyPolicy
}

type taskDependencySatisfaction struct {
	queries   *sqlitegen.Queries
	projector *TaskProjector
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
	projection *TaskStatusProjection,
	counter *TaskDependencyCounter,
) (*TaskDependencies, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if projection == nil {
		return nil, errors.New("task status projection is required")
	}
	if counter == nil {
		return nil, errors.New("task dependency counter is required")
	}
	return &TaskDependencies{
		queries:    metadataStore.Queries(),
		projection: projection,
		counter:    counter,
		policy:     workflow.TaskDependencyPolicy{},
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
	return d.counter.CountUnsatisfiedBlockers(ctx, trimmedTaskID)
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
	if d.projection == nil {
		return taskDependencyFacts{}, errors.New("task status projection is required")
	}
	var facts taskDependencyFacts
	err := d.projection.WithSnapshot(ctx, nil, func(observation TaskStatusObservation, durable *TaskStatusDurableSnapshot) error {
		var err error
		facts, err = d.loadFactsWithSnapshot(ctx, taskID, blockedOnly, observation, durable)
		return err
	})
	return facts, err
}

func (d *TaskDependencies) projectTaskDependenciesWithSnapshot(
	ctx context.Context,
	taskID string,
	observation TaskStatusObservation,
	durable *TaskStatusDurableSnapshot,
) (serverapi.WorkflowTaskDependencies, error) {
	facts, err := d.loadFactsWithSnapshot(ctx, taskID, false, observation, durable)
	if err != nil {
		return serverapi.WorkflowTaskDependencies{}, err
	}
	return d.projectFacts(facts)
}

func (d *TaskDependencies) loadFactsWithSnapshot(
	ctx context.Context,
	taskID string,
	blockedOnly bool,
	observation TaskStatusObservation,
	durable *TaskStatusDurableSnapshot,
) (taskDependencyFacts, error) {
	if err := durable.validate(); err != nil {
		return taskDependencyFacts{}, err
	}
	rows, err := d.relationshipRowsWithQueries(ctx, durable.queries, taskID, blockedOnly)
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
	statuses, err := d.projectDependencyStatuses(ctx, taskIDs, observation, durable)
	if err != nil {
		return taskDependencyFacts{}, err
	}
	for _, row := range rows {
		direction := serverapi.WorkflowTaskDependencyDirection(row.Direction)
		workflowID := workflowIDs[taskDependencyRowKey{direction: direction, taskID: row.TaskID}]
		projectedStatus := statuses[workflow.TaskID(row.TaskID)]
		facts.rows[direction] = append(facts.rows[direction], taskDependencyFactRow{
			direction: row.Direction,
			taskID:    row.TaskID,
			shortID:   row.ShortID,
			title:     row.Title,
			workflow:  workflowID.String(),
			status:    projectedStatus.Status,
			done:      projectedStatus.Done,
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

func (d *TaskDependencies) projectDependencyStatuses(
	ctx context.Context,
	taskIDs []string,
	observation TaskStatusObservation,
	durable *TaskStatusDurableSnapshot,
) (map[workflow.TaskID]workflowTaskStatusFact, error) {
	ids := make([]workflow.TaskID, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		ids = append(ids, workflow.TaskID(taskID))
	}
	projected, err := durable.ProjectedStatuses(ctx, ids, observation.LiveTaskStatesJSON)
	if err != nil {
		return nil, err
	}
	return projected, nil
}

func (d *TaskDependencies) relationshipRows(ctx context.Context, taskID string, blockedOnly bool) ([]sqlitegen.ListTaskDependencyProjectionRowsRow, error) {
	return d.relationshipRowsWithQueries(ctx, d.queries, taskID, blockedOnly)
}

func (d *TaskDependencies) relationshipRowsWithQueries(
	ctx context.Context,
	queries *sqlitegen.Queries,
	taskID string,
	blockedOnly bool,
) ([]sqlitegen.ListTaskDependencyProjectionRowsRow, error) {
	if queries == nil {
		return nil, errors.New("workflow queries are required")
	}
	if blockedOnly {
		return listDependencyBlockerRows(ctx, queries, taskID)
	}
	rows, err := queries.ListTaskDependencyProjectionRows(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("list task dependencies for %q: %w", taskID, err)
	}
	if len(rows) > workflow.MaxTaskDependencies*2 {
		return nil, fmt.Errorf("task %q has %d direct dependencies, exceeding invariant limit %d per direction", taskID, len(rows), workflow.MaxTaskDependencies)
	}
	return rows, nil
}

func listDependencyBlockerRows(
	ctx context.Context,
	queries *sqlitegen.Queries,
	taskID string,
) ([]sqlitegen.ListTaskDependencyProjectionRowsRow, error) {
	rows, err := queries.ListTaskDependencyBlockedByProjectionRows(ctx, taskID)
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

func (s taskDependencySatisfaction) CountUnsatisfiedBlockers(ctx context.Context, taskID string) (int, error) {
	if s.queries == nil || s.projector == nil {
		return 0, errors.New("task dependency satisfaction is required")
	}
	rows, err := listDependencyBlockerRows(ctx, s.queries, taskID)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	taskIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		taskIDs = append(taskIDs, row.TaskID)
	}
	statuses, err := loadWorkflowTaskStatusFacts(ctx, s.queries, s.projector, taskIDs)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, relatedTaskID := range taskIDs {
		if !statuses[relatedTaskID].Done {
			count++
		}
	}
	return count, nil
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
