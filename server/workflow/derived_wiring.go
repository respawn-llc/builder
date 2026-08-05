package workflow

import (
	"fmt"
	"slices"
	"strings"
)

type DerivedWiring struct {
	Diagnostics []ValidationError

	inputBindingsByEdge              map[EdgeID][]InputBinding
	requiredProvisionFieldsByEdge    map[EdgeID][]OutputField
	requiredProvisionFieldsByGroup   map[TransitionGroupID][]OutputField
	possibleProvisionFieldsByNode    map[NodeID][]OutputField
	requiredProviderFieldsByJoinEdge map[EdgeID][]OutputField
	joinOutputFieldsByNode           map[NodeID][]OutputField
	parameterDependencyEdgesByEdge   map[EdgeID][]EdgeID
	currentNodeInputBindingsByEdge   map[EdgeID][]InputBinding
	currentNodeOutputFieldsByGroup   map[TransitionGroupID][]OutputField
	priorParameterRequirementsByNode map[NodeID][]PriorTransitionParameterRequirement
	selectorApplicabilityByEdge      map[EdgeID]EdgeSelectorApplicability
}

type PriorTransitionParameterRequirement struct {
	ProviderNode  ModelKey
	TransitionKey ModelKey
	ParameterName string
}

type priorParameterTransitionResolution struct {
	matched    int
	guaranteed []TransitionGroup
}

type derivedPriorParameterRequirement struct {
	value                     PriorTransitionParameterRequirement
	providerTransitionGroupID TransitionGroupID
}

func DeriveWiring(def Definition) DerivedWiring {
	priorReferencesByEdge := make(map[EdgeID][]PromptPriorParameterReference, len(def.Edges))
	for _, edge := range def.Edges {
		refs, err := ExtractPromptTemplateReferences(edge.PromptTemplate)
		if err != nil {
			continue
		}
		priorReferencesByEdge[edge.ID] = append([]PromptPriorParameterReference(nil), refs.PriorParams...)
	}
	return DeriveWiringWithPriorParameterReferences(def, priorReferencesByEdge)
}

// DeriveWiringWithPriorParameterReferences derives the canonical wiring graph
// from prompt-owned prior-Parameter references supplied by the caller. The
// metadata migration boundary uses this entry point with its frozen historical
// parser; runtime callers use DeriveWiring, which owns live prompt parsing.
func DeriveWiringWithPriorParameterReferences(
	def Definition,
	priorReferencesByEdge map[EdgeID][]PromptPriorParameterReference,
) DerivedWiring {
	return deriveWiring(def, nil, priorReferencesByEdge)
}

func DeriveWiringWithCatalog(def Definition, catalog TargetAgentCatalog) DerivedWiring {
	priorReferencesByEdge := make(map[EdgeID][]PromptPriorParameterReference, len(def.Edges))
	for _, edge := range def.Edges {
		refs, err := ExtractPromptTemplateReferences(edge.PromptTemplate)
		if err != nil {
			continue
		}
		priorReferencesByEdge[edge.ID] = append([]PromptPriorParameterReference(nil), refs.PriorParams...)
	}
	return deriveWiring(def, catalog, priorReferencesByEdge)
}

