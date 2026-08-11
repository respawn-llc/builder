package metadata

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"text/template"
	"text/template/parse"

	"core/server/workflow"
	"core/shared/runtimeids"

	"github.com/google/uuid"
	sqlitedriver "modernc.org/sqlite"
)

const migrationCurrentInputValuesFunction = "kent_migration_current_input_values_v1"
const migrationPriorParametersFunction = "kent_migration_prior_transition_parameters_v1"
const migrationPriorNodeValuesFunction = "kent_migration_prior_node_values_v1"
const migrationWorkflowIDBlobFunction = "kent_migration_workflow_id_blob_v1"
const migrationWorkflowIDTextFunction = "kent_migration_workflow_id_text_v1"
const migrationWorkflowRunHistoryCutoverIrreversibleFunction = "kent_workflow_run_history_cutover_is_irreversible"
const migrationCurrentNodePriorValuesIrreversibleFunction = "kent_current_node_prior_transition_parameters_are_irreversible"
const migrationWorkflowSessionAgentRoleIrreversibleFunction = "kent_workflow_session_agent_role_backfill_is_irreversible"
const migrationCurrentNodeAgentExecutionFunction = "kent_migration_current_node_agent_execution_v1"
const migrationCurrentNodeAgentExecutionValidationFunction = "kent_migration_current_node_agent_execution_validation_v1"
const migrationPendingApprovalAgentExecutionValidationFunction = "kent_migration_pending_approval_agent_execution_validation_v1"
const migrationGraphEntityIDBlobFunction = "kent_migration_graph_entity_id_blob_v1"

func migrationAgentExecutionSelection(
	contextMode workflow.ContextMode,
	contextSource workflow.ContextSource,
	targetSessionResolved bool,
	targetBound bool,
	fallbackRole string,
	sessionRole string,
) (workflow.AgentExecutionSelection, error) {
	fallbackRole = strings.TrimSpace(fallbackRole)
	if fallbackRole == "" {
		return workflow.AgentExecutionSelection{}, errors.New("current node Agent fallback role is required")
	}
	policy, err := workflow.ResolveAssigneeSessionPolicy(workflow.AssigneeSessionPolicyRequest{
		ContextMode:           contextMode,
		ContextSource:         contextSource,
		TargetSessionResolved: targetSessionResolved,
	})
	if err != nil {
		return workflow.AgentExecutionSelection{}, fmt.Errorf("resolve current node Agent session policy: %w", err)
	}
	origin := workflow.AssigneeOriginConfiguredFallback
	role := fallbackRole
	if targetBound || policy == workflow.AssigneeSessionPolicyPreserve {
		role = strings.TrimSpace(sessionRole)
		if role == "" {
			role = workflow.DefaultAgentRole
		}
		origin = workflow.AssigneeOriginRetainedSession
	}
	selection, err := workflow.NewAgentExecutionSelection(role, nil, origin)
	if err != nil {
		return workflow.AgentExecutionSelection{}, fmt.Errorf("materialize current node Agent execution selection: %w", err)
	}
	return selection, nil
}

var registerMetadataSQLiteFunctionsOnce sync.Once
var registerMetadataSQLiteFunctionsErr error

func registerMetadataSQLiteFunctions() error {
	registerMetadataSQLiteFunctionsOnce.Do(func() {
		registerMetadataSQLiteFunctionsErr = sqlitedriver.RegisterDeterministicScalarFunction(
			migrationCurrentInputValuesFunction,
			10,
			migrationCurrentInputValues,
		)
		if registerMetadataSQLiteFunctionsErr != nil {
			return
		}
		registerMetadataSQLiteFunctionsErr = sqlitedriver.RegisterDeterministicScalarFunction(
			migrationPriorParametersFunction,
			7,
			migrationPriorParameters,
		)
		if registerMetadataSQLiteFunctionsErr != nil {
			return
		}
		registerMetadataSQLiteFunctionsErr = sqlitedriver.RegisterDeterministicScalarFunction(
			migrationWorkflowIDBlobFunction,
			2,
			migrationWorkflowIDBlob,
		)
		if registerMetadataSQLiteFunctionsErr != nil {
			return
		}
		registerMetadataSQLiteFunctionsErr = sqlitedriver.RegisterDeterministicScalarFunction(
			migrationWorkflowIDTextFunction,
			2,
			migrationWorkflowIDText,
		)
		if registerMetadataSQLiteFunctionsErr != nil {
			return
		}
		registerMetadataSQLiteFunctionsErr = sqlitedriver.RegisterDeterministicScalarFunction(
			graphEntityIDBlobFunction,
			1,
			graphEntityIDBlob,
		)
		if registerMetadataSQLiteFunctionsErr != nil {
			return
		}
		registerMetadataSQLiteFunctionsErr = sqlitedriver.RegisterDeterministicScalarFunction(
			graphEntityIDTextFunction,
			1,
			graphEntityIDText,
		)
		if registerMetadataSQLiteFunctionsErr != nil {
			return
		}
		registerMetadataSQLiteFunctionsErr = sqlitedriver.RegisterScalarFunction(
			migrationGraphEntityIDBlobFunction,
			2,
			migrationGraphEntityIDBlob,
		)
		if registerMetadataSQLiteFunctionsErr != nil {
			return
		}
		registerMetadataSQLiteFunctionsErr = sqlitedriver.RegisterDeterministicScalarFunction(
			migrationCurrentNodeAgentExecutionFunction,
			6,
			migrationCurrentNodeAgentExecution,
		)
		if registerMetadataSQLiteFunctionsErr != nil {
			return
		}
		registerMetadataSQLiteFunctionsErr = sqlitedriver.RegisterDeterministicScalarFunction(
			migrationCurrentNodeAgentExecutionValidationFunction,
			4,
			migrationValidateCurrentNodeAgentExecution,
		)
		if registerMetadataSQLiteFunctionsErr != nil {
			return
		}
		registerMetadataSQLiteFunctionsErr = sqlitedriver.RegisterDeterministicScalarFunction(
			migrationPendingApprovalAgentExecutionValidationFunction,
			1,
			migrationValidatePendingApprovalAgentExecution,
		)
		if registerMetadataSQLiteFunctionsErr != nil {
			return
		}
		for _, functionName := range [...]string{
			migrationWorkflowRunHistoryCutoverIrreversibleFunction,
			migrationCurrentNodePriorValuesIrreversibleFunction,
			migrationWorkflowSessionAgentRoleIrreversibleFunction,
		} {
			registerMetadataSQLiteFunctionsErr = sqlitedriver.RegisterDeterministicScalarFunction(
				functionName,
				0,
				migrationIrreversibleMarker(functionName),
			)
			if registerMetadataSQLiteFunctionsErr != nil {
				return
			}
		}
	})
	if registerMetadataSQLiteFunctionsErr != nil {
		return fmt.Errorf("register metadata SQLite migration functions: %w", registerMetadataSQLiteFunctionsErr)
	}
	return nil
}

