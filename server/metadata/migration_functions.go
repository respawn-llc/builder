package metadata

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"core/server/workflow"

	sqlitedriver "modernc.org/sqlite"
)

const migrationCurrentInputValuesFunction = "kent_migration_current_input_values_v1"
const migrationPriorValuesFunction = "kent_migration_prior_node_values_v1"
const migrationReclassifyPriorValuesFunction = "kent_migration_reclassify_prior_values_v1"

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
			migrationPriorValuesFunction,
			8,
			migrationPriorValues,
		)
		if registerMetadataSQLiteFunctionsErr != nil {
			return
		}
		registerMetadataSQLiteFunctionsErr = sqlitedriver.RegisterDeterministicScalarFunction(
			migrationReclassifyPriorValuesFunction,
			5,
			migrationReclassifyPriorValues,
		)
	})
	if registerMetadataSQLiteFunctionsErr != nil {
		return fmt.Errorf("register metadata SQLite migration functions: %w", registerMetadataSQLiteFunctionsErr)
	}
	return nil
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

func migrationPriorValues(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 8 {
		return nil, fmt.Errorf("%s requires 8 arguments", migrationPriorValuesFunction)
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
		return nil, fmt.Errorf("prior-node migration failure: %s: %w", context, err)
	}
	graphJSON, err := migrationStringArgument(args[4], "workflow graph")
	if err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: %w", context, err)
	}
	graph := []migrationGraphEdge{}
	if err := json.Unmarshal([]byte(graphJSON), &graph); err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: decode workflow graph: %w", context, err)
	}
	requirements, err := migrationPriorValueRequirements(currentNodeID, graph)
	if err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: %w", context, err)
	}
	frozenNodeJSON, err := migrationStringArgument(args[5], "frozen prior-node values")
	if err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: %w", context, err)
	}
	frozenParameterJSON, err := migrationStringArgument(args[6], "frozen prior-parameter values")
	if err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: %w", context, err)
	}
	values, err := migrationFrozenPriorValues(context, frozenNodeJSON, frozenParameterJSON)
	if err != nil {
		return nil, err
	}
	candidatesJSON, err := migrationStringArgument(args[7], "prior-node candidates")
	if err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: %w", context, err)
	}
	candidates := []migrationPriorValueCandidate{}
	if err := json.Unmarshal([]byte(candidatesJSON), &candidates); err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: decode candidates: %w", context, err)
	}
	for _, requirement := range requirements {
		valueKey := migrationPriorValueRequirementKey(requirement)
		candidate, candidateFound, candidateErr := migrationLatestPriorValueCandidate(branch != "", requirement, candidates)
		if candidateErr != nil {
			return nil, fmt.Errorf("prior-node migration failure: %s, value_key=%s: %w", context, valueKey, candidateErr)
		}
		frozenValue, frozenFound := values.Value(requirement)
		if frozenFound {
			if candidateFound && candidate != frozenValue {
				return nil, fmt.Errorf(
					"prior-node migration failure: %s, value_key=%s: frozen value conflicts with reconstructed value",
					context,
					valueKey,
				)
			}
			values.Set(requirement, frozenValue)
			continue
		}
		if !candidateFound {
			return nil, fmt.Errorf(
				"prior-node migration failure: %s, value_key=%s, provider_node_key=%s: required value is missing",
				context,
				valueKey,
				requirement.ProviderNodeKey(),
			)
		}
		values.Set(requirement, candidate)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: encode values: %w", context, err)
	}
	return string(encoded), nil
}

func migrationFrozenPriorValues(context, frozenNodeJSON, frozenParameterJSON string) (workflow.MaterializedPriorValues, error) {
	nodeOutputs, err := migrationFrozenPriorValueNamespace(context, "node output", frozenNodeJSON)
	if err != nil {
		return workflow.MaterializedPriorValues{}, err
	}
	transitionParameters, err := migrationFrozenPriorValueNamespace(context, "transition parameter", frozenParameterJSON)
	if err != nil {
		return workflow.MaterializedPriorValues{}, err
	}
	return workflow.MaterializedPriorValues{
		NodeOutputs:          nodeOutputs,
		TransitionParameters: transitionParameters,
	}, nil
}

