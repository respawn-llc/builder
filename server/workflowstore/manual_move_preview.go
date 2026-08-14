package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

type ManualMovePreviewOutcome string

const (
	ManualMovePreviewOutcomeNoOp       ManualMovePreviewOutcome = "no_op"
	ManualMovePreviewOutcomeDirect     ManualMovePreviewOutcome = "direct"
	ManualMovePreviewOutcomeTransition ManualMovePreviewOutcome = "transition"
	ManualMovePreviewOutcomeBlocked    ManualMovePreviewOutcome = "blocked"
)

type ManualMoveBlocker string

const (
	ManualMoveBlockerInvalidWorkflow              ManualMoveBlocker = "invalid_workflow"
	ManualMoveBlockerNoSourcePosition             ManualMoveBlocker = "no_source_position"
	ManualMoveBlockerUnsupportedDestination       ManualMoveBlocker = "unsupported_destination"
	ManualMoveBlockerLifecycleConflict            ManualMoveBlocker = "lifecycle_conflict"
	ManualMoveBlockerContextSessionUnavailable    ManualMoveBlocker = "context_session_unavailable"
	ManualMoveBlockerNoUsableTransition           ManualMoveBlocker = "no_usable_transition"
	ManualMoveBlockerParallelBranchRequiresFanOut ManualMoveBlocker = "parallel_branch_requires_fan_out"
)

var (
	ErrManualMoveTransitionSelectionRequired = errors.New("manual move requires a Transition selection")
	ErrManualMoveTransitionNotUsable         = errors.New("manual move Transition is not usable")
	ErrManualMoveDirectFieldsNotAllowed      = errors.New("direct manual move does not accept Transition or value fields")
	ErrManualMoveValuesInvalid               = errors.New("manual move values do not match the selected Transition requirements")
)

type manualMoveValueIdentity struct {
	NodeKey    workflow.ModelKey
	OutputName string
}

type manualMoveMaterializedValue struct {
	value    string
	present  bool
	conflict bool
}

type manualMoveValueEnvironment struct {
	values map[manualMoveValueIdentity]manualMoveMaterializedValue
}

func newManualMoveValueEnvironment() manualMoveValueEnvironment {
	return manualMoveValueEnvironment{values: make(map[manualMoveValueIdentity]manualMoveMaterializedValue)}
}

func (e *manualMoveValueEnvironment) add(nodeKey workflow.ModelKey, outputName, value string) {
	nodeKey = workflow.ModelKey(strings.TrimSpace(string(nodeKey)))
	outputName = strings.TrimSpace(outputName)
	if nodeKey == "" || outputName == "" || strings.TrimSpace(value) == "" {
		return
	}
	key := manualMoveValueIdentity{NodeKey: nodeKey, OutputName: outputName}
	current := e.values[key]
	if !current.present {
		current.value = value
		current.present = true
		e.values[key] = current
		return
	}
	if current.value != value {
		current.conflict = true
		e.values[key] = current
	}
}

func (e manualMoveValueEnvironment) resolved(nodeKey workflow.ModelKey, outputName string) *string {
	value, exists := e.values[manualMoveValueIdentity{NodeKey: nodeKey, OutputName: outputName}]
	if !exists || !value.present || value.conflict {
		return nil
	}
	result := value.value
	return &result
}

type ManualMoveRequiredValue struct {
	NodeKey       workflow.ModelKey
	OutputName    string
	Description   *string
	ResolvedValue *string
}

type ManualMoveTransitionChoice struct {
	TransitionKey    workflow.TransitionID
	Label            string
	SourceNode       workflow.Node
	Edges            []workflow.Edge
	RequiredValues   []ManualMoveRequiredValue
	authorizedValues map[manualMoveValueIdentity]struct{}
}

type ManualMovePreview struct {
	Outcome      ManualMovePreviewOutcome
	CurrentNodes []workflow.CurrentNode
	Choices      []ManualMoveTransitionChoice
	Blocker      ManualMoveBlocker
}