func migrationGraphEntityIDBlob(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("%s requires 2 arguments", migrationGraphEntityIDBlobFunction)
	}
	raw, err := migrationStringArgument(args[0], "graph entity ID")
	if err != nil {
		return nil, err
	}
	location, err := migrationStringArgument(args[1], "graph identity location")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(location) == "" {
		return nil, errors.New("graph identity migration location is required")
	}
	value, err := runtimeids.MigrateGraphEntityIDBlob(raw)
	if err != nil {
		return nil, fmt.Errorf("graph identity migration failure at %s: %w", location, err)
	}
	return value, nil
}

func migrationCurrentNodeAgentExecution(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 6 {
		return nil, fmt.Errorf("%s requires 6 arguments", migrationCurrentNodeAgentExecutionFunction)
	}
	modeRaw, err := migrationStringArgument(args[0], "context mode")
	if err != nil {
		return nil, err
	}
	sourceRaw, err := migrationStringArgument(args[1], "context source")
	if err != nil {
		return nil, err
	}
	targetResolved, err := migrationBooleanArgument(args[2], "target session resolution")
	if err != nil {
		return nil, err
	}
	targetBound, err := migrationBooleanArgument(args[3], "target binding")
	if err != nil {
		return nil, err
	}
	selection, err := migrationAgentExecutionSelection(
		workflow.ContextMode(strings.TrimSpace(modeRaw)),
		workflow.ContextSource{Kind: workflow.ContextSourceKind(strings.TrimSpace(sourceRaw))},
		targetResolved,
		targetBound,
		migrationOptionalStringArgument(args[4]),
		migrationOptionalStringArgument(args[5]),
	)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(selection)
	if err != nil {
		return nil, fmt.Errorf("encode migrated Agent execution selection: %w", err)
	}
	return string(encoded), nil
}

func migrationValidateCurrentNodeAgentExecution(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 4 {
		return nil, fmt.Errorf("%s requires 4 arguments", migrationCurrentNodeAgentExecutionValidationFunction)
	}
	kind, err := migrationStringArgument(args[0], "current node kind")
	if err != nil {
		return nil, err
	}
	assignee := strings.TrimSpace(migrationOptionalStringArgument(args[1]))
	thinking := strings.TrimSpace(migrationOptionalStringArgument(args[2]))
	origin := strings.TrimSpace(migrationOptionalStringArgument(args[3]))
	switch workflow.NodeKind(strings.TrimSpace(kind)) {
	case workflow.NodeKindAgent:
		if assignee == "" || origin == "" {
			return nil, errors.New("Agent current node selection is incomplete")
		}
		switch workflow.AssigneeOrigin(origin) {
		case workflow.AssigneeOriginConfiguredFallback, workflow.AssigneeOriginTransitionSelected, workflow.AssigneeOriginRetainedSession:
		default:
			return nil, fmt.Errorf("Agent current node selection origin %q is invalid", origin)
		}
	case workflow.NodeKindStart, workflow.NodeKindScript, workflow.NodeKindJoin, workflow.NodeKindTerminal:
		if assignee != "" || thinking != "" || origin != "" {
			return nil, fmt.Errorf("%s current node contains Agent selection fields", kind)
		}
	default:
		return nil, fmt.Errorf("current node kind %q is invalid", kind)
	}
	return int64(1), nil
}