func migrationFrozenPriorValueNamespace(context, kind, raw string) (map[workflow.ModelKey]map[string]string, error) {
	decoded := map[string]map[string]string{}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: decode frozen values: %w", context, err)
	}
	values := make(map[workflow.ModelKey]map[string]string, len(decoded))
	for namespace, fields := range decoded {
		trimmedNamespace := strings.TrimSpace(namespace)
		if trimmedNamespace == "" {
			return nil, fmt.Errorf("prior-node migration failure: %s, value_key=<blank>: frozen %s key is required", context, kind)
		}
		if len(fields) == 0 {
			return nil, fmt.Errorf("prior-node migration failure: %s, value_key=%s: frozen %s values are empty", context, trimmedNamespace, kind)
		}
		values[workflow.ModelKey(trimmedNamespace)] = make(map[string]string, len(fields))
		for fieldName, value := range fields {
			trimmedFieldName := strings.TrimSpace(fieldName)
			if trimmedFieldName == "" {
				return nil, fmt.Errorf("prior-node migration failure: %s, value_key=%s.<blank>: frozen %s field name is required", context, trimmedNamespace, kind)
			}
			values[workflow.ModelKey(trimmedNamespace)][trimmedFieldName] = value
		}
	}
	return values, nil
}

func migrationPriorValueRequirements(currentNodeID string, graph []migrationGraphEdge) ([]workflow.PriorValueRequirement, error) {
	definition, err := migrationWorkflowDefinition(graph)
	if err != nil {
		return nil, err
	}
	return workflow.DeriveWiring(definition).PriorValueRequirementsForNode(workflow.NodeID(currentNodeID)), nil
}

