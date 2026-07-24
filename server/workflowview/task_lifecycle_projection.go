package workflowview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/shared/invariant"
	"core/shared/serverapi"
)

type TaskLifecycleProjection struct {
	queries   *sqlitegen.Queries
	authority *sessionruntime.Authority
}

type taskLifecycleProjection struct {
	CurrentExecutions     []sessionruntime.TaskExecution
	InterruptedSessionIDs []string
	RunActions            taskRunActionFacts
	facts                 []taskExecutablePlacementFact
}

type taskExecutablePlacementFact struct {
	taskID          string
	placementID     string
	nodeID          string
	nodeKind        workflow.NodeKind
	runID           *string
	sessionID       *string
	generation      *int64
	started         bool
	completed       bool
	interrupted     bool
	waitingQuestion bool
	exact           *sessionruntime.TaskExecution
}

func NewTaskLifecycleProjection(metadataStore *metadata.Store, authority *sessionruntime.Authority) (*TaskLifecycleProjection, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	return &TaskLifecycleProjection{
		queries:   metadataStore.Queries(),
		authority: authority,
	}, nil
}

func (p *TaskLifecycleProjection) Project(
	ctx context.Context,
	taskIDs []string,
	statuses map[string]workflowTaskStatusFact,
) (map[string]taskLifecycleProjection, error) {
	if p == nil {
		return nil, errors.New("task lifecycle projection is required")
	}
	if len(taskIDs) == 0 {
		return map[string]taskLifecycleProjection{}, nil
	}
	domainTaskIDs := make([]workflow.TaskID, 0, len(taskIDs))
	projections := make(map[string]taskLifecycleProjection, len(taskIDs))
	seenTaskIDs := make(map[string]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if strings.TrimSpace(taskID) == "" {
			return nil, errors.New("workflow task id is required")
		}
		if _, duplicate := seenTaskIDs[taskID]; duplicate {
			return nil, fmt.Errorf("workflow task id %q is duplicated", taskID)
		}
		if _, exists := statuses[taskID]; !exists {
			return nil, fmt.Errorf("workflow task status is missing for task %q", taskID)
		}
		seenTaskIDs[taskID] = struct{}{}
		domainTaskIDs = append(domainTaskIDs, workflow.TaskID(taskID))
		projections[taskID] = taskLifecycleProjection{
			CurrentExecutions:     []sessionruntime.TaskExecution{},
			InterruptedSessionIDs: []string{},
			facts:                 []taskExecutablePlacementFact{},
		}
	}
	snapshots, err := p.authority.CurrentTaskExecutionSnapshots(domainTaskIDs)
	if err != nil {
		return nil, err
	}
	rows, err := p.queries.ListWorkflowTaskExecutablePlacementFactsByTasks(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	exactByTaskAndRef := make(map[string]map[sessionruntime.WorkflowExecutionRef]sessionruntime.TaskExecution, len(taskIDs))
	for _, taskID := range taskIDs {
		exactByRef := make(map[sessionruntime.WorkflowExecutionRef]sessionruntime.TaskExecution)
		for _, execution := range snapshots[workflow.TaskID(taskID)].Executions {
			if _, duplicate := exactByRef[execution.Ref]; duplicate {
				return nil, fmt.Errorf(
					"workflow task %q has duplicate exact execution for run %q generation %d",
					taskID,
					execution.Ref.RunID,
					execution.Ref.Generation,
				)
			}
			exactByRef[execution.Ref] = execution
		}
		exactByTaskAndRef[taskID] = exactByRef
	}
	seenPlacements := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		status, exists := statuses[row.TaskID]
		if !exists {
			return nil, fmt.Errorf("workflow lifecycle projection returned unexpected task %q", row.TaskID)
		}
		fact, err := executablePlacementFact(row, exactByTaskAndRef[row.TaskID])
		if err != nil {
			return nil, err
		}
		if _, duplicate := seenPlacements[fact.placementID]; duplicate {
			return nil, p.fail(
				status.Status.Kind,
				serverapi.WorkflowTaskIntegrityReasonCurrentRunMissing,
				fact,
				taskRunActionFacts{},
			)
		}
		seenPlacements[fact.placementID] = struct{}{}
		projection := projections[row.TaskID]
		projection.facts = append(projection.facts, fact)
		if fact.runID == nil || fact.generation == nil {
			return nil, p.fail(
				status.Status.Kind,
				serverapi.WorkflowTaskIntegrityReasonCurrentRunMissing,
				fact,
				projection.RunActions,
			)
		}
		switch fact.nodeKind {
		case workflow.NodeKindAgent:
			if fact.started && fact.sessionID == nil {
				return nil, p.fail(
					status.Status.Kind,
					serverapi.WorkflowTaskIntegrityReasonAgentSessionMissing,
					fact,
					projection.RunActions,
				)
			}
			if fact.exact != nil {
				if fact.exact.Agent == nil ||
					fact.exact.Script != nil ||
					fact.sessionID == nil ||
					fact.exact.Agent.SessionID.String() != *fact.sessionID {
					return nil, p.fail(
						status.Status.Kind,
						serverapi.WorkflowTaskIntegrityReasonExactExecutionMismatch,
						fact,
						projection.RunActions,
					)
				}
			}
		case workflow.NodeKindScript:
			if fact.exact != nil && (fact.exact.Script == nil || fact.exact.Agent != nil) {
				return nil, p.fail(
					status.Status.Kind,
					serverapi.WorkflowTaskIntegrityReasonExactExecutionMismatch,
					fact,
					projection.RunActions,
				)
			}
		default:
			return nil, fmt.Errorf("workflow task %q executable placement has node kind %q", row.TaskID, fact.nodeKind)
		}
		if fact.started &&
			!fact.interrupted &&
			fact.exact == nil &&
			(fact.nodeKind == workflow.NodeKindScript || !fact.waitingQuestion) {
			return nil, p.fail(
				status.Status.Kind,
				serverapi.WorkflowTaskIntegrityReasonExactExecutionMissing,
				fact,
				projection.RunActions,
			)
		}
		if fact.interrupted {
			projection.RunActions.HasInterrupted = true
			if fact.nodeKind == workflow.NodeKindAgent && fact.sessionID != nil {
				projection.InterruptedSessionIDs = append(projection.InterruptedSessionIDs, *fact.sessionID)
			}
		} else if fact.started {
			if fact.waitingQuestion {
				projection.RunActions.HasWaitingQuestion = true
			}
			if fact.exact != nil {
				execution := *fact.exact
				execution.WaitingQuestion = fact.waitingQuestion
				projection.CurrentExecutions = append(projection.CurrentExecutions, execution)
			}
			if !fact.waitingQuestion && fact.exact != nil {
				projection.RunActions.HasRunning = true
			}
		}
		projections[row.TaskID] = projection
	}
	for taskID, projection := range projections {
		sort.Slice(projection.CurrentExecutions, func(i, j int) bool {
			left := projection.CurrentExecutions[i].Ref
			right := projection.CurrentExecutions[j].Ref
			if left.RunID != right.RunID {
				return left.RunID < right.RunID
			}
			return left.Generation < right.Generation
		})
		sort.Strings(projection.InterruptedSessionIDs)
		projections[taskID] = projection
	}
	return projections, nil
}