func deriveWiring(
	def Definition,
	catalog TargetAgentCatalog,
	priorReferencesByEdge map[EdgeID][]PromptPriorParameterReference,
) DerivedWiring {
	derived := DerivedWiring{
		inputBindingsByEdge:              map[EdgeID][]InputBinding{},
		requiredProvisionFieldsByEdge:    map[EdgeID][]OutputField{},
		requiredProvisionFieldsByGroup:   map[TransitionGroupID][]OutputField{},
		possibleProvisionFieldsByNode:    map[NodeID][]OutputField{},
		requiredProviderFieldsByJoinEdge: map[EdgeID][]OutputField{},
		joinOutputFieldsByNode:           map[NodeID][]OutputField{},
		parameterDependencyEdgesByEdge:   map[EdgeID][]EdgeID{},
		currentNodeInputBindingsByEdge:   map[EdgeID][]InputBinding{},
		currentNodeOutputFieldsByGroup:   map[TransitionGroupID][]OutputField{},
		priorParameterRequirementsByNode: map[NodeID][]PriorTransitionParameterRequirement{},
		selectorApplicabilityByEdge:      map[EdgeID]EdgeSelectorApplicability{},
	}
	nodesByID := make(map[NodeID]Node, len(def.Nodes))
	groupsByID := make(map[TransitionGroupID]TransitionGroup, len(def.TransitionGroups))
	incomingByNode := make(map[NodeID][]Edge, len(def.Edges))
	outgoingByNode := make(map[NodeID][]Edge, len(def.Edges))
	for _, node := range def.Nodes {
		if strings.TrimSpace(string(NodeIDOf(node))) != "" {
			nodesByID[NodeIDOf(node)] = node
		}
	}
	for _, group := range def.TransitionGroups {
		if strings.TrimSpace(string(group.ID)) != "" {
			groupsByID[group.ID] = group
		}
	}
	for _, edge := range def.Edges {
		incomingByNode[edge.TargetNodeID] = append(incomingByNode[edge.TargetNodeID], edge)
		if group, ok := groupsByID[edge.TransitionGroupID]; ok {
			outgoingByNode[group.SourceNodeID] = append(outgoingByNode[group.SourceNodeID], edge)
		}
		derived.parameterDependencyEdgesByEdge[edge.ID] = []EdgeID{edge.ID}
	}
	for _, edge := range def.Edges {
		group, groupExists := groupsByID[edge.TransitionGroupID]
		if !groupExists {
			continue
		}
		source, sourceExists := nodesByID[group.SourceNodeID]
		target, targetExists := nodesByID[edge.TargetNodeID]
		selector := EdgeSelectorApplicability{
			Assignee: SelectorApplicability{Reason: SelectorApplicabilityTopology},
			Thinking: SelectorApplicability{Reason: SelectorApplicabilityTopology},
		}
		if sourceExists && targetExists {
			selector = ResolveEdgeSelectorApplicability(
				edge,
				source.Kind(),
				target.Kind(),
				len(edgesForTransitionGroup(def.Edges, edge.TransitionGroupID)) > 1,
				catalog,
				NodeSubagentRole(target),
			)
		}
		derived.selectorApplicabilityByEdge[edge.ID] = selector
	}
	for _, edge := range def.Edges {
		group, groupExists := groupsByID[edge.TransitionGroupID]
		if !groupExists {
			continue
		}
		source, sourceExists := nodesByID[group.SourceNodeID]
		if sourceExists && (source.Kind() == NodeKindStart || source.Kind() == NodeKindJoin) {
			continue
		}
		requiredFields := edgeParameterFields(edge)
		if len(requiredFields) == 0 {
			continue
		}
		derived.inputBindingsByEdge[edge.ID] = inputBindingsForFields(requiredFields)
		derived.addRequiredProvisionFields(edge.ID, edge.TransitionGroupID, requiredFields)
		derived.addPossibleProvisionFields(group.SourceNodeID, requiredFields, ValidationError{NodeID: group.SourceNodeID, EdgeID: edge.ID, TransitionGroupID: edge.TransitionGroupID})
	}
	for _, node := range def.Nodes {
		if node.Kind() == NodeKindJoin {
			derived.deriveJoinAggregateParameters(node, incomingByNode)
		}
	}
	derived.deriveParameterDependencies(incomingByNode, outgoingByNode)
	derived.deriveCurrentNodeValueEnvironment(def, nodesByID, groupsByID, outgoingByNode, priorReferencesByEdge)
	return derived
}

func (w DerivedWiring) InputBindingsForEdge(edgeID EdgeID) []InputBinding {
	return append([]InputBinding(nil), w.inputBindingsByEdge[edgeID]...)
}

func (w DerivedWiring) RequiredProvisionFieldsForTransitionGroup(groupID TransitionGroupID) []OutputField {
	return append([]OutputField(nil), w.requiredProvisionFieldsByGroup[groupID]...)
}