func (s *Store) PreviewManualMove(ctx context.Context, req ManualMoveRequest) (ManualMovePreview, error) {
	preview, err := s.resolveManualMove(ctx, s.queries, req)
	reportWorkflowInvariantError(s.invariantPolicy, err)
	return preview, err
}

func (s *Store) resolveManualMove(ctx context.Context, q *sqlitegen.Queries, req ManualMoveRequest) (ManualMovePreview, error) {
	task, err := q.GetTask(ctx, string(req.TaskID))
	if err != nil {
		return ManualMovePreview{}, err
	}
	currentNodes, err := s.listTaskCurrentNodes(ctx, q, req.TaskID)
	if err != nil {
		return ManualMovePreview{}, err
	}
	for _, currentNode := range currentNodes {
		if currentNode.Reference.NodeID == req.TargetNodeID {
			return ManualMovePreview{
				Outcome:      ManualMovePreviewOutcomeNoOp,
				CurrentNodes: append([]workflow.CurrentNode(nil), currentNodes...),
			}, nil
		}
	}
	definition, _, err := workflowDefinitionFromQueries(ctx, q, runtimeids.WorkflowID(task.WorkflowID))
	if err != nil {
		return ManualMovePreview{}, err
	}
	if err := s.preflightInitialExecution(definition); err != nil {
		var validationErr WorkflowValidationError
		if errors.As(err, &validationErr) {
			return ManualMovePreview{Outcome: ManualMovePreviewOutcomeBlocked, Blocker: ManualMoveBlockerInvalidWorkflow}, nil
		}
		return ManualMovePreview{}, err
	}
	target, err := currentNodeDefinitionNode(definition, req.TargetNodeID)
	if err != nil {
		return ManualMovePreview{}, err
	}
	if len(currentNodes) == 0 {
		return ManualMovePreview{Outcome: ManualMovePreviewOutcomeBlocked, Blocker: ManualMoveBlockerNoSourcePosition}, nil
	}
	switch target.Kind() {
	case workflow.NodeKindStart, workflow.NodeKindTerminal:
		if req.TransitionKey != nil || len(req.Values) != 0 {
			return ManualMovePreview{}, ErrManualMoveDirectFieldsNotAllowed
		}
		return ManualMovePreview{Outcome: ManualMovePreviewOutcomeDirect}, nil
	case workflow.NodeKindAgent, workflow.NodeKindScript:
		return s.resolveManualMoveExecutablePreview(ctx, q, definition, target, currentNodes, req)
	default:
		return ManualMovePreview{Outcome: ManualMovePreviewOutcomeBlocked, Blocker: ManualMoveBlockerUnsupportedDestination}, nil
	}
}