func executablePlacementFact(
	row sqlitegen.ListWorkflowTaskExecutablePlacementFactsByTasksRow,
	exactByRef map[sessionruntime.WorkflowExecutionRef]sessionruntime.TaskExecution,
) (taskExecutablePlacementFact, error) {
	nodeID := strings.TrimSpace(row.NodeID.String)
	if !row.NodeID.Valid || nodeID == "" {
		return taskExecutablePlacementFact{}, fmt.Errorf("workflow task %q executable placement %q has no node id", row.TaskID, row.PlacementID)
	}
	fact := taskExecutablePlacementFact{
		taskID:          row.TaskID,
		placementID:     row.PlacementID,
		nodeID:          nodeID,
		nodeKind:        workflow.NodeKind(row.NodeKind),
		started:         row.StartedAtUnixMs.Valid,
		completed:       row.CompletedAtUnixMs.Valid,
		interrupted:     row.InterruptedAtUnixMs.Valid,
		waitingQuestion: row.WaitingAskID.Valid,
	}
	if row.RunID.Valid {
		runID := strings.TrimSpace(row.RunID.String)
		if runID == "" {
			return taskExecutablePlacementFact{}, fmt.Errorf("workflow task %q executable placement %q has a blank run id", row.TaskID, row.PlacementID)
		}
		fact.runID = &runID
		if !row.RunGeneration.Valid || row.RunGeneration.Int64 < 0 {
			return taskExecutablePlacementFact{}, fmt.Errorf(
				"workflow task %q run %q has invalid generation",
				row.TaskID,
				runID,
			)
		}
		generation := row.RunGeneration.Int64
		fact.generation = &generation
		ref := sessionruntime.WorkflowExecutionRef{
			TaskID:     workflow.TaskID(row.TaskID),
			RunID:      workflow.RunID(runID),
			Generation: generation,
		}
		if exact, exists := exactByRef[ref]; exists {
			exactCopy := exact
			fact.exact = &exactCopy
		}
	} else if row.RunGeneration.Valid ||
		row.StartedAtUnixMs.Valid ||
		row.CompletedAtUnixMs.Valid ||
		row.InterruptedAtUnixMs.Valid ||
		row.WaitingAskID.Valid ||
		row.SessionID.Valid {
		return taskExecutablePlacementFact{}, fmt.Errorf(
			"workflow task %q executable placement %q has lifecycle facts without a run",
			row.TaskID,
			row.PlacementID,
		)
	}
	if row.SessionID.Valid {
		sessionID := strings.TrimSpace(row.SessionID.String)
		if sessionID == "" {
			return taskExecutablePlacementFact{}, fmt.Errorf("workflow task %q run has a blank session id", row.TaskID)
		}
		fact.sessionID = &sessionID
	}
	return fact, nil
}