func migrationValidatePendingApprovalAgentExecution(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("%s requires 1 argument", migrationPendingApprovalAgentExecutionValidationFunction)
	}
	raw, err := migrationStringArgument(args[0], "pending approval target snapshot")
	if err != nil {
		return nil, err
	}
	var snapshot struct {
		NodeKind                workflow.NodeKind                 `json:"node_kind"`
		AgentExecutionSelection *workflow.AgentExecutionSelection `json:"agent_execution_selection,omitempty"`
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil, fmt.Errorf("decode pending approval target snapshot: %w", err)
	}
	switch snapshot.NodeKind {
	case workflow.NodeKindAgent:
		if snapshot.AgentExecutionSelection == nil {
			return nil, errors.New("pending approval Agent target selection is missing")
		}
		if err := snapshot.AgentExecutionSelection.Validate(); err != nil {
			return nil, fmt.Errorf("pending approval Agent target selection: %w", err)
		}
	case workflow.NodeKindStart, workflow.NodeKindScript, workflow.NodeKindJoin, workflow.NodeKindTerminal:
		if snapshot.AgentExecutionSelection != nil {
			return nil, fmt.Errorf("pending approval %s target contains Agent selection", snapshot.NodeKind)
		}
	default:
		return nil, fmt.Errorf("pending approval target kind %q is invalid", snapshot.NodeKind)
	}
	return int64(1), nil
}

func migrationIrreversibleMarker(
	functionName string,
) func(*sqlitedriver.FunctionContext, []driver.Value) (driver.Value, error) {
	return func(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
		if len(args) != 0 {
			return nil, fmt.Errorf("%s requires no arguments", functionName)
		}
		return nil, fmt.Errorf("metadata migration guarded by %s is irreversible", functionName)
	}
}

func migrationWorkflowIDBlob(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
	workflowID, err := migrationWorkflowIDArgument(args)
	if err != nil {
		return nil, err
	}
	value := make([]byte, len(workflowID))
	copy(value, workflowID[:])
	return value, nil
}

func migrationWorkflowIDText(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
	workflowID, err := migrationWorkflowIDArgument(args)
	if err != nil {
		return nil, err
	}
	return workflowID.String(), nil
}

func migrationWorkflowIDArgument(args []driver.Value) (uuid.UUID, error) {
	if len(args) != 2 {
		return uuid.Nil, fmt.Errorf("workflow ID migration function requires 2 arguments")
	}
	location, err := migrationStringArgument(args[1], "workflow identity location")
	if err != nil {
		return uuid.Nil, fmt.Errorf("workflow ID migration failure: %w", err)
	}
	location = strings.TrimSpace(location)
	if location == "" {
		return uuid.Nil, errors.New("workflow ID migration failure: workflow identity location is required")
	}
	raw, err := migrationStringArgument(args[0], "workflow ID")
	if err != nil {
		return uuid.Nil, &workflowIdentityMigrationDiagnostic{
			Location: location,
			Cause:    err,
		}
	}
	return parseMigrationWorkflowID(raw, location)
}

type workflowIdentityMigrationDiagnostic struct {
	Location string
	Cause    error
}

func (e *workflowIdentityMigrationDiagnostic) Error() string {
	return fmt.Sprintf("workflow ID migration failure at %s: %v", e.Location, e.Cause)
}

func (e *workflowIdentityMigrationDiagnostic) Unwrap() error { return e.Cause }

func parseMigrationWorkflowID(raw string, location string) (uuid.UUID, error) {
	if parsed, err := runtimeids.ParseCanonicalUUIDv4(raw, "workflow ID"); err == nil {
		return parsed, nil
	}
	const prefix = "workflow-"
	const canonicalUUIDTextLength = 36
	var parsed uuid.UUID
	var err error
	if len(raw) == len(prefix)+canonicalUUIDTextLength {
		var canonical [canonicalUUIDTextLength]byte
		matchesPrefix := true
		for index := 0; index < len(prefix); index++ {
			if raw[index] != prefix[index] {
				matchesPrefix = false
				break
			}
		}
		if matchesPrefix {
			for index := range canonical {
				canonical[index] = raw[len(prefix)+index]
			}
			parsed, err = runtimeids.ParseCanonicalUUIDv4(string(canonical[:]), "workflow ID")
		} else {
			err = fmt.Errorf("workflow ID must use the %q prefix followed by a canonical UUIDv4", prefix)
		}
	} else {
		err = fmt.Errorf("workflow ID must use the %q prefix followed by a canonical UUIDv4", prefix)
	}
	if err != nil {
		return uuid.Nil, &workflowIdentityMigrationDiagnostic{
			Location: location,
			Cause:    fmt.Errorf("must be a canonical UUIDv4 or workflow-prefixed canonical UUIDv4: %w", err),
		}
	}
	return parsed, nil
}

type migrationGraphEdge struct {
	EdgeID           string `json:"edge_id"`
	SnapshotPriority int    `json:"snapshot_priority"`
	TransitionKey    string `json:"transition_key"`
	SourceNodeID     string `json:"source_node_id"`
	SourceNodeKey    string `json:"source_node_key"`
	SourceNodeKind   string `json:"source_node_kind"`
	TargetNodeID     string `json:"target_node_id"`
	TargetNodeKey    string `json:"target_node_key"`
	TargetNodeKind   string `json:"target_node_kind"`
	PromptTemplate   string `json:"prompt_template"`
	ParametersJSON   string `json:"parameters_json"`
}