func (s *Store) resolveManualMoveExecutablePreview(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	target workflow.Node,
	currentNodes []workflow.CurrentNode,
	req ManualMoveRequest,
) (ManualMovePreview, error) {
	valueEnvironment, err := s.manualMoveValueEnvironment(
		ctx,
		q,
		definition,
		req.TaskID,
		currentNodes,
		workflow.NodeIDOf(target),
	)
	if err != nil {
		return ManualMovePreview{}, err
	}
	groupsByID := make(map[workflow.TransitionGroupID]workflow.TransitionGroup, len(definition.TransitionGroups))
	nodesByID := make(map[workflow.NodeID]workflow.Node, len(definition.Nodes))
	edgesByTarget := make(map[workflow.NodeID][]workflow.Edge)
	edgesByGroup := make(map[workflow.TransitionGroupID][]workflow.Edge)
	for _, node := range definition.Nodes {
		nodesByID[workflow.NodeIDOf(node)] = node
	}
	for _, group := range definition.TransitionGroups {
		groupsByID[group.ID] = group
	}
	for _, edge := range definition.Edges {
		edgesByGroup[edge.TransitionGroupID] = append(edgesByGroup[edge.TransitionGroupID], edge)
		if edge.TargetNodeID == workflow.NodeIDOf(target) {
			edgesByTarget[edge.TargetNodeID] = append(edgesByTarget[edge.TargetNodeID], edge)
		}
	}

	candidatesByGroup := make(map[workflow.TransitionGroupID]ManualMoveTransitionChoice)
	contextUnavailable := false
	parallelBlocked := false
	for _, edge := range edgesByTarget[workflow.NodeIDOf(target)] {
		group, exists := groupsByID[edge.TransitionGroupID]
		if !exists {
			return ManualMovePreview{}, fmt.Errorf("manual move edge %q references missing Transition Group %q", edge.ID, edge.TransitionGroupID)
		}
		if workflow.SerialTransitionRequiresFanoutSiblings(definition, group.ID) {
			parallelBlocked = true
			continue
		}
		if _, exists := candidatesByGroup[group.ID]; exists {
			continue
		}
		source, exists := nodesByID[group.SourceNodeID]
		if !exists {
			return ManualMovePreview{}, fmt.Errorf("manual move Transition %q source node %q is absent", group.TransitionID, group.SourceNodeID)
		}
		candidate := ManualMoveTransitionChoice{
			TransitionKey: group.TransitionID,
			Label:         group.DisplayName,
			SourceNode:    source,
			Edges:         append([]workflow.Edge(nil), edgesByGroup[group.ID]...),
		}
		contextUnavailableForCandidate, err := s.manualMoveContextUnavailable(ctx, q, definition, req.TaskID, candidate, currentNodes)
		if err != nil {
			return ManualMovePreview{}, err
		}
		if contextUnavailableForCandidate {
			contextUnavailable = true
			continue
		}
		contracts, authorizedValues, err := s.manualMoveProtectedParameterPolicies(ctx, q, definition, candidate, currentNodes)
		if err != nil {
			return ManualMovePreview{}, err
		}
		candidate.RequiredValues = manualMoveRequiredValues(definition, candidate, valueEnvironment, req.Values, contracts)
		candidate.authorizedValues = authorizedValues
		candidatesByGroup[group.ID] = candidate
	}
	candidates := make([]ManualMoveTransitionChoice, 0, len(candidatesByGroup))
	for _, candidate := range candidatesByGroup {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].TransitionKey != candidates[j].TransitionKey {
			return candidates[i].TransitionKey < candidates[j].TransitionKey
		}
		return workflow.NodeKey(candidates[i].SourceNode) < workflow.NodeKey(candidates[j].SourceNode)
	})
	if len(candidates) == 0 {
		if contextUnavailable {
			return ManualMovePreview{Outcome: ManualMovePreviewOutcomeBlocked, Blocker: ManualMoveBlockerContextSessionUnavailable}, nil
		}
		if parallelBlocked {
			return ManualMovePreview{Outcome: ManualMovePreviewOutcomeBlocked, Blocker: ManualMoveBlockerParallelBranchRequiresFanOut}, nil
		}
		return ManualMovePreview{Outcome: ManualMovePreviewOutcomeBlocked, Blocker: ManualMoveBlockerNoUsableTransition}, nil
	}
	if req.TransitionKey != nil {
		for _, candidate := range candidates {
			if candidate.TransitionKey != *req.TransitionKey {
				continue
			}
			if len(req.Values) != 0 {
				if err := validateManualMoveValues(candidate, req.Values, valueEnvironment); err != nil {
					return ManualMovePreview{}, err
				}
			}
			return ManualMovePreview{Outcome: ManualMovePreviewOutcomeTransition, Choices: []ManualMoveTransitionChoice{candidate}}, nil
		}
		return ManualMovePreview{}, fmt.Errorf("%w: Transition %q", ErrManualMoveTransitionNotUsable, *req.TransitionKey)
	}
	if len(candidates) == 1 && len(req.Values) != 0 {
		if err := validateManualMoveValues(candidates[0], req.Values, valueEnvironment); err != nil {
			return ManualMovePreview{}, err
		}
	}
	return ManualMovePreview{Outcome: ManualMovePreviewOutcomeTransition, Choices: candidates}, nil
}