func (w DerivedWiring) RequiredProvisionFieldsForEdge(edgeID EdgeID) []OutputField {
	return append([]OutputField(nil), w.requiredProvisionFieldsByEdge[edgeID]...)
}

func (w DerivedWiring) PossibleProvisionFieldsForNode(nodeID NodeID) []OutputField {
	return append([]OutputField(nil), w.possibleProvisionFieldsByNode[nodeID]...)
}

func (w DerivedWiring) RequiredProviderFieldsForJoinEdge(edgeID EdgeID) []OutputField {
	return append([]OutputField(nil), w.requiredProviderFieldsByJoinEdge[edgeID]...)
}

func (w DerivedWiring) JoinOutputFieldsForNode(nodeID NodeID) []OutputField {
	return append([]OutputField(nil), w.joinOutputFieldsByNode[nodeID]...)
}

func (w DerivedWiring) SelectorApplicabilityForEdge(edgeID EdgeID) EdgeSelectorApplicability {
	return w.selectorApplicabilityByEdge[edgeID]
}

func edgesForTransitionGroup(edges []Edge, groupID TransitionGroupID) []Edge {
	out := make([]Edge, 0, len(edges))
	for _, edge := range edges {
		if edge.TransitionGroupID == groupID {
			out = append(out, edge)
		}
	}
	return out
}

func (w DerivedWiring) CurrentNodeInputBindingsForEdge(edgeID EdgeID) []InputBinding {
	return append([]InputBinding(nil), w.currentNodeInputBindingsByEdge[edgeID]...)
}

func (w DerivedWiring) CurrentNodeOutputFieldsForTransitionGroup(groupID TransitionGroupID) []OutputField {
	return append([]OutputField(nil), w.currentNodeOutputFieldsByGroup[groupID]...)
}

func (w DerivedWiring) PriorParameterRequirementsForNode(nodeID NodeID) []PriorTransitionParameterRequirement {
	return append([]PriorTransitionParameterRequirement(nil), w.priorParameterRequirementsByNode[nodeID]...)
}

// ParameterDependencyEdgesForEdge returns entering Edges whose materialized
// values depend on an Edge's Parameters. The dependency map is derived from
// the same Join aggregate fields used by CurrentNodeInputBindingsForEdge.
func (w DerivedWiring) ParameterDependencyEdgesForEdge(edgeID EdgeID) []EdgeID {
	return append([]EdgeID(nil), w.parameterDependencyEdgesByEdge[edgeID]...)
}

func (w DerivedWiring) TransitionOutputFieldsForEdge(edge Edge, source Node) []OutputField {
	if source.Kind() == NodeKindJoin {
		return w.JoinOutputFieldsForNode(NodeIDOf(source))
	}
	return w.RequiredProvisionFieldsForEdge(edge.ID)
}

func TransitionOutputFieldsForTargetNode(def Definition, derived DerivedWiring, targetNodeID NodeID) []OutputField {
	nodesByID := make(map[NodeID]Node, len(def.Nodes))
	groupsByID := make(map[TransitionGroupID]TransitionGroup, len(def.TransitionGroups))
	for _, node := range def.Nodes {
		nodesByID[NodeIDOf(node)] = node
	}
	for _, group := range def.TransitionGroups {
		groupsByID[group.ID] = group
	}
	fields := []OutputField{}
	for _, edge := range def.Edges {
		if edge.TargetNodeID != targetNodeID {
			continue
		}
		group, groupExists := groupsByID[edge.TransitionGroupID]
		if !groupExists {
			continue
		}
		source, sourceExists := nodesByID[group.SourceNodeID]
		if !sourceExists {
			continue
		}
		fields = appendUniqueOutputFields(fields, derived.TransitionOutputFieldsForEdge(edge, source))
	}
	return fields
}