type migrationPriorValueCandidate struct {
	Scope              string `json:"scope"`
	NodeKey            string `json:"node_key"`
	TransitionKey      string `json:"transition_key"`
	OutputValuesJSON   string `json:"output_values_json"`
	AppliedAtUnixMs    int64  `json:"applied_at_unix_ms"`
	CreatedAtUnixMs    int64  `json:"created_at_unix_ms"`
	TransitionRecordID string `json:"transition_record_id"`
}

func migrationPriorParameters(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 7 {
		return nil, fmt.Errorf("%s requires 7 arguments", migrationPriorParametersFunction)
	}
	taskID, err := migrationStringArgument(args[0], "task id")
	if err != nil {
		return nil, err
	}
	nodeID, err := migrationStringArgument(args[1], "node id")
	if err != nil {
		return nil, err
	}
	branch := strings.TrimSpace(migrationOptionalStringArgument(args[2]))
	branchLabel := branch
	if branchLabel == "" {
		branchLabel = "serial"
	}
	context := fmt.Sprintf("task_id=%s, node_id=%s, transition_branch_key=%s", taskID, nodeID, branchLabel)
	currentNodeID, err := migrationStringArgument(args[3], "current node id")
	if err != nil {
		return nil, fmt.Errorf("prior-parameter migration failure: %s: %w", context, err)
	}
	graphJSON, err := migrationStringArgument(args[4], "workflow graph")
	if err != nil {
		return nil, fmt.Errorf("prior-parameter migration failure: %s: %w", context, err)
	}
	graph := []migrationGraphEdge{}
	if err := json.Unmarshal([]byte(graphJSON), &graph); err != nil {
		return nil, fmt.Errorf("prior-parameter migration failure: %s: decode workflow graph: %w", context, err)
	}
	requirements, err := migrationPriorParameterRequirements(currentNodeID, graph)
	if err != nil {
		return nil, fmt.Errorf("prior-parameter migration failure: %s: %w", context, err)
	}
	frozenParameterJSON, err := migrationStringArgument(args[5], "frozen prior-parameter values")
	if err != nil {
		return nil, fmt.Errorf("prior-parameter migration failure: %s: %w", context, err)
	}
	values, err := migrationFrozenPriorParameters(context, frozenParameterJSON)
	if err != nil {
		return nil, err
	}
	candidatesJSON, err := migrationStringArgument(args[6], "prior-parameter candidates")
	if err != nil {
		return nil, fmt.Errorf("prior-parameter migration failure: %s: %w", context, err)
	}
	candidates := []migrationPriorValueCandidate{}
	if err := json.Unmarshal([]byte(candidatesJSON), &candidates); err != nil {
		return nil, fmt.Errorf("prior-parameter migration failure: %s: decode candidates: %w", context, err)
	}
	for _, requirement := range requirements {
		valueKey := migrationPriorParameterRequirementKey(requirement)
		candidate, candidateFound, candidateErr := migrationLatestPriorParameterCandidate(branch != "", requirement, candidates)
		if candidateErr != nil {
			return nil, fmt.Errorf("prior-parameter migration failure: %s, value_key=%s: %w", context, valueKey, candidateErr)
		}
		frozenValue, frozenFound := values.TransitionParameter(requirement.TransitionKey, requirement.ParameterName)
		if frozenFound {
			if candidateFound && candidate != frozenValue {
				return nil, fmt.Errorf(
					"prior-parameter migration failure: %s, value_key=%s: frozen value conflicts with reconstructed value",
					context,
					valueKey,
				)
			}
			values.SetTransitionParameter(requirement.TransitionKey, requirement.ParameterName, frozenValue)
			continue
		}
		if !candidateFound {
			return nil, fmt.Errorf(
				"prior-parameter migration failure: %s, value_key=%s, provider_node_key=%s: required value is missing",
				context,
				valueKey,
				requirement.ProviderNode,
			)
		}
		values.SetTransitionParameter(requirement.TransitionKey, requirement.ParameterName, candidate)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("prior-parameter migration failure: %s: encode values: %w", context, err)
	}
	return string(encoded), nil
}

func migrationFrozenPriorParameters(context, frozenParameterJSON string) (workflow.MaterializedPriorValues, error) {
	transitionParameters, err := migrationFrozenPriorValueNamespace(context, "transition parameter", frozenParameterJSON)
	if err != nil {
		return workflow.MaterializedPriorValues{}, err
	}
	return workflow.MaterializedPriorValues{
		TransitionParameters: transitionParameters,
	}, nil
}