func migrationWorkflowDefinition(graph []migrationGraphEdge) (workflow.Definition, error) {
	const migrationWorkflowID workflow.WorkflowID = "migration-workflow"
	graphByEdgeID := make(map[string]migrationGraphEdge, len(graph))
	for _, edge := range graph {
		edgeID := strings.TrimSpace(edge.EdgeID)
		if edgeID == "" {
			return workflow.Definition{}, errors.New("workflow graph edge id is blank")
		}
		existing, exists := graphByEdgeID[edgeID]
		if !exists || edge.SnapshotPriority > existing.SnapshotPriority {
			graphByEdgeID[edgeID] = edge
			continue
		}
		if edge.SnapshotPriority == existing.SnapshotPriority && edge != existing {
			return workflow.Definition{}, fmt.Errorf("workflow graph edge %q has conflicting snapshots", edgeID)
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
			return workflow.Definition{}, errors.New("workflow graph edge has an incomplete node")
		}
		if transitionKey == "" || edgeID == "" {
			return workflow.Definition{}, errors.New("workflow graph edge has an incomplete transition")
		}
		if err := addMigrationWorkflowNode(nodesByID, migrationWorkflowID, sourceNodeID, sourceNodeKey, sourceNodeKind); err != nil {
			return workflow.Definition{}, err
		}
		if err := addMigrationWorkflowNode(nodesByID, migrationWorkflowID, targetNodeID, targetNodeKey, targetNodeKind); err != nil {
			return workflow.Definition{}, err
		}
		refs, err := workflow.ExtractPromptTemplateReferences(edge.PromptTemplate)
		if err != nil {
			return workflow.Definition{}, fmt.Errorf("parse prompt for edge %s -> %s: %w", sourceNodeID, targetNodeID, err)
		}
		if len(refs.Invalid) != 0 {
			return workflow.Definition{}, fmt.Errorf("prompt for edge %s -> %s has invalid reference %q", sourceNodeID, targetNodeID, refs.Invalid[0].Placeholder)
		}
		parameters := []workflow.Parameter{}
		if err := json.Unmarshal([]byte(edge.ParametersJSON), &parameters); err != nil {
			return workflow.Definition{}, fmt.Errorf("decode parameters for edge %s -> %s: %w", sourceNodeID, targetNodeID, err)
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
	}, nil
}

func addMigrationWorkflowNode(
	nodesByID map[workflow.NodeID]workflow.Node,
	workflowID workflow.WorkflowID,
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

func migrationLatestPriorValueCandidate(
	branchScoped bool,
	requirement workflow.PriorValueRequirement,
	candidates []migrationPriorValueCandidate,
) (string, bool, error) {
	outputName := requirement.ValueName()
	for _, scope := range migrationPriorCandidateScopes(branchScoped) {
		var selected *migrationPriorValueCandidate
		var selectedValue string
		for index := range candidates {
			candidate := &candidates[index]
			if candidate.Scope != scope || !migrationCandidateProvidesRequirement(*candidate, requirement) {
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

func migrationCandidateProvidesRequirement(candidate migrationPriorValueCandidate, requirement workflow.PriorValueRequirement) bool {
	if strings.TrimSpace(candidate.NodeKey) != strings.TrimSpace(string(requirement.ProviderNodeKey())) {
		return false
	}
	switch requirement.Origin() {
	case workflow.PriorValueOriginNodeOutput:
		return true
	case workflow.PriorValueOriginTransitionParameter:
		return strings.TrimSpace(candidate.TransitionKey) == strings.TrimSpace(string(requirement.Namespace()))
	default:
		panic(fmt.Sprintf("unsupported prior value origin %q", requirement.Origin()))
	}
}

func migrationPriorValueRequirementKey(requirement workflow.PriorValueRequirement) string {
	switch requirement.Origin() {
	case workflow.PriorValueOriginNodeOutput:
		return ".Nodes." + string(requirement.Namespace()) + "." + requirement.ValueName()
	case workflow.PriorValueOriginTransitionParameter:
		return ".Params." + string(requirement.Namespace()) + "." + requirement.ValueName()
	default:
		panic(fmt.Sprintf("unsupported prior value origin %q", requirement.Origin()))
	}
}

type migrationFlatPriorValueKey struct {
	Namespace string
	FieldName string
}

type migrationPriorValueOrigin uint8

const (
	migrationPriorValueOriginNodeOutput migrationPriorValueOrigin = 1 << iota
	migrationPriorValueOriginTransitionParameter
)

func migrationReclassifyPriorValues(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 5 {
		return nil, fmt.Errorf("%s requires 5 arguments", migrationReclassifyPriorValuesFunction)
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
	rawValues, err := migrationStringArgument(args[3], "prior values")
	if err != nil {
		return nil, fmt.Errorf("prior-value origin migration failure: %s: %w", context, err)
	}
	if typed, present, err := migrationDecodeTypedPriorValues(context, rawValues); present || err != nil {
		if err != nil {
			return nil, err
		}
		return migrationEncodePriorValues(context, typed)
	}
	graphJSON, err := migrationStringArgument(args[4], "workflow graph")
	if err != nil {
		return nil, fmt.Errorf("prior-value origin migration failure: %s: %w", context, err)
	}
	graph := []migrationGraphEdge{}
	if err := json.Unmarshal([]byte(graphJSON), &graph); err != nil {
		return nil, fmt.Errorf("prior-value origin migration failure: %s: decode workflow graph: %w", context, err)
	}
	definition, err := migrationWorkflowDefinition(graph)
	if err != nil {
		return nil, fmt.Errorf("prior-value origin migration failure: %s: %w", context, err)
	}
	flat := map[string]map[string]string{}
	if err := json.Unmarshal([]byte(rawValues), &flat); err != nil {
		return nil, fmt.Errorf("prior-value origin migration failure: %s: decode flat values: %w", context, err)
	}
	if flat == nil {
		return nil, fmt.Errorf("prior-value origin migration failure: %s: flat values must be an object", context)
	}
	currentOrigins := migrationPriorValueOriginsForNode(definition, workflow.NodeID(nodeID))
	allOrigins := migrationAllPriorValueOrigins(definition)
	nodeKeys := make(map[string]struct{}, len(definition.Nodes))
	for _, node := range definition.Nodes {
		nodeKeys[string(workflow.NodeKey(node))] = struct{}{}
	}
	transitionKeys := make(map[string]struct{}, len(definition.TransitionGroups))
	for _, group := range definition.TransitionGroups {
		transitionKeys[string(group.TransitionID)] = struct{}{}
	}
	values := workflow.MaterializedPriorValues{
		NodeOutputs:          map[workflow.ModelKey]map[string]string{},
		TransitionParameters: map[workflow.ModelKey]map[string]string{},
	}
	namespaces := make([]string, 0, len(flat))
	for namespace := range flat {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	for _, namespace := range namespaces {
		trimmedNamespace := strings.TrimSpace(namespace)
		if trimmedNamespace == "" {
			return nil, fmt.Errorf("prior-value origin migration failure: %s, value_key=<blank>: namespace is required", context)
		}
		fields := flat[namespace]
		if len(fields) == 0 {
			return nil, fmt.Errorf("prior-value origin migration failure: %s, value_key=%s: values are empty", context, trimmedNamespace)
		}
		fieldNames := make([]string, 0, len(fields))
		for fieldName := range fields {
			fieldNames = append(fieldNames, fieldName)
		}
		sort.Strings(fieldNames)
		for _, fieldName := range fieldNames {
			trimmedFieldName := strings.TrimSpace(fieldName)
			if trimmedFieldName == "" {
				return nil, fmt.Errorf("prior-value origin migration failure: %s, value_key=%s.<blank>: field name is required", context, trimmedNamespace)
			}
			key := migrationFlatPriorValueKey{Namespace: trimmedNamespace, FieldName: trimmedFieldName}
			origin := currentOrigins[key]
			if origin == 0 {
				origin = allOrigins[key]
			}
			if origin == 0 {
				_, nodeKeyExists := nodeKeys[trimmedNamespace]
				_, transitionKeyExists := transitionKeys[trimmedNamespace]
				if nodeKeyExists {
					origin |= migrationPriorValueOriginNodeOutput
				}
				if transitionKeyExists {
					origin |= migrationPriorValueOriginTransitionParameter
				}
			}
			switch origin {
			case migrationPriorValueOriginNodeOutput:
				values.SetNodeOutput(workflow.ModelKey(trimmedNamespace), trimmedFieldName, fields[fieldName])
			case migrationPriorValueOriginTransitionParameter:
				values.SetTransitionParameter(workflow.ModelKey(trimmedNamespace), trimmedFieldName, fields[fieldName])
			case migrationPriorValueOriginNodeOutput | migrationPriorValueOriginTransitionParameter:
				return nil, fmt.Errorf(
					"prior-value origin migration failure: %s, value_key=%s.%s: flat value collides between Node output and Transition parameter origins",
					context,
					trimmedNamespace,
					trimmedFieldName,
				)
			default:
				return nil, fmt.Errorf(
					"prior-value origin migration failure: %s, value_key=%s.%s: flat value origin cannot be determined",
					context,
					trimmedNamespace,
					trimmedFieldName,
				)
			}
		}
	}
	return migrationEncodePriorValues(context, values)
}

func migrationDecodeTypedPriorValues(context, raw string) (workflow.MaterializedPriorValues, bool, error) {
	object := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return workflow.MaterializedPriorValues{}, false, nil
	}
	_, hasNodeOutputs := object["node_outputs"]
	_, hasTransitionParameters := object["transition_parameters"]
	if !hasNodeOutputs && !hasTransitionParameters {
		return workflow.MaterializedPriorValues{}, false, nil
	}
	if !hasNodeOutputs || !hasTransitionParameters || len(object) != 2 {
		return workflow.MaterializedPriorValues{}, true, fmt.Errorf(
			"prior-value origin migration failure: %s: typed prior values must contain exactly node_outputs and transition_parameters",
			context,
		)
	}
	values := workflow.MaterializedPriorValues{}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return workflow.MaterializedPriorValues{}, true, fmt.Errorf("prior-value origin migration failure: %s: decode typed values: %w", context, err)
	}
	if err := migrationValidatePriorValues(context, values); err != nil {
		return workflow.MaterializedPriorValues{}, true, err
	}
	return values, true, nil
}

func migrationPriorValueOriginsForNode(definition workflow.Definition, nodeID workflow.NodeID) map[migrationFlatPriorValueKey]migrationPriorValueOrigin {
	return migrationPriorValueOrigins(workflow.DeriveWiring(definition).PriorValueRequirementsForNode(nodeID))
}

func migrationAllPriorValueOrigins(definition workflow.Definition) map[migrationFlatPriorValueKey]migrationPriorValueOrigin {
	origins := map[migrationFlatPriorValueKey]migrationPriorValueOrigin{}
	derived := workflow.DeriveWiring(definition)
	for _, node := range definition.Nodes {
		for key, origin := range migrationPriorValueOrigins(derived.PriorValueRequirementsForNode(workflow.NodeIDOf(node))) {
			origins[key] |= origin
		}
	}
	return origins
}

func migrationPriorValueOrigins(requirements []workflow.PriorValueRequirement) map[migrationFlatPriorValueKey]migrationPriorValueOrigin {
	origins := make(map[migrationFlatPriorValueKey]migrationPriorValueOrigin, len(requirements))
	for _, requirement := range requirements {
		key := migrationFlatPriorValueKey{Namespace: string(requirement.Namespace()), FieldName: requirement.ValueName()}
		switch requirement.Origin() {
		case workflow.PriorValueOriginNodeOutput:
			origins[key] |= migrationPriorValueOriginNodeOutput
		case workflow.PriorValueOriginTransitionParameter:
			origins[key] |= migrationPriorValueOriginTransitionParameter
		default:
			panic(fmt.Sprintf("unsupported prior value origin %q", requirement.Origin()))
		}
	}
	return origins
}

func migrationEncodePriorValues(context string, values workflow.MaterializedPriorValues) (driver.Value, error) {
	if err := migrationValidatePriorValues(context, values); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("prior-value origin migration failure: %s: encode values: %w", context, err)
	}
	return string(encoded), nil
}

func migrationValidatePriorValues(context string, values workflow.MaterializedPriorValues) error {
	if values.NodeOutputs == nil || values.TransitionParameters == nil {
		return fmt.Errorf("prior-value origin migration failure: %s: typed value namespaces must be objects", context)
	}
	if err := migrationValidatePriorValueNamespace(context, "node output", values.NodeOutputs); err != nil {
		return err
	}
	if err := migrationValidatePriorValueNamespace(context, "transition parameter", values.TransitionParameters); err != nil {
		return err
	}
	return nil
}

func migrationValidatePriorValueNamespace(context, kind string, namespace map[workflow.ModelKey]map[string]string) error {
	for key, fields := range namespace {
		if strings.TrimSpace(string(key)) == "" {
			return fmt.Errorf("prior-value origin migration failure: %s: %s key is required", context, kind)
		}
		if len(fields) == 0 {
			return fmt.Errorf("prior-value origin migration failure: %s, value_key=%s: %s values are empty", context, key, kind)
		}
		for fieldName := range fields {
			if strings.TrimSpace(fieldName) == "" {
				return fmt.Errorf("prior-value origin migration failure: %s, value_key=%s.<blank>: %s field name is required", context, key, kind)
			}
		}
	}
	return nil
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
