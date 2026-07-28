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
			7,
			migrationPriorNodeValues,
		)
	})
	if registerMetadataSQLiteFunctionsErr != nil {
		return fmt.Errorf("register metadata SQLite migration functions: %w", registerMetadataSQLiteFunctionsErr)
	}
	return nil
}

type migrationGraphEdge struct {
	SourceNodeID   string `json:"source_node_id"`
	TargetNodeID   string `json:"target_node_id"`
	PromptTemplate string `json:"prompt_template"`
}

type migrationPriorValueCandidate struct {
	Scope            string `json:"scope"`
	NodeKey          string `json:"node_key"`
	OutputValuesJSON string `json:"output_values_json"`
	AppliedAtUnixMs  int64  `json:"applied_at_unix_ms"`
	CreatedAtUnixMs  int64  `json:"created_at_unix_ms"`
	TransitionID     string `json:"transition_id"`
}

func migrationPriorNodeValues(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 7 {
		return nil, fmt.Errorf("%s requires 7 arguments", migrationPriorNodeValuesFunction)
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
	frozenJSON, err := migrationStringArgument(args[5], "frozen prior-node values")
	if err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: %w", context, err)
	}
	frozen := map[string]map[string]string{}
	if err := json.Unmarshal([]byte(frozenJSON), &frozen); err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: decode frozen values: %w", context, err)
	}
	candidatesJSON, err := migrationStringArgument(args[6], "prior-node candidates")
	if err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: %w", context, err)
	}
	candidates := []migrationPriorValueCandidate{}
	if err := json.Unmarshal([]byte(candidatesJSON), &candidates); err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: decode candidates: %w", context, err)
	}
	values := make(map[string]map[string]string, len(frozen))
	for nodeKey, outputValues := range frozen {
		trimmedNodeKey := strings.TrimSpace(nodeKey)
		if trimmedNodeKey == "" {
			return nil, fmt.Errorf("prior-node migration failure: %s, value_key=<blank>: frozen node key is required", context)
		}
		if len(outputValues) == 0 {
			return nil, fmt.Errorf("prior-node migration failure: %s, value_key=%s: frozen node values are empty", context, trimmedNodeKey)
		}
		values[trimmedNodeKey] = make(map[string]string, len(outputValues))
		for outputName, value := range outputValues {
			trimmedOutputName := strings.TrimSpace(outputName)
			if trimmedOutputName == "" {
				return nil, fmt.Errorf("prior-node migration failure: %s, value_key=%s.<blank>: frozen output name is required", context, trimmedNodeKey)
			}
			values[trimmedNodeKey][trimmedOutputName] = value
		}
	}
	for _, requirement := range requirements {
		nodeKey := strings.TrimSpace(string(requirement.NodeKey))
		outputName := strings.TrimSpace(requirement.OutputName)
		valueKey := nodeKey + "." + outputName
		candidate, candidateFound, candidateErr := migrationLatestPriorNodeCandidate(branch != "", nodeKey, outputName, candidates)
		if candidateErr != nil {
			return nil, fmt.Errorf("prior-node migration failure: %s, value_key=%s: %w", context, valueKey, candidateErr)
		}
		frozenValue, frozenFound := frozen[nodeKey][outputName]
		if frozenFound {
			if candidateFound && candidate != frozenValue {
				return nil, fmt.Errorf(
					"prior-node migration failure: %s, value_key=%s: frozen value conflicts with reconstructed value",
					context,
					valueKey,
				)
			}
			if values[nodeKey] == nil {
				values[nodeKey] = make(map[string]string)
			}
			values[nodeKey][outputName] = frozenValue
			continue
		}
		if !candidateFound {
			return nil, fmt.Errorf("prior-node migration failure: %s, value_key=%s: required value is missing", context, valueKey)
		}
		if values[nodeKey] == nil {
			values[nodeKey] = make(map[string]string)
		}
		values[nodeKey][outputName] = candidate
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("prior-node migration failure: %s: encode values: %w", context, err)
	}
	return string(encoded), nil
}