func migrationFrozenPriorValueNamespace(context, kind, raw string) (map[workflow.ModelKey]map[string]string, error) {
	decoded := map[string]map[string]string{}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("prior-parameter migration failure: %s: decode frozen values: %w", context, err)
	}
	values := make(map[workflow.ModelKey]map[string]string, len(decoded))
	for namespace, fields := range decoded {
		trimmedNamespace := strings.TrimSpace(namespace)
		if trimmedNamespace == "" {
			return nil, fmt.Errorf("prior-parameter migration failure: %s, value_key=<blank>: frozen %s key is required", context, kind)
		}
		if len(fields) == 0 {
			return nil, fmt.Errorf("prior-parameter migration failure: %s, value_key=%s: frozen %s values are empty", context, trimmedNamespace, kind)
		}
		values[workflow.ModelKey(trimmedNamespace)] = make(map[string]string, len(fields))
		for fieldName, value := range fields {
			trimmedFieldName := strings.TrimSpace(fieldName)
			if trimmedFieldName == "" {
				return nil, fmt.Errorf("prior-parameter migration failure: %s, value_key=%s.<blank>: frozen %s field name is required", context, trimmedNamespace, kind)
			}
			values[workflow.ModelKey(trimmedNamespace)][trimmedFieldName] = value
		}
	}
	return values, nil
}

func migrationPriorParameterRequirements(currentNodeID string, graph []migrationGraphEdge) ([]workflow.PriorTransitionParameterRequirement, error) {
	definition, priorReferencesByEdge, err := migrationWorkflowDefinition(graph)
	if err != nil {
		return nil, err
	}
	return workflow.DeriveWiringWithPriorParameterReferences(definition, priorReferencesByEdge).
		PriorParameterRequirementsForNode(workflow.NodeID(currentNodeID)), nil
}

func migrationWorkflowDefinition(graph []migrationGraphEdge) (workflow.Definition, map[workflow.EdgeID][]workflow.PromptPriorParameterReference, error) {
	migrationWorkflowID, err := runtimeids.ParseWorkflowID("00000000-0000-4000-8000-000000000001")
	if err != nil {
		return workflow.Definition{}, nil, fmt.Errorf("parse synthetic migration Workflow ID: %w", err)
	}
	graphByEdgeID := make(map[string]migrationGraphEdge, len(graph))
	for _, edge := range graph {
		edgeID := strings.TrimSpace(edge.EdgeID)
		if edgeID == "" {
			return workflow.Definition{}, nil, errors.New("workflow graph edge id is blank")
		}
		existing, exists := graphByEdgeID[edgeID]
		if !exists || edge.SnapshotPriority > existing.SnapshotPriority {
			graphByEdgeID[edgeID] = edge
			continue
		}
		if edge.SnapshotPriority == existing.SnapshotPriority && edge != existing {
			return workflow.Definition{}, nil, fmt.Errorf("workflow graph edge %q has conflicting snapshots", edgeID)
		}
	}
	edgeIDs := make([]string, 0, len(graphByEdgeID))
	for edgeID := range graphByEdgeID {
		edgeIDs = append(edgeIDs, edgeID)
	}
	sort.Strings(edgeIDs)
	nodesByID := make(map[workflow.NodeID]workflow.Node)
	groupsBySemanticKey := make(map[string]workflow.TransitionGroup)
	edges := make([]workflow.Edge, 0, len(edgeIDs))
	priorReferencesByEdge := make(map[workflow.EdgeID][]workflow.PromptPriorParameterReference, len(edgeIDs))
	for _, selectedEdgeID := range edgeIDs {
		edge := graphByEdgeID[selectedEdgeID]
		sourceNodeID := strings.TrimSpace(edge.SourceNodeID)
		sourceNodeKey := strings.TrimSpace(edge.SourceNodeKey)
		sourceNodeKind := workflow.NodeKind(strings.TrimSpace(edge.SourceNodeKind))
		targetNodeID := strings.TrimSpace(edge.TargetNodeID)
		targetNodeKey := strings.TrimSpace(edge.TargetNodeKey)
		targetNodeKind := workflow.NodeKind(strings.TrimSpace(edge.TargetNodeKind))
		transitionKey := strings.TrimSpace(edge.TransitionKey)
		edgeID := strings.TrimSpace(edge.EdgeID)
		if sourceNodeID == "" || sourceNodeKey == "" || targetNodeID == "" || targetNodeKey == "" {
			return workflow.Definition{}, nil, errors.New("workflow graph edge has an incomplete node")
		}
		if transitionKey == "" || edgeID == "" {
			return workflow.Definition{}, nil, errors.New("workflow graph edge has an incomplete transition")
		}
		if err := addMigrationWorkflowNode(nodesByID, migrationWorkflowID, sourceNodeID, sourceNodeKey, sourceNodeKind); err != nil {
			return workflow.Definition{}, nil, err
		}
		if err := addMigrationWorkflowNode(nodesByID, migrationWorkflowID, targetNodeID, targetNodeKey, targetNodeKind); err != nil {
			return workflow.Definition{}, nil, err
		}
		refs, err := migrationPriorParameterReferences(edge.PromptTemplate)
		if err != nil {
			return workflow.Definition{}, nil, fmt.Errorf("parse prompt for edge %s -> %s: %w", sourceNodeID, targetNodeID, err)
		}
		parameters := []workflow.Parameter{}
		if err := json.Unmarshal([]byte(edge.ParametersJSON), &parameters); err != nil {
			return workflow.Definition{}, nil, fmt.Errorf("decode parameters for edge %s -> %s: %w", sourceNodeID, targetNodeID, err)
		}
		groupSemanticKey := sourceNodeID + "\x00" + transitionKey
		group, exists := groupsBySemanticKey[groupSemanticKey]
		if !exists {
			group = workflow.TransitionGroup{
				WorkflowID:   migrationWorkflowID,
				ID:           workflow.TransitionGroupID(groupSemanticKey),
				SourceNodeID: workflow.NodeID(sourceNodeID),
				TransitionID: workflow.TransitionID(transitionKey),
			}
			groupsBySemanticKey[groupSemanticKey] = group
		}
		edges = append(edges, workflow.Edge{
			WorkflowID:        migrationWorkflowID,
			ID:                workflow.EdgeID(edgeID),
			TransitionGroupID: group.ID,
			TargetNodeID:      workflow.NodeID(targetNodeID),
			PromptTemplate:    edge.PromptTemplate,
			Parameters:        parameters,
		})
		priorReferencesByEdge[workflow.EdgeID(edgeID)] = refs
	}
	nodeIDs := make([]string, 0, len(nodesByID))
	for nodeID := range nodesByID {
		nodeIDs = append(nodeIDs, string(nodeID))
	}
	sort.Strings(nodeIDs)
	nodes := make([]workflow.Node, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		nodes = append(nodes, nodesByID[workflow.NodeID(nodeID)])
	}
	groups := make([]workflow.TransitionGroup, 0, len(groupsBySemanticKey))
	for _, group := range groupsBySemanticKey {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(left, right int) bool { return groups[left].ID < groups[right].ID })
	return workflow.Definition{
		ID:               migrationWorkflowID,
		Nodes:            nodes,
		TransitionGroups: groups,
		Edges:            edges,
	}, priorReferencesByEdge, nil
}

