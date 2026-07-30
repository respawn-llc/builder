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
const migrationPriorNodeValuesFunction = "kent_migration_prior_node_values_v1"

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
			migrationPriorNodeValuesFunction,
			8,
			migrationPriorNodeValues,
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

func migrationPriorNodeValues(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 8 {
		return nil, fmt.Errorf("%s requires 8 arguments", migrationPriorNodeValuesFunction)
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
	requirements, err := migrationPriorNodeRequirements(currentNodeID, graph)
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
	frozen, err := migrationFrozenPriorValues(context, frozenNodeJSON, frozenParameterJSON)
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
	values := frozen
	for _, requirement := range requirements {
		namespace := strings.TrimSpace(string(requirement.Namespace))
		providerNodeKey := strings.TrimSpace(string(requirement.ProviderNodeKey))
		outputName := strings.TrimSpace(requirement.OutputName)
		valueKey := namespace + "." + outputName
		candidate, candidateFound, candidateErr := migrationLatestPriorNodeCandidate(branch != "", requirement, candidates)
		if candidateErr != nil {
			return nil, fmt.Errorf("prior-node migration failure: %s, value_key=%s: %w", context, valueKey, candidateErr)
		}
		frozenValue, frozenFound := frozen[namespace][outputName]
		if frozenFound {
			if candidateFound && candidate != frozenValue {
				return nil, fmt.Errorf(
					"prior-node migration failure: %s, value_key=%s: frozen value conflicts with reconstructed value",
					context,
					valueKey,
				)
			}
			if values[namespace] == nil {
				values[namespace] = make(map[string]string)
			}
			values[namespace][outputName] = frozenValue
			continue
		}
		if !candidateFound {
			providerTransitionKey := ""
			if requirement.ProviderTransitionKey != nil {
				providerTransitionKey = string(*requirement.ProviderTransitionKey)
			}
			return nil, fmt.Errorf(
				"prior-node migration failure: %s, value_key=%s, provider_node_key=%s, provider_transition_key=%s: required value is missing",
				context,
				valueKey,
				providerNodeKey,
				providerTransitionKey,
			)
		}
		if values[namespace] == nil {
			values[namespace] = make(map[string]string)
		}
		values[namespace][outputName] = candidate
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: encode values: %w", context, err)
	}
	return string(encoded), nil
}

func migrationFrozenPriorValues(context string, frozenSources ...string) (map[string]map[string]string, error) {
	values := map[string]map[string]string{}
	for _, frozenJSON := range frozenSources {
		frozen := map[string]map[string]string{}
		if err := json.Unmarshal([]byte(frozenJSON), &frozen); err != nil {
			return nil, fmt.Errorf("prior-node migration failure: %s: decode frozen values: %w", context, err)
		}
		for namespace, outputValues := range frozen {
			trimmedNamespace := strings.TrimSpace(namespace)
			if trimmedNamespace == "" {
				return nil, fmt.Errorf("prior-node migration failure: %s, value_key=<blank>: frozen node key is required", context)
			}
			if len(outputValues) == 0 {
				return nil, fmt.Errorf("prior-node migration failure: %s, value_key=%s: frozen node values are empty", context, trimmedNamespace)
			}
			if values[trimmedNamespace] == nil {
				values[trimmedNamespace] = make(map[string]string, len(outputValues))
			}
			for outputName, value := range outputValues {
				trimmedOutputName := strings.TrimSpace(outputName)
				if trimmedOutputName == "" {
					return nil, fmt.Errorf("prior-node migration failure: %s, value_key=%s.<blank>: frozen output name is required", context, trimmedNamespace)
				}
				if existing, exists := values[trimmedNamespace][trimmedOutputName]; exists && existing != value {
					return nil, fmt.Errorf(
						"prior-node migration failure: %s, value_key=%s.%s: frozen value sources conflict",
						context,
						trimmedNamespace,
						trimmedOutputName,
					)
				}
				values[trimmedNamespace][trimmedOutputName] = value
			}
		}
	}
	return values, nil
}

func migrationPriorNodeRequirements(currentNodeID string, graph []migrationGraphEdge) ([]workflow.PriorNodeValueRequirement, error) {
	definition, err := migrationWorkflowDefinition(graph)
	if err != nil {
		return nil, err
	}
	return workflow.DeriveWiring(definition).PriorNodeValueRequirementsForNode(workflow.NodeID(currentNodeID)), nil
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

func migrationLatestPriorNodeCandidate(
	branchScoped bool,
	requirement workflow.PriorNodeValueRequirement,
	candidates []migrationPriorValueCandidate,
) (string, bool, error) {
	outputName := strings.TrimSpace(requirement.OutputName)
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

func migrationCandidateProvidesRequirement(candidate migrationPriorValueCandidate, requirement workflow.PriorNodeValueRequirement) bool {
	if requirement.ProviderTransitionKey != nil {
		return strings.TrimSpace(candidate.TransitionKey) == strings.TrimSpace(string(*requirement.ProviderTransitionKey))
	}
	return strings.TrimSpace(candidate.NodeKey) == strings.TrimSpace(string(requirement.ProviderNodeKey))
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