func (w *DerivedWiring) addRequiredProvisionFields(edgeID EdgeID, groupID TransitionGroupID, fields []OutputField) {
	edgeMerged, edgeDiagnostics := appendCompatibleOutputFields(w.requiredProvisionFieldsByEdge[edgeID], fields, ValidationError{EdgeID: edgeID, TransitionGroupID: groupID})
	w.requiredProvisionFieldsByEdge[edgeID] = edgeMerged
	merged, diagnostics := appendCompatibleOutputFields(w.requiredProvisionFieldsByGroup[groupID], fields, ValidationError{TransitionGroupID: groupID})
	w.requiredProvisionFieldsByGroup[groupID] = merged
	w.Diagnostics = append(w.Diagnostics, edgeDiagnostics...)
	w.Diagnostics = append(w.Diagnostics, diagnostics...)
}

func (w *DerivedWiring) addPossibleProvisionFields(nodeID NodeID, fields []OutputField, ref ValidationError) {
	merged, diagnostics := appendCompatibleOutputFields(w.possibleProvisionFieldsByNode[nodeID], fields, ref)
	w.possibleProvisionFieldsByNode[nodeID] = merged
	w.Diagnostics = append(w.Diagnostics, diagnostics...)
}

func (w *DerivedWiring) addRequiredProviderFields(edgeID EdgeID, fields []OutputField, ref ValidationError) {
	merged, diagnostics := appendCompatibleOutputFields(w.requiredProviderFieldsByJoinEdge[edgeID], fields, ref)
	w.requiredProviderFieldsByJoinEdge[edgeID] = merged
	w.Diagnostics = append(w.Diagnostics, diagnostics...)
}

func (w *DerivedWiring) deriveJoinAggregateParameters(join Node, incomingByNode map[NodeID][]Edge) {
	incomingEdges := incomingByNode[NodeIDOf(join)]
	groupFields := map[TransitionGroupID][]OutputField{}
	groupOrder := []TransitionGroupID{}
	seenGroup := map[TransitionGroupID]bool{}
	for _, edge := range incomingEdges {
		fields := edgeParameterFields(edge)
		if len(fields) == 0 {
			continue
		}
		ref := ValidationError{NodeID: NodeIDOf(join), EdgeID: edge.ID, TransitionGroupID: edge.TransitionGroupID}
		w.addRequiredProviderFields(edge.ID, fields, ref)
		if !seenGroup[edge.TransitionGroupID] {
			groupOrder = append(groupOrder, edge.TransitionGroupID)
			seenGroup[edge.TransitionGroupID] = true
		}
		merged, diagnostics := appendCompatibleOutputFields(groupFields[edge.TransitionGroupID], fields, ref)
		groupFields[edge.TransitionGroupID] = merged
		w.Diagnostics = append(w.Diagnostics, diagnostics...)
	}

	ownerByField := map[string]TransitionGroupID{}
	aggregate := []OutputField{}
	for _, groupID := range groupOrder {
		for _, field := range groupFields[groupID] {
			name := strings.TrimSpace(field.Name)
			if name == "" {
				continue
			}
			if owner, exists := ownerByField[name]; exists && owner != groupID {
				w.addDiagnostic(CodeProvisionFieldOverlap, fmt.Sprintf("%s: join aggregate parameter %s is produced by multiple transitions", fmt.Sprintf("Node %s", nodeDisplayName(join)), name), ValidationError{NodeID: NodeIDOf(join), FieldName: name, TransitionGroupID: groupID})
				continue
			}
			ownerByField[name] = groupID
			aggregate = appendUniqueOutputFields(aggregate, []OutputField{field})
		}
	}
	w.joinOutputFieldsByNode[NodeIDOf(join)] = aggregate
}

func (w *DerivedWiring) deriveParameterDependencies(
	incomingByNode map[NodeID][]Edge,
	outgoingByNode map[NodeID][]Edge,
) {
	for joinID := range w.joinOutputFieldsByNode {
		for _, provider := range incomingByNode[joinID] {
			if len(w.requiredProviderFieldsByJoinEdge[provider.ID]) == 0 {
				continue
			}
			for _, outgoing := range outgoingByNode[joinID] {
				w.parameterDependencyEdgesByEdge[provider.ID] = AppendUniqueEdgeIDs(
					w.parameterDependencyEdgesByEdge[provider.ID],
					[]EdgeID{outgoing.ID},
				)
			}
		}
	}
}

