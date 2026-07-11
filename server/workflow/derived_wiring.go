package workflow

import (
	"fmt"
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
}

func DeriveWiring(def Definition) DerivedWiring {
	derived := DerivedWiring{
		inputBindingsByEdge:              map[EdgeID][]InputBinding{},
		requiredProvisionFieldsByEdge:    map[EdgeID][]OutputField{},
		requiredProvisionFieldsByGroup:   map[TransitionGroupID][]OutputField{},
		possibleProvisionFieldsByNode:    map[NodeID][]OutputField{},
		requiredProviderFieldsByJoinEdge: map[EdgeID][]OutputField{},
		joinOutputFieldsByNode:           map[NodeID][]OutputField{},
	}
	nodesByID := make(map[NodeID]Node, len(def.Nodes))
	groupsByID := make(map[TransitionGroupID]TransitionGroup, len(def.TransitionGroups))
	incomingByNode := make(map[NodeID][]Edge, len(def.Edges))
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