func migrationPriorNodeRequirements(currentNodeID string, graph []migrationGraphEdge) ([]workflow.PriorNodeValueRequirement, error) {
	outgoing := make(map[string][]string)
	requirementsByTarget := make(map[string][]workflow.PriorNodeValueRequirement)
	for _, edge := range graph {
		sourceNodeID := strings.TrimSpace(edge.SourceNodeID)
		targetNodeID := strings.TrimSpace(edge.TargetNodeID)
		if sourceNodeID == "" || targetNodeID == "" {
			return nil, errors.New("workflow graph edge has a blank node id")
		}
		outgoing[sourceNodeID] = append(outgoing[sourceNodeID], targetNodeID)
		refs, err := workflow.ExtractPromptTemplateReferences(edge.PromptTemplate)
		if err != nil {
			return nil, fmt.Errorf("parse prompt for edge %s -> %s: %w", sourceNodeID, targetNodeID, err)
		}
		if len(refs.Invalid) != 0 {
			return nil, fmt.Errorf("prompt for edge %s -> %s has invalid reference %q", sourceNodeID, targetNodeID, refs.Invalid[0].Placeholder)
		}
		for _, ref := range refs.PriorNodes {
			requirement := workflow.PriorNodeValueRequirement{
				NodeKey:    workflow.ModelKey(strings.TrimSpace(string(ref.NodeKey))),
				OutputName: strings.TrimSpace(ref.OutputName),
			}
			if requirement.NodeKey == "" || requirement.OutputName == "" {
				return nil, fmt.Errorf("prompt for edge %s -> %s has an incomplete prior-node reference", sourceNodeID, targetNodeID)
			}
			requirementsByTarget[targetNodeID] = append(requirementsByTarget[targetNodeID], requirement)
		}
	}
	reachable := make(map[string]struct{})
	stack := []string{strings.TrimSpace(currentNodeID)}
	for len(stack) != 0 {
		nodeID := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if nodeID == "" {
			return nil, errors.New("current node id is blank")
		}
		if _, seen := reachable[nodeID]; seen {
			continue
		}
		reachable[nodeID] = struct{}{}
		stack = append(stack, outgoing[nodeID]...)
	}
	seen := make(map[workflow.PriorNodeValueRequirement]struct{})
	requirements := make([]workflow.PriorNodeValueRequirement, 0)
	for targetNodeID, targetRequirements := range requirementsByTarget {
		if _, required := reachable[targetNodeID]; !required {
			continue
		}
		for _, requirement := range targetRequirements {
			if _, duplicate := seen[requirement]; duplicate {
				continue
			}
			seen[requirement] = struct{}{}
			requirements = append(requirements, requirement)
		}
	}
	sort.Slice(requirements, func(left, right int) bool {
		leftNodeKey := string(requirements[left].NodeKey)
		rightNodeKey := string(requirements[right].NodeKey)
		if leftNodeKey != rightNodeKey {
			return leftNodeKey < rightNodeKey
		}
		return requirements[left].OutputName < requirements[right].OutputName
	})
	return requirements, nil
}

func migrationLatestPriorNodeCandidate(
	branchScoped bool,
	nodeKey string,
	outputName string,
	candidates []migrationPriorValueCandidate,
) (string, bool, error) {
	for _, scope := range migrationPriorCandidateScopes(branchScoped) {
		var selected *migrationPriorValueCandidate
		var selectedValue string
		for index := range candidates {
			candidate := &candidates[index]
			if candidate.Scope != scope || strings.TrimSpace(candidate.NodeKey) != nodeKey {
				continue
			}
			outputs := map[string]string{}
			if err := json.Unmarshal([]byte(candidate.OutputValuesJSON), &outputs); err != nil {
				return "", false, fmt.Errorf("transition %q has malformed output values: %w", candidate.TransitionID, err)
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
					selected.TransitionID,
					candidate.TransitionID,
				)
			}
		}
		if selected != nil {
			return selectedValue, true, nil
		}
	}
	return "", false, nil
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