// AppendUniqueEdgeIDs returns existing Edge IDs followed by additions that are
// not already present, preserving first-seen order.
func AppendUniqueEdgeIDs(existing []EdgeID, additions ...[]EdgeID) []EdgeID {
	for _, group := range additions {
		for _, addition := range group {
			if slices.Contains(existing, addition) {
				continue
			}
			existing = append(existing, addition)
		}
	}
	return existing
}

func (w *DerivedWiring) deriveCurrentNodeValueEnvironment(
	def Definition,
	nodesByID map[NodeID]Node,
	groupsByID map[TransitionGroupID]TransitionGroup,
	outgoingByNode map[NodeID][]Edge,
	priorReferencesByEdge map[EdgeID][]PromptPriorParameterReference,
) {
	startNodeID, hasSingleStart := singleStartNodeID(def.Nodes)
	priorRequirementsByPromptNode := make(map[NodeID][]derivedPriorParameterRequirement, len(nodesByID))
	for _, edge := range def.Edges {
		_, targetExists := nodesByID[edge.TargetNodeID]
		if !targetExists {
			continue
		}
		consumerGroup, consumerGroupExists := groupsByID[edge.TransitionGroupID]
		if consumerGroupExists {
			source, sourceExists := nodesByID[consumerGroup.SourceNodeID]
			if sourceExists {
				currentFields := w.TransitionOutputFieldsForEdge(edge, source)
				w.currentNodeInputBindingsByEdge[edge.ID] = appendUniqueInputBindings(
					w.currentNodeInputBindingsByEdge[edge.ID],
					inputBindingsForFields(currentFields),
				)
				for _, field := range currentFields {
					w.addCurrentNodeOutputField(consumerGroup.ID, field)
				}
			}
		}
		if !consumerGroupExists {
			continue
		}
		if !hasSingleStart {
			continue
		}
		for _, priorParam := range priorReferencesByEdge[edge.ID] {
			transitionKey := ModelKey(strings.TrimSpace(string(priorParam.TransitionKey)))
			outputName := strings.TrimSpace(priorParam.ParameterKey)
			if transitionKey == "" || outputName == "" {
				continue
			}
			resolution := resolvePriorParameterTransitionGroups(
				def.TransitionGroups,
				transitionKey,
				consumerGroup.SourceNodeID,
				startNodeID,
				outgoingByNode,
			)
			if len(resolution.guaranteed) != 1 {
				continue
			}
			provider, providerExists := nodesByID[resolution.guaranteed[0].SourceNodeID]
			if !providerExists {
				continue
			}
			providerNodeKey := NodeKey(provider)
			if providerNodeKey == "" {
				continue
			}
			priorRequirementsByPromptNode[edge.TargetNodeID] = appendUniqueDerivedPriorParameterRequirements(
				priorRequirementsByPromptNode[edge.TargetNodeID],
				[]derivedPriorParameterRequirement{{
					value: PriorTransitionParameterRequirement{
						ProviderNode:  providerNodeKey,
						TransitionKey: transitionKey,
						ParameterName: outputName,
					},
					providerTransitionGroupID: resolution.guaranteed[0].ID,
				}},
			)
		}
	}
	for nodeID := range nodesByID {
		requirements := []PriorTransitionParameterRequirement{}
		for reachableNodeID := range reachableNodeIDs(nodeID, outgoingByNode) {
			for _, requirement := range priorRequirementsByPromptNode[reachableNodeID] {
				if !transitionGroupDominates(startNodeID, requirement.providerTransitionGroupID, nodeID, outgoingByNode) {
					continue
				}
				requirements = appendUniquePriorParameterRequirements(requirements, []PriorTransitionParameterRequirement{requirement.value})
			}
		}
		w.priorParameterRequirementsByNode[nodeID] = requirements
	}
}