func (s *Store) manualMoveContextUnavailable(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	taskID workflow.TaskID,
	choice ManualMoveTransitionChoice,
	currentNodes []workflow.CurrentNode,
) (bool, error) {
	contextSource := manualMoveContextCurrentNode(currentNodes)
	if len(currentNodes) == 0 {
		return true, nil
	}
	for _, edge := range choice.Edges {
		if edge.ContextMode == workflow.ContextModeNewSession {
			continue
		}
		target, targetErr := currentNodeDefinitionNode(definition, edge.TargetNodeID)
		if targetErr != nil {
			return false, targetErr
		}
		_, err := resolveTransitionContext(
			ctx,
			q,
			definition,
			edge,
			currentNodes[0].Reference.TaskID,
			contextSource,
			nil,
			choice.SourceNode,
			target,
			true,
		)
		if err == nil {
			continue
		}
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrManualMoveTransitionNotUsable) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func (s *Store) manualMoveProtectedParameterPolicies(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	choice ManualMoveTransitionChoice,
	currentNodes []workflow.CurrentNode,
) (map[workflow.EdgeID]workflow.TransitionParameterContract, map[manualMoveValueIdentity]struct{}, error) {
	contracts := make(map[workflow.EdgeID]workflow.TransitionParameterContract, len(choice.Edges))
	authorized := make(map[manualMoveValueIdentity]struct{})
	contextSource := manualMoveContextCurrentNode(currentNodes)
	for _, edge := range choice.Edges {
		target, err := currentNodeDefinitionNode(definition, edge.TargetNodeID)
		if err != nil {
			return nil, nil, err
		}
		planned, err := s.planTransitionParameterContract(
			ctx,
			q,
			definition,
			edge,
			choice.SourceNode,
			target,
			contextSource,
			nil,
			true,
			true,
			transitionContractContextResolutionRequired,
		)
		if err != nil {
			return nil, nil, err
		}
		contracts[edge.ID] = planned
		policy := planned.Consumption
		for _, parameter := range edge.Parameters {
			var parameterPolicy workflow.ProtectedParameterConsumptionPolicy
			switch workflow.CanonicalParameterPurpose(parameter.Purpose) {
			case workflow.ParameterPurposeTargetAssignee:
				parameterPolicy = policy.Assignee
			case workflow.ParameterPurposeTargetThinking:
				parameterPolicy = policy.Thinking
			default:
				continue
			}
			if parameterPolicy == workflow.ProtectedParameterConsumptionIgnoreAuthorized {
				authorized[manualMoveValueIdentity{
					NodeKey:    workflow.NodeKey(choice.SourceNode),
					OutputName: parameter.Key,
				}] = struct{}{}
			}
		}
	}
	return contracts, authorized, nil
}

func manualMoveRequiredValues(
	definition workflow.Definition,
	choice ManualMoveTransitionChoice,
	environment manualMoveValueEnvironment,
	submitted map[workflow.ModelKey]map[string]string,
	contracts map[workflow.EdgeID]workflow.TransitionParameterContract,
) []ManualMoveRequiredValue {
	merged := make(map[manualMoveValueIdentity]ManualMoveRequiredValue)
	for _, edge := range choice.Edges {
		target, err := currentNodeDefinitionNode(definition, edge.TargetNodeID)
		if err != nil {
			continue
		}
		group, err := transitionGroupForEdge(definition, edge)
		if err != nil {
			continue
		}
		for _, required := range manualMoveRequiredValuesForTarget(
			definition,
			target,
			group,
			choice.SourceNode,
			environment,
			submitted,
			contracts,
		) {
			identity := manualMoveValueIdentity{NodeKey: required.NodeKey, OutputName: required.OutputName}
			if existing, exists := merged[identity]; exists {
				if existing.Description == nil {
					existing.Description = required.Description
				}
				if existing.ResolvedValue == nil {
					existing.ResolvedValue = required.ResolvedValue
				}
				merged[identity] = existing
				continue
			}
			merged[identity] = required
		}
	}
	values := make([]ManualMoveRequiredValue, 0, len(merged))
	for _, value := range merged {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].NodeKey != values[j].NodeKey {
			return values[i].NodeKey < values[j].NodeKey
		}
		return values[i].OutputName < values[j].OutputName
	})
	return values
}