func (p *TaskLifecycleProjection) ValidateActions(
	status workflowTaskStatusFact,
	projection taskLifecycleProjection,
	actions serverapi.WorkflowTaskActions,
) error {
	if status.Done || status.Status.Kind == serverapi.WorkflowTaskStatusKindCanceled || len(projection.facts) == 0 {
		return nil
	}
	expected := taskRunActionFacts{
		HasRunning:     projection.RunActions.HasRunning,
		HasInterrupted: projection.RunActions.HasInterrupted,
	}
	if actions.CanInterrupt == expected.HasRunning && actions.CanResume == expected.HasInterrupted {
		return nil
	}
	return p.fail(
		status.Status.Kind,
		serverapi.WorkflowTaskIntegrityReasonActionProjectionInvalid,
		projection.facts[0],
		taskRunActionFacts{
			HasRunning:     actions.CanInterrupt,
			HasInterrupted: actions.CanResume,
		},
	)
}

func (p *TaskLifecycleProjection) fail(
	statusKind serverapi.WorkflowTaskStatusKind,
	reason serverapi.WorkflowTaskIntegrityReason,
	fact taskExecutablePlacementFact,
	actions taskRunActionFacts,
) error {
	integrityErr := workflowTaskIntegrityError(statusKind, reason, fact, actions)
	diagnostic := invariant.FailureDiagnostic(
		invariant.ScopeWorkflowProjection,
		"project_workflow_task_lifecycle",
		integrityErr,
	)
	diagnostic.Fields[invariant.FieldTaskID] = fact.taskID
	diagnostic.Fields[invariant.FieldNodeID] = fact.nodeID
	diagnostic.Fields[invariant.FieldNodeKind] = string(fact.nodeKind)
	if fact.runID != nil {
		diagnostic.Fields[invariant.FieldRunID] = *fact.runID
	}
	if fact.sessionID != nil {
		diagnostic.Fields[invariant.FieldSessionID] = *fact.sessionID
	}
	if fact.generation != nil {
		diagnostic.Fields[invariant.FieldGeneration] = strconv.FormatInt(*fact.generation, 10)
	}
	diagnostic.Fields[invariant.FieldDurableLifecycle] = mustWorkflowIntegrityDiagnosticJSON(integrityErr.Durable)
	diagnostic.Fields[invariant.FieldExactExecution] = mustWorkflowIntegrityDiagnosticJSON(integrityErr.Exact)
	diagnostic.Fields[invariant.FieldActionProjection] = mustWorkflowIntegrityDiagnosticJSON(integrityErr.Actions)
	invariant.NewPolicy().Check(false, diagnostic)
	return integrityErr
}

func workflowTaskIntegrityError(
	statusKind serverapi.WorkflowTaskStatusKind,
	reason serverapi.WorkflowTaskIntegrityReason,
	fact taskExecutablePlacementFact,
	actions taskRunActionFacts,
) *serverapi.WorkflowTaskIntegrityError {
	exact := serverapi.WorkflowTaskIntegrityExactFacts{}
	if fact.exact != nil {
		exact.Present = true
		exact.WaitingQuestion = fact.exact.WaitingQuestion
		switch {
		case fact.exact.Agent != nil:
			kind := string(workflow.NodeKindAgent)
			sessionID := fact.exact.Agent.SessionID.String()
			exact.Kind = &kind
			exact.SessionID = &sessionID
		case fact.exact.Script != nil:
			kind := string(workflow.NodeKindScript)
			exact.Kind = &kind
		}
	}
	return &serverapi.WorkflowTaskIntegrityError{
		Reason:      reason,
		TaskID:      fact.taskID,
		PlacementID: fact.placementID,
		NodeID:      fact.nodeID,
		NodeKind:    string(fact.nodeKind),
		RunID:       cloneWorkflowIntegrityString(fact.runID),
		SessionID:   cloneWorkflowIntegrityString(fact.sessionID),
		Generation:  cloneWorkflowIntegrityInt64(fact.generation),
		StatusKind:  statusKind,
		Durable: serverapi.WorkflowTaskIntegrityDurableFacts{
			RunPresent:      fact.runID != nil,
			Started:         fact.started,
			Completed:       fact.completed,
			Interrupted:     fact.interrupted,
			WaitingQuestion: fact.waitingQuestion,
		},
		Exact: exact,
		Actions: serverapi.WorkflowTaskIntegrityActionFacts{
			CanInterrupt: actions.HasRunning,
			CanResume:    actions.HasInterrupted,
		},
	}
}

func cloneWorkflowIntegrityString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneWorkflowIntegrityInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func mustWorkflowIntegrityDiagnosticJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal workflow integrity diagnostic: %v", err))
	}
	return string(encoded)
}