func singleStartNodeID(nodes []Node) (NodeID, bool) {
	var start NodeID
	for _, node := range nodes {
		if node.Kind() != NodeKindStart {
			continue
		}
		if start != "" {
			return "", false
		}
		start = NodeIDOf(node)
	}
	return start, start != ""
}

func resolvePriorParameterTransitionGroups(
	groups []TransitionGroup,
	transitionKey ModelKey,
	consumerSourceNodeID NodeID,
	startNodeID NodeID,
	outgoingByNode map[NodeID][]Edge,
) priorParameterTransitionResolution {
	resolution := priorParameterTransitionResolution{}
	for _, group := range groups {
		if strings.TrimSpace(string(group.TransitionID)) != strings.TrimSpace(string(transitionKey)) {
			continue
		}
		resolution.matched++
		if transitionGroupDominates(startNodeID, group.ID, consumerSourceNodeID, outgoingByNode) {
			resolution.guaranteed = append(resolution.guaranteed, group)
		}
	}
	return resolution
}

func transitionGroupDominates(
	startNodeID NodeID,
	groupID TransitionGroupID,
	targetNodeID NodeID,
	outgoingByNode map[NodeID][]Edge,
) bool {
	if startNodeID == "" {
		return false
	}
	if _, reachable := reachableNodeIDs(startNodeID, outgoingByNode)[targetNodeID]; !reachable {
		return false
	}
	visited := map[NodeID]bool{}
	stack := []NodeID{startNodeID}
	for len(stack) > 0 {
		nodeID := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[nodeID] {
			continue
		}
		if nodeID == targetNodeID {
			return false
		}
		visited[nodeID] = true
		for _, edge := range outgoingByNode[nodeID] {
			if edge.TransitionGroupID == groupID {
				continue
			}
			if !visited[edge.TargetNodeID] {
				stack = append(stack, edge.TargetNodeID)
			}
		}
	}
	return true
}

func nodeDominatesFromStart(
	startNodeID NodeID,
	candidateNodeID NodeID,
	targetNodeID NodeID,
	outgoingByNode map[NodeID][]Edge,
) bool {
	if candidateNodeID == targetNodeID {
		return true
	}
	if startNodeID == "" {
		return false
	}
	if _, reachable := reachableNodeIDs(startNodeID, outgoingByNode)[targetNodeID]; !reachable {
		return false
	}
	visited := map[NodeID]bool{}
	if startNodeID == candidateNodeID {
		return true
	}
	stack := []NodeID{startNodeID}
	for len(stack) > 0 {
		nodeID := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[nodeID] || nodeID == candidateNodeID {
			continue
		}
		visited[nodeID] = true
		for _, edge := range outgoingByNode[nodeID] {
			if !visited[edge.TargetNodeID] && edge.TargetNodeID != candidateNodeID {
				stack = append(stack, edge.TargetNodeID)
			}
		}
	}
	return !visited[targetNodeID]
}

func (w *DerivedWiring) addCurrentNodeOutputField(groupID TransitionGroupID, field OutputField) {
	w.currentNodeOutputFieldsByGroup[groupID] = appendUniqueOutputFields(
		w.currentNodeOutputFieldsByGroup[groupID],
		[]OutputField{field},
	)
}

func outputFieldByName(fields []OutputField, name string) (OutputField, bool) {
	for _, field := range fields {
		if strings.TrimSpace(field.Name) == name {
			return OutputField{Name: name, Description: strings.TrimSpace(field.Description)}, true
		}
	}
	return OutputField{}, false
}

func reachableNodeIDs(start NodeID, outgoingByNode map[NodeID][]Edge) map[NodeID]struct{} {
	visited := map[NodeID]struct{}{}
	stack := []NodeID{start}
	for len(stack) > 0 {
		nodeID := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, seen := visited[nodeID]; seen {
			continue
		}
		visited[nodeID] = struct{}{}
		for _, edge := range outgoingByNode[nodeID] {
			if _, seen := visited[edge.TargetNodeID]; !seen {
				stack = append(stack, edge.TargetNodeID)
			}
		}
	}
	return visited
}