func manualMoveRequiredValuesForTarget(
	definition workflow.Definition,
	target workflow.Node,
	group workflow.TransitionGroup,
	source workflow.Node,
	environment manualMoveValueEnvironment,
	submitted map[workflow.ModelKey]map[string]string,
	contracts map[workflow.EdgeID]workflow.TransitionParameterContract,
) []ManualMoveRequiredValue {
	derived := workflow.DeriveWiring(definition)
	values := make([]ManualMoveRequiredValue, 0)
	seen := make(map[string]struct{})
	appendValue := func(nodeKey workflow.ModelKey, field workflow.OutputField) {
		name := strings.TrimSpace(field.Name)
		if nodeKey == "" || name == "" {
			return
		}
		key := string(nodeKey) + "\x00" + name
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		values = append(values, ManualMoveRequiredValue{
			NodeKey:       nodeKey,
			OutputName:    name,
			Description:   manualMoveOptionalDescription(field.Description),
			ResolvedValue: manualMoveSubmittedOrResolved(nodeKey, name, environment, submitted),
		})
	}
	if source.Kind() == workflow.NodeKindJoin {
		for _, edge := range definition.Edges {
			if edge.TargetNodeID != workflow.NodeIDOf(source) {
				continue
			}
			providerGroup, err := transitionGroupForEdge(definition, edge)
			if err != nil {
				continue
			}
			provider, err := currentNodeDefinitionNode(definition, providerGroup.SourceNodeID)
			if err != nil {
				continue
			}
			for _, field := range derived.RequiredProviderFieldsForJoinEdge(edge.ID) {
				appendValue(workflow.NodeKey(provider), field)
			}
		}
	} else {
		for _, edge := range definition.Edges {
			if edge.TransitionGroupID != group.ID {
				continue
			}
			contract := contracts[edge.ID]
			for _, field := range manualMoveTransitionOutputFields(derived, edge, source, contract) {
				appendValue(workflow.NodeKey(source), field)
			}
		}
	}
	for _, requirement := range derived.PriorParameterRequirementsForNode(workflow.NodeIDOf(target)) {
		key := string(requirement.ProviderNode) + "\x00" + requirement.ParameterName
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		values = append(values, ManualMoveRequiredValue{
			NodeKey:       requirement.ProviderNode,
			OutputName:    requirement.ParameterName,
			Description:   manualMovePriorParameterDescription(definition, derived, requirement),
			ResolvedValue: manualMoveSubmittedOrResolved(requirement.ProviderNode, requirement.ParameterName, environment, submitted),
		})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].NodeKey != values[j].NodeKey {
			return values[i].NodeKey < values[j].NodeKey
		}
		return values[i].OutputName < values[j].OutputName
	})
	return values
}

func manualMoveTransitionOutputFields(
	derived workflow.DerivedWiring,
	edge workflow.Edge,
	source workflow.Node,
	contract workflow.TransitionParameterContract,
) []workflow.OutputField {
	fields := derived.TransitionOutputFieldsForEdge(edge, source)
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		seen[strings.TrimSpace(field.Name)] = struct{}{}
	}
	for _, parameter := range contract.Parameters {
		name := strings.TrimSpace(parameter.Key)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		fields = append(fields, workflow.OutputField{Name: name, Description: parameter.Description})
		seen[name] = struct{}{}
	}
	for index := range fields {
		if parameter, exists := parameterByKey(contract.Parameters, fields[index].Name); exists {
			fields[index].Description = parameter.Description
		}
	}
	return fields
}