func migrationPriorParameterReferences(prompt string) ([]workflow.PromptPriorParameterReference, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, nil
	}
	tmpl, err := template.New("historical workflow prompt").Parse(prompt)
	if err != nil {
		return nil, err
	}
	refs := make([]workflow.PromptPriorParameterReference, 0)
	for _, parsed := range tmpl.Templates() {
		if parsed.Tree != nil {
			if err := walkMigrationPromptNode(parsed.Tree.Root, &refs); err != nil {
				return nil, err
			}
		}
	}
	return refs, nil
}

func walkMigrationPromptNode(node parse.Node, refs *[]workflow.PromptPriorParameterReference) error {
	switch typed := node.(type) {
	case nil:
		return nil
	case *parse.ListNode:
		for _, child := range typed.Nodes {
			if err := walkMigrationPromptNode(child, refs); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return walkMigrationPromptNode(typed.Pipe, refs)
	case *parse.IfNode:
		if err := walkMigrationPromptNode(typed.Pipe, refs); err != nil {
			return err
		}
		if err := walkMigrationPromptNode(typed.List, refs); err != nil {
			return err
		}
		return walkMigrationPromptNode(typed.ElseList, refs)
	case *parse.RangeNode:
		if err := walkMigrationPromptNode(typed.Pipe, refs); err != nil {
			return err
		}
		if err := walkMigrationPromptNode(typed.List, refs); err != nil {
			return err
		}
		return walkMigrationPromptNode(typed.ElseList, refs)
	case *parse.WithNode:
		if err := walkMigrationPromptNode(typed.Pipe, refs); err != nil {
			return err
		}
		if err := walkMigrationPromptNode(typed.List, refs); err != nil {
			return err
		}
		return walkMigrationPromptNode(typed.ElseList, refs)
	case *parse.TemplateNode:
		return walkMigrationPromptNode(typed.Pipe, refs)
	case *parse.PipeNode:
		for _, command := range typed.Cmds {
			if err := walkMigrationPromptNode(command, refs); err != nil {
				return err
			}
		}
	case *parse.CommandNode:
		if len(typed.Args) > 0 {
			if ident, ok := typed.Args[0].(*parse.IdentifierNode); ok &&
				ident.Ident == "index" &&
				indexCommandTouchesHistoricalPromptNamespace(typed.Args[1:]) {
				return errors.New("historical dynamic prompt reference lookup is unsupported")
			}
		}
		for _, arg := range typed.Args {
			if err := walkMigrationPromptNode(arg, refs); err != nil {
				return err
			}
		}
	case *parse.ChainNode:
		if err := walkMigrationPromptNode(typed.Node, refs); err != nil {
			return err
		}
		if len(typed.Field) > 0 && historicalPromptNamespace(typed.Field[0]) {
			return errors.New("historical chained prompt references are unsupported")
		}
	case *parse.FieldNode:
		if err := migrationPromptFieldReference(typed.Ident, refs); err != nil {
			return err
		}
	case *parse.VariableNode:
		if variableTouchesHistoricalPromptNamespace(typed.Ident) {
			return errors.New("historical variable prompt references are unsupported")
		}
	}
	return nil
}

func variableTouchesHistoricalPromptNamespace(ident []string) bool {
	for _, part := range ident {
		if historicalPromptNamespace(part) {
			return true
		}
	}
	return false
}

func historicalPromptNamespace(value string) bool {
	return value == "Inputs" || value == "Params" || value == "$Inputs" || value == "$Params"
}

func indexCommandTouchesHistoricalPromptNamespace(args []parse.Node) bool {
	if len(args) == 0 {
		return false
	}
	if _, ok := args[0].(*parse.DotNode); ok {
		return true
	}
	for _, arg := range args {
		switch typed := arg.(type) {
		case *parse.FieldNode:
			if len(typed.Ident) > 0 && historicalPromptNamespace(typed.Ident[0]) {
				return true
			}
		case *parse.ChainNode:
			if len(typed.Field) > 0 && historicalPromptNamespace(typed.Field[0]) {
				return true
			}
			if indexCommandTouchesHistoricalPromptNamespace([]parse.Node{typed.Node}) {
				return true
			}
		case *parse.VariableNode:
			if variableTouchesHistoricalPromptNamespace(typed.Ident) {
				return true
			}
		case *parse.StringNode:
			if historicalPromptNamespace(typed.Text) {
				return true
			}
		}
	}
	return false
}

func migrationPromptFieldReference(ident []string, refs *[]workflow.PromptPriorParameterReference) error {
	if len(ident) == 0 {
		return errors.New("historical prompt reference is empty")
	}
	switch ident[0] {
	case "TaskId", "TaskShortId", "TaskTitle", "TaskBody", "NodeId", "NodeKey", "NodeDisplayName":
		if len(ident) != 1 {
			return fmt.Errorf("historical prompt built-in %q is chained", ident[0])
		}
	case "Inputs":
		if len(ident) != 2 || strings.TrimSpace(ident[1]) == "" {
			return errors.New("historical .Inputs reference must use .Inputs.<input_name>")
		}
	case "Params":
		switch len(ident) {
		case 2:
			if strings.TrimSpace(ident[1]) == "" {
				return errors.New("historical .Params reference has an empty parameter key")
			}
		case 3:
			if strings.TrimSpace(ident[1]) == "" || strings.TrimSpace(ident[2]) == "" {
				return errors.New("historical prior .Params reference has an empty key")
			}
			*refs = append(*refs, workflow.PromptPriorParameterReference{
				TransitionKey: workflow.ModelKey(ident[1]),
				ParameterKey:  ident[2],
				Placeholder:   "." + strings.Join(ident, "."),
			})
		default:
			return errors.New("historical .Params reference has an unsupported shape")
		}
	default:
		return fmt.Errorf("historical prompt reference .%s is unsupported", strings.Join(ident, "."))
	}
	return nil
}

func addMigrationWorkflowNode(
	nodesByID map[workflow.NodeID]workflow.Node,
	workflowID runtimeids.WorkflowID,
	nodeID string,
	nodeKey string,
	kind workflow.NodeKind,
) error {
	id := workflow.NodeID(nodeID)
	if existing, exists := nodesByID[id]; exists {
		if workflow.NodeKey(existing) != workflow.ModelKey(nodeKey) || existing.Kind() != kind {
			return fmt.Errorf("workflow graph node %q has conflicting identity", nodeID)
		}
		return nil
	}
	node, err := workflow.NewNode(
		workflow.NodeIdentity{WorkflowID: workflowID, ID: id, Key: workflow.ModelKey(nodeKey), DisplayName: nodeKey},
		kind,
		workflow.NodeFields{},
	)
	if err != nil {
		return fmt.Errorf("decode workflow graph node %q: %w", nodeID, err)
	}
	nodesByID[id] = node
	return nil
}

func migrationLatestPriorParameterCandidate(
	branchScoped bool,
	requirement workflow.PriorTransitionParameterRequirement,
	candidates []migrationPriorValueCandidate,
) (string, bool, error) {
	outputName := requirement.ParameterName
	for _, scope := range migrationPriorCandidateScopes(branchScoped) {
		var selected *migrationPriorValueCandidate
		var selectedValue string
		for index := range candidates {
			candidate := &candidates[index]
			if candidate.Scope != scope || !migrationCandidateProvidesPriorParameter(*candidate, requirement) {
				continue
			}
			outputs := map[string]string{}
			if err := json.Unmarshal([]byte(candidate.OutputValuesJSON), &outputs); err != nil {
				return "", false, fmt.Errorf("transition %q has malformed output values: %w", candidate.TransitionRecordID, err)
			}
			value, exists := outputs[outputName]
			if !exists {
				continue
			}
			if selected == nil ||
				candidate.AppliedAtUnixMs > selected.AppliedAtUnixMs ||
				(candidate.AppliedAtUnixMs == selected.AppliedAtUnixMs && candidate.CreatedAtUnixMs > selected.CreatedAtUnixMs) {
				selected = candidate
				selectedValue = value
				continue
			}
			if candidate.AppliedAtUnixMs == selected.AppliedAtUnixMs &&
				candidate.CreatedAtUnixMs == selected.CreatedAtUnixMs {
				return "", false, fmt.Errorf(
					"transitions %q and %q have an ordering tie",
					selected.TransitionRecordID,
					candidate.TransitionRecordID,
				)
			}
		}
		if selected != nil {
			return selectedValue, true, nil
		}
	}
	return "", false, nil
}

func migrationCandidateProvidesPriorParameter(candidate migrationPriorValueCandidate, requirement workflow.PriorTransitionParameterRequirement) bool {
	return strings.TrimSpace(candidate.NodeKey) == strings.TrimSpace(string(requirement.ProviderNode)) &&
		strings.TrimSpace(candidate.TransitionKey) == strings.TrimSpace(string(requirement.TransitionKey))
}

func migrationPriorParameterRequirementKey(requirement workflow.PriorTransitionParameterRequirement) string {
	return ".Params." + string(requirement.TransitionKey) + "." + requirement.ParameterName
}

func migrationPriorCandidateScopes(branchScoped bool) []string {
	if branchScoped {
		return []string{"branch", "task"}
	}
	return []string{"task"}
}

func migrationCurrentInputValues(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 10 {
		return nil, fmt.Errorf("%s requires 10 arguments", migrationCurrentInputValuesFunction)
	}
	taskID, err := migrationStringArgument(args[0], "task id")
	if err != nil {
		return nil, err
	}
	nodeID, err := migrationStringArgument(args[1], "node id")
	if err != nil {
		return nil, err
	}
	branch := strings.TrimSpace(migrationOptionalStringArgument(args[2]))
	branchLabel := branch
	if branchLabel == "" {
		branchLabel = "serial"
	}
	context := fmt.Sprintf("task_id=%s, node_id=%s, transition_branch_key=%s", taskID, nodeID, branchLabel)
	bindingsJSON, err := migrationStringArgument(args[3], "input bindings")
	if err != nil {
		return nil, fmt.Errorf("current input migration failure: %s: %w", context, err)
	}
	bindings, err := decodeLegacyMigrationInputBindings(bindingsJSON)
	if err != nil {
		return nil, fmt.Errorf("current input migration failure: %s: %w", context, err)
	}
	outputValuesJSON, err := migrationStringArgument(args[4], "transition output values")
	if err != nil {
		return nil, fmt.Errorf("current input migration failure: %s: %w", context, err)
	}
	outputValues := map[string]string{}
	if err := json.Unmarshal([]byte(outputValuesJSON), &outputValues); err != nil {
		return nil, fmt.Errorf("current input migration failure: %s: decode transition output values: %w", context, err)
	}
	taskFields := map[string]string{
		"short_id":   migrationOptionalStringArgument(args[6]),
		"title":      migrationOptionalStringArgument(args[7]),
		"body":       migrationOptionalStringArgument(args[8]),
		"source_url": migrationOptionalStringArgument(args[9]),
	}
	commentary := migrationOptionalStringArgument(args[5])
	values := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		name := strings.TrimSpace(binding.Name)
		field := strings.TrimSpace(binding.Field)
		if name == "" {
			return nil, fmt.Errorf("current input migration failure: %s, value_key=<blank>: input binding name is required", context)
		}
		if field == "" {
			return nil, fmt.Errorf("current input migration failure: %s, value_key=%s: input binding field is required", context, name)
		}
		if _, duplicate := values[name]; duplicate {
			return nil, fmt.Errorf("current input migration failure: %s, value_key=%s: duplicate input binding", context, name)
		}
		switch binding.Source {
		case workflow.BindingSourceTask:
			value, exists := taskFields[field]
			if !exists {
				return nil, fmt.Errorf("current input migration failure: %s, value_key=%s: unsupported task field %q", context, name, field)
			}
			values[name] = value
		case workflow.BindingSourceTransitionOutput, workflow.BindingSourceJoin:
			if binding.Source == workflow.BindingSourceTransitionOutput && field == workflow.RuntimePromptParameterCommentary {
				values[name] = commentary
				continue
			}
			value, exists := outputValues[field]
			if !exists {
				return nil, fmt.Errorf("current input migration failure: %s, value_key=%s: transition output %q is missing", context, name, field)
			}
			values[name] = value
		default:
			return nil, fmt.Errorf("current input migration failure: %s, value_key=%s: unsupported binding source %q", context, name, binding.Source)
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("current input migration failure: %s: encode values: %w", context, err)
	}
	return string(encoded), nil
}

func decodeLegacyMigrationInputBindings(raw string) ([]workflow.InputBinding, error) {
	bindings := []workflow.InputBinding{}
	if err := json.Unmarshal([]byte(raw), &bindings); err == nil {
		return bindings, nil
	}
	legacyEmpty := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(raw), &legacyEmpty); err != nil {
		return nil, fmt.Errorf("decode input bindings: %w", err)
	}
	if len(legacyEmpty) != 0 {
		return nil, errors.New("input bindings must be an array")
	}
	return bindings, nil
}

func migrationStringArgument(value driver.Value, label string) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case []byte:
		return string(typed), nil
	default:
		return "", fmt.Errorf("%s must be text", label)
	}
}

func migrationOptionalStringArgument(value driver.Value) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

func migrationBooleanArgument(value driver.Value, label string) (bool, error) {
	switch typed := value.(type) {
	case int64:
		switch typed {
		case 0:
			return false, nil
		case 1:
			return true, nil
		default:
			return false, fmt.Errorf("%s must be 0 or 1", label)
		}
	case int:
		switch typed {
		case 0:
			return false, nil
		case 1:
			return true, nil
		default:
			return false, fmt.Errorf("%s must be 0 or 1", label)
		}
	default:
		return false, fmt.Errorf("%s must be an integer", label)
	}
}