func appendUniqueInputBindings(existing []InputBinding, additions []InputBinding) []InputBinding {
	out := append([]InputBinding(nil), existing...)
	seen := make(map[string]struct{}, len(out))
	for _, binding := range out {
		seen[binding.Name] = struct{}{}
	}
	for _, binding := range additions {
		if _, exists := seen[binding.Name]; exists {
			continue
		}
		seen[binding.Name] = struct{}{}
		out = append(out, binding)
	}
	return out
}

func appendUniquePriorParameterRequirements(existing []PriorTransitionParameterRequirement, additions []PriorTransitionParameterRequirement) []PriorTransitionParameterRequirement {
	out := append([]PriorTransitionParameterRequirement(nil), existing...)
	for _, requirement := range additions {
		if !slices.Contains(out, requirement) {
			out = append(out, requirement)
		}
	}
	return out
}

func appendUniqueDerivedPriorParameterRequirements(existing []derivedPriorParameterRequirement, additions []derivedPriorParameterRequirement) []derivedPriorParameterRequirement {
	out := append([]derivedPriorParameterRequirement(nil), existing...)
	for _, addition := range additions {
		if !slices.Contains(out, addition) {
			out = append(out, addition)
		}
	}
	return out
}

func (w *DerivedWiring) addDiagnostic(code ValidationErrorCode, message string, ref ValidationError) {
	ref.Code = code
	ref.Message = message
	ref.BlocksContext = true
	w.Diagnostics = append(w.Diagnostics, ref)
}

func edgeParameterFields(edge Edge) []OutputField {
	fields := make([]OutputField, 0, len(edge.Parameters))
	for _, parameter := range edge.Parameters {
		key := strings.TrimSpace(parameter.Key)
		description := strings.TrimSpace(parameter.Description)
		if key == "" {
			continue
		}
		fields = append(fields, OutputField{Name: key, Description: description})
	}
	return fields
}

func inputBindingsForFields(fields []OutputField) []InputBinding {
	bindings := make([]InputBinding, 0, len(fields))
	for _, field := range fields {
		name := strings.TrimSpace(field.Name)
		if name == "" {
			continue
		}
		bindings = append(bindings, InputBinding{Name: name, Source: BindingSourceTransitionOutput, Field: name})
	}
	return bindings
}

func appendUniqueOutputFields(existing []OutputField, additions []OutputField) []OutputField {
	out := append([]OutputField(nil), existing...)
	seen := make(map[string]int, len(out))
	for index, field := range out {
		seen[strings.TrimSpace(field.Name)] = index
	}
	for _, field := range additions {
		name := strings.TrimSpace(field.Name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = len(out)
		out = append(out, OutputField{Name: name, Description: strings.TrimSpace(field.Description)})
	}
	return out
}

func appendCompatibleOutputFields(existing []OutputField, additions []OutputField, ref ValidationError) ([]OutputField, []ValidationError) {
	out := append([]OutputField(nil), existing...)
	seen := make(map[string]int, len(out))
	for index, field := range out {
		seen[strings.TrimSpace(field.Name)] = index
	}
	diagnostics := []ValidationError{}
	for _, field := range additions {
		name := strings.TrimSpace(field.Name)
		if name == "" {
			continue
		}
		description := strings.TrimSpace(field.Description)
		if previousIndex, exists := seen[name]; exists {
			if strings.TrimSpace(out[previousIndex].Description) != description {
				diagnostic := ref
				diagnostic.Code = CodeProvisionFieldOverlap
				diagnostic.Message = "derived provision field has incompatible input definitions"
				diagnostic.BlocksContext = true
				diagnostic.FieldName = name
				diagnostics = append(diagnostics, diagnostic)
			}
			continue
		}
		seen[name] = len(out)
		out = append(out, OutputField{Name: name, Description: description})
	}
	return out, diagnostics
}