func manualMovePriorParameterDescription(
	definition workflow.Definition,
	derived workflow.DerivedWiring,
	requirement workflow.PriorTransitionParameterRequirement,
) *string {
	for _, group := range definition.TransitionGroups {
		if workflow.ModelKey(group.TransitionID) != requirement.TransitionKey {
			continue
		}
		source, err := currentNodeDefinitionNode(definition, group.SourceNodeID)
		if err != nil || workflow.NodeKey(source) != requirement.ProviderNode {
			continue
		}
		if source.Kind() == workflow.NodeKindJoin {
			for _, field := range derived.JoinOutputFieldsForNode(group.SourceNodeID) {
				if field.Name == requirement.ParameterName {
					return manualMoveOptionalDescription(field.Description)
				}
			}
			continue
		}
		for _, edge := range definition.Edges {
			if edge.TransitionGroupID != group.ID {
				continue
			}
			for _, parameter := range edge.Parameters {
				if parameter.Key == requirement.ParameterName {
					return manualMoveOptionalDescription(parameter.Description)
				}
			}
		}
	}
	return nil
}

func manualMoveOptionalDescription(description string) *string {
	if strings.TrimSpace(description) == "" {
		return nil
	}
	return &description
}

func manualMoveSubmittedOrResolved(
	nodeKey workflow.ModelKey,
	outputName string,
	environment manualMoveValueEnvironment,
	submitted map[workflow.ModelKey]map[string]string,
) *string {
	if outputs := submitted[nodeKey]; outputs != nil {
		if value, exists := outputs[outputName]; exists {
			result := value
			return &result
		}
	}
	return environment.resolved(nodeKey, outputName)
}

func validateManualMoveValues(
	choice ManualMoveTransitionChoice,
	submitted map[workflow.ModelKey]map[string]string,
	environment manualMoveValueEnvironment,
) error {
	requirements := make(map[manualMoveValueIdentity]struct{}, len(choice.RequiredValues))
	requiredNodes := make(map[workflow.ModelKey]struct{}, len(choice.RequiredValues))
	for _, required := range choice.RequiredValues {
		requirements[manualMoveValueIdentity{NodeKey: required.NodeKey, OutputName: required.OutputName}] = struct{}{}
		requiredNodes[required.NodeKey] = struct{}{}
	}
	for value := range choice.authorizedValues {
		requirements[value] = struct{}{}
		requiredNodes[value.NodeKey] = struct{}{}
	}
	for nodeKey, outputs := range submitted {
		if _, exists := requiredNodes[nodeKey]; !exists {
			return fmt.Errorf("%w: extra value node %s", ErrManualMoveValuesInvalid, nodeKey)
		}
		for outputName, value := range outputs {
			if _, exists := requirements[manualMoveValueIdentity{NodeKey: nodeKey, OutputName: outputName}]; !exists {
				return fmt.Errorf("%w: extra value %s.%s", ErrManualMoveValuesInvalid, nodeKey, outputName)
			}
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%w: blank value %s.%s", ErrManualMoveValuesInvalid, nodeKey, outputName)
			}
			if len(value) > workflow.MaxOutputValueBytes {
				return fmt.Errorf("%w: oversized value %s.%s", ErrManualMoveValuesInvalid, nodeKey, outputName)
			}
		}
	}
	for _, required := range choice.RequiredValues {
		if outputs := submitted[required.NodeKey]; outputs != nil {
			if value, exists := outputs[required.OutputName]; exists && strings.TrimSpace(value) != "" {
				continue
			}
		}
		resolved := environment.values[manualMoveValueIdentity{NodeKey: required.NodeKey, OutputName: required.OutputName}]
		if !resolved.present || resolved.conflict {
			return fmt.Errorf("%w: missing value %s.%s", ErrManualMoveValuesInvalid, required.NodeKey, required.OutputName)
		}
	}
	return nil
}

func (s *Store) manualMoveValueEnvironment(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	taskID workflow.TaskID,
	currentNodes []workflow.CurrentNode,
	targetNodeID workflow.NodeID,
) (manualMoveValueEnvironment, error) {
	environment := newManualMoveValueEnvironment()
	wiring := workflow.DeriveWiring(definition)
	for _, currentNode := range currentNodes {
		if err := addManualMoveCurrentNodeValues(definition, wiring, currentNode, targetNodeID, &environment); err != nil {
			return manualMoveValueEnvironment{}, err
		}
	}
	if err := s.addManualMoveArrivedFanoutValues(ctx, q, definition, taskID, &environment); err != nil {
		return manualMoveValueEnvironment{}, err
	}
	rows, err := q.ListTaskPendingApprovals(ctx, string(taskID))
	if err != nil {
		return manualMoveValueEnvironment{}, err
	}
	for _, row := range rows {
		approval, err := pendingApprovalFromRow(ctx, q, pendingApprovalRecordFromListRow(row))
		if err != nil {
			return manualMoveValueEnvironment{}, err
		}
		source, err := currentNodeDefinitionNode(definition, approval.Source.NodeID)
		if err != nil {
			return manualMoveValueEnvironment{}, err
		}
		for outputName, value := range approval.OutputValues {
			environment.add(workflow.NodeKey(source), outputName, value)
		}
		for _, branch := range approval.Branches {
			if err := addManualMoveCurrentNodeValues(
				definition,
				wiring,
				branch.Target.CurrentNode,
				targetNodeID,
				&environment,
			); err != nil {
				return manualMoveValueEnvironment{}, err
			}
		}
	}
	return environment, nil
}

func (s *Store) addManualMoveArrivedFanoutValues(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	taskID workflow.TaskID,
	environment *manualMoveValueEnvironment,
) error {
	arrivals, _, err := currentFanoutJoinArrivals(ctx, q, taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if len(arrivals) == 0 {
		return nil
	}
	resolution, resolved := workflow.ResolveFanoutJoin(definition, currentFanoutBranchKeys(arrivals))
	if !resolved {
		return currentFanoutJoinTopologyError(definition, taskID)
	}
	for _, arrival := range arrivals {
		if len(arrival.Values) == 0 {
			continue
		}
		joinEdge, exists := resolution.BranchJoinEdges[arrival.BranchKey]
		if !exists {
			return currentFanoutJoinTopologyError(definition, taskID)
		}
		group, err := transitionGroupForEdge(definition, joinEdge)
		if err != nil {
			return err
		}
		source, err := currentNodeDefinitionNode(definition, group.SourceNodeID)
		if err != nil {
			return err
		}
		for outputName, value := range arrival.Values {
			environment.add(workflow.NodeKey(source), outputName, value)
		}
	}
	return nil
}

func addManualMoveCurrentNodeValues(
	definition workflow.Definition,
	wiring workflow.DerivedWiring,
	currentNode workflow.CurrentNode,
	targetNodeID workflow.NodeID,
	environment *manualMoveValueEnvironment,
) error {
	if currentNode.EnteredByEdgeID != nil {
		edge, err := currentNodeDefinitionEnteringEdgeForManualMove(definition, currentNode, targetNodeID)
		if err != nil {
			return err
		}
		group, err := transitionGroupForEdge(definition, edge)
		if err != nil {
			return err
		}
		source, err := currentNodeDefinitionNode(definition, group.SourceNodeID)
		if err != nil {
			return err
		}
		for _, binding := range wiring.CurrentNodeInputBindingsForEdge(edge.ID) {
			if value, exists := currentNode.CurrentInputValues[binding.Name]; exists {
				environment.add(workflow.NodeKey(source), binding.Field, value)
			}
		}
	}
	for _, requirement := range wiring.PriorParameterRequirementsForNode(currentNode.Reference.NodeID) {
		if value, exists := currentNode.PriorValues.TransitionParameter(requirement.TransitionKey, requirement.ParameterName); exists {
			environment.add(requirement.ProviderNode, requirement.ParameterName, value)
		}
	}
	return nil
}

func currentNodeDefinitionEnteringEdgeForManualMove(
	definition workflow.Definition,
	currentNode workflow.CurrentNode,
	targetNodeID workflow.NodeID,
) (workflow.Edge, error) {
	edge, err := currentNodeDefinitionEnteringEdgeByID(definition, currentNode)
	if err != nil {
		return workflow.Edge{}, err
	}
	if edge.TargetNodeID != currentNode.Reference.NodeID && edge.TargetNodeID != targetNodeID {
		return workflow.Edge{}, currentNodeEnteringEdgeTargetError(currentNode, edge)
	}
	return edge, nil
}
