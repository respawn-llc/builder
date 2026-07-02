package workflowstore

import (
	"fmt"
	"sort"
	"strings"

	"core/server/workflow"
)

type runStartSnapshot struct {
	WorkflowID           workflow.WorkflowID          `json:"workflow_id"`
	WorkflowRevisionSeen int64                        `json:"workflow_revision_seen"`
	Node                 nodeContractSnapshot         `json:"node"`
	Nodes                []nodeContractSnapshot       `json:"nodes,omitempty"`
	TransitionGroups     []transitionContractSnapshot `json:"transition_groups"`
}

type nodeContractSnapshot struct {
	ID                 workflow.NodeID              `json:"id"`
	Key                workflow.ModelKey            `json:"key"`
	DisplayName        string                       `json:"display_name"`
	Kind               workflow.NodeKind            `json:"kind"`
	SubagentRole       string                       `json:"subagent_role,omitempty"`
	PromptTemplate     string                       `json:"prompt_template,omitempty"`
	CompletionMode     string                       `json:"completion_mode,omitempty"`
	InputFields        []workflow.InputField        `json:"input_fields,omitempty"`
	JoinInputProviders []workflow.JoinInputProvider `json:"join_input_providers,omitempty"`
	OutputFields       []workflow.OutputField       `json:"output_fields,omitempty"`
}

type transitionContractSnapshot struct {
	ID           workflow.TransitionGroupID `json:"id"`
	SourceNodeID workflow.NodeID            `json:"source_node_id,omitempty"`
	TransitionID string                     `json:"transition_id"`
	DisplayName  string                     `json:"display_name"`
	Description  string                     `json:"description,omitempty"`
	Edges        []edgeContractSnapshot     `json:"edges"`
}

type edgeContractSnapshot struct {
	ID                 workflow.EdgeID              `json:"id"`
	Key                workflow.ModelKey            `json:"key"`
	TargetNode         nodeContractSnapshot         `json:"target_node"`
	ContextMode        workflow.ContextMode         `json:"context_mode"`
	ContextSource      workflow.ContextSource       `json:"context_source"`
	RequiresApproval   bool                         `json:"requires_approval"`
	PromptTemplate     string                       `json:"prompt_template,omitempty"`
	Parameters         []workflow.Parameter         `json:"parameters,omitempty"`
	InputBindings      []workflow.InputBinding      `json:"input_bindings,omitempty"`
	OutputRequirements []workflow.OutputRequirement `json:"output_requirements,omitempty"`
}

func nodeRecordFromSnapshot(node nodeContractSnapshot, workflowID workflow.WorkflowID) NodeRecord {
	return NodeRecord{
		ID:                 node.ID,
		WorkflowID:         workflowID,
		Key:                node.Key,
		Kind:               node.Kind,
		DisplayName:        node.DisplayName,
		SubagentRole:       node.SubagentRole,
		PromptTemplate:     node.PromptTemplate,
		CompletionMode:     node.CompletionMode,
		InputFields:        append([]workflow.InputField(nil), node.InputFields...),
		JoinInputProviders: append([]workflow.JoinInputProvider(nil), node.JoinInputProviders...),
		OutputFields:       append([]workflow.OutputField(nil), node.OutputFields...),
	}
}

func transitionIDsFromSnapshot(snapshot runStartSnapshot) []string {
	out := make([]string, 0, len(snapshot.TransitionGroups))
	for _, group := range snapshot.transitionGroupsForNode(snapshot.Node.ID) {
		id := strings.TrimSpace(group.TransitionID)
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

func transitionOptionsFromSnapshot(snapshot runStartSnapshot) []TransitionOption {
	out := make([]TransitionOption, 0, len(snapshot.TransitionGroups))
	for _, group := range snapshot.transitionGroupsForNode(snapshot.Node.ID) {
		id := strings.TrimSpace(group.TransitionID)
		if id == "" {
			continue
		}
		out = append(out, TransitionOption{ID: id, DisplayName: strings.TrimSpace(group.DisplayName), Description: strings.TrimSpace(group.Description), Parameters: transitionParametersFromSnapshot(group)})
	}
	return out
}

func (s runStartSnapshot) hasContinueSessionEdge() bool {
	for _, group := range s.TransitionGroups {
		for _, edge := range group.Edges {
			if edge.ContextMode == workflow.ContextModeContinueSession {
				return true
			}
		}
	}
	return false
}

func transitionParametersFromSnapshot(group transitionContractSnapshot) []workflow.Parameter {
	out := []workflow.Parameter{}
	seen := map[string]bool{}
	for _, edge := range group.Edges {
		for _, parameter := range edge.Parameters {
			key := strings.TrimSpace(parameter.Key)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, workflow.Parameter{Key: key, Description: strings.TrimSpace(parameter.Description)})
		}
	}
	return out
}

func mustInputBindingsJSON(value []workflow.InputBinding) string {
	if value == nil {
		value = []workflow.InputBinding{}
	}
	return workflow.MustMarshalString(value)
}

func mustOutputRequirementsJSON(value []workflow.OutputRequirement) string {
	if value == nil {
		value = []workflow.OutputRequirement{}
	}
	return workflow.MustMarshalString(value)
}

func newRunStartSnapshot(def workflow.Definition, record WorkflowRecord, nodeID workflow.NodeID) (runStartSnapshot, error) {
	derived := workflow.DeriveWiring(def)
	nodes := make(map[workflow.NodeID]workflow.Node, len(def.Nodes))
	for _, node := range def.Nodes {
		nodes[workflow.NodeIDOf(node)] = node
	}
	node, ok := nodes[nodeID]
	if !ok {
		return runStartSnapshot{}, fmt.Errorf("snapshot node %q missing", nodeID)
	}
	edgesByGroup := make(map[workflow.TransitionGroupID][]workflow.Edge, len(def.Edges))
	for _, edge := range def.Edges {
		edgesByGroup[edge.TransitionGroupID] = append(edgesByGroup[edge.TransitionGroupID], edge)
	}
	snapshot := runStartSnapshot{
		WorkflowID:           record.ID,
		WorkflowRevisionSeen: record.Version,
		Node:                 nodeSnapshotWithDerivedWiring(node, derived),
	}
	for _, defNode := range def.Nodes {
		snapshot.Nodes = append(snapshot.Nodes, nodeSnapshotWithDerivedWiring(defNode, derived))
	}
	for _, group := range def.TransitionGroups {
		groupSnapshot := transitionContractSnapshot{ID: group.ID, SourceNodeID: group.SourceNodeID, TransitionID: string(group.TransitionID), DisplayName: group.DisplayName, Description: group.Description}
		source := nodes[group.SourceNodeID]
		for _, edge := range edgesByGroup[group.ID] {
			target, ok := nodes[edge.TargetNodeID]
			if !ok {
				return runStartSnapshot{}, fmt.Errorf("snapshot edge target %q missing", edge.TargetNodeID)
			}
			groupSnapshot.Edges = append(groupSnapshot.Edges, edgeSnapshotWithDerivedWiring(edge, source, target, derived))
		}
		snapshot.TransitionGroups = append(snapshot.TransitionGroups, groupSnapshot)
	}
	return snapshot, nil
}

func nodeSnapshotWithDerivedWiring(node workflow.Node, derived workflow.DerivedWiring) nodeContractSnapshot {
	snapshot := nodeSnapshot(node)
	snapshot.OutputFields = derived.PossibleProvisionFieldsForNode(workflow.NodeIDOf(node))
	return snapshot
}

func nodeSnapshot(node workflow.Node) nodeContractSnapshot {
	return nodeContractSnapshot{
		ID:                 workflow.NodeIDOf(node),
		Key:                workflow.NodeKey(node),
		DisplayName:        workflow.NodeDisplayName(node),
		Kind:               node.Kind(),
		SubagentRole:       workflow.NodeSubagentRole(node),
		PromptTemplate:     workflow.NodePromptTemplate(node),
		CompletionMode:     workflow.NodeCompletionMode(node),
		InputFields:        workflow.NodeInputFields(node),
		JoinInputProviders: workflow.NodeJoinInputProviders(node),
		OutputFields:       workflow.NodeOutputFields(node),
	}
}

func edgeSnapshotWithDerivedWiring(edge workflow.Edge, source workflow.Node, target workflow.Node, derived workflow.DerivedWiring) edgeContractSnapshot {
	return edgeContractSnapshot{
		ID:                 edge.ID,
		Key:                edge.Key,
		TargetNode:         nodeSnapshotWithDerivedWiring(target, derived),
		ContextMode:        edge.ContextMode,
		ContextSource:      workflow.CanonicalContextSource(edge.ContextSource),
		RequiresApproval:   edge.RequiresApproval,
		PromptTemplate:     strings.TrimSpace(edge.PromptTemplate),
		Parameters:         edgeParametersSnapshot(edge, source, derived),
		InputBindings:      edgeInputBindingsSnapshot(edge, source, derived),
		OutputRequirements: edgeOutputRequirementsSnapshot(edge, source, target, derived),
	}
}

func edgeParametersSnapshot(edge workflow.Edge, source workflow.Node, derived workflow.DerivedWiring) []workflow.Parameter {
	if source.Kind() == workflow.NodeKindJoin {
		return parametersFromOutputFields(derived.JoinOutputFieldsForNode(workflow.NodeIDOf(source)))
	}
	return append([]workflow.Parameter(nil), edge.Parameters...)
}

func parametersFromOutputFields(fields []workflow.OutputField) []workflow.Parameter {
	out := make([]workflow.Parameter, 0, len(fields))
	for _, field := range fields {
		key := strings.TrimSpace(field.Name)
		if key != "" {
			out = append(out, workflow.Parameter{Key: key, Description: strings.TrimSpace(field.Description)})
		}
	}
	return out
}

func edgeInputBindingsSnapshot(edge workflow.Edge, source workflow.Node, derived workflow.DerivedWiring) []workflow.InputBinding {
	if source.Kind() == workflow.NodeKindJoin {
		return inputBindingsForOutputFields(derived.JoinOutputFieldsForNode(workflow.NodeIDOf(source)))
	}
	return derived.InputBindingsForEdge(edge.ID)
}

func inputBindingsForOutputFields(fields []workflow.OutputField) []workflow.InputBinding {
	bindings := make([]workflow.InputBinding, 0, len(fields))
	for _, field := range fields {
		name := strings.TrimSpace(field.Name)
		if name != "" {
			bindings = append(bindings, workflow.InputBinding{Name: name, Source: workflow.BindingSourceJoin, Field: name})
		}
	}
	return bindings
}

func edgeOutputRequirementsSnapshot(edge workflow.Edge, source workflow.Node, target workflow.Node, derived workflow.DerivedWiring) []workflow.OutputRequirement {
	fields := derived.TransitionOutputFieldsForEdge(edge, source)
	if target.Kind() == workflow.NodeKindJoin {
		fields = derived.RequiredProviderFieldsForJoinEdge(edge.ID)
	}
	requirements := make([]workflow.OutputRequirement, 0, len(fields))
	for _, field := range fields {
		if strings.TrimSpace(field.Name) != "" {
			requirements = append(requirements, workflow.OutputRequirement{FieldName: strings.TrimSpace(field.Name)})
		}
	}
	return requirements
}

func (s runStartSnapshot) transitionByID(transitionID string) (transitionContractSnapshot, bool) {
	for _, group := range s.transitionGroupsForNode(s.Node.ID) {
		if group.TransitionID == transitionID {
			return group, true
		}
	}
	return transitionContractSnapshot{}, false
}

func (s runStartSnapshot) transitionGroupsForNode(nodeID workflow.NodeID) []transitionContractSnapshot {
	out := make([]transitionContractSnapshot, 0, len(s.TransitionGroups))
	for _, group := range s.TransitionGroups {
		if group.SourceNodeID == "" || group.SourceNodeID == nodeID {
			out = append(out, group)
		}
	}
	return out
}

func (s runStartSnapshot) hasFullGraphContract() bool {
	return len(s.Nodes) > 0
}

func (s runStartSnapshot) forNode(target nodeContractSnapshot) (runStartSnapshot, bool, error) {
	if !s.hasFullGraphContract() {
		return runStartSnapshot{}, false, nil
	}
	node, ok := s.nodeByID(target.ID)
	if !ok {
		return runStartSnapshot{}, true, fmt.Errorf("snapshot target node %q missing", target.ID)
	}
	return runStartSnapshot{
		WorkflowID:           s.WorkflowID,
		WorkflowRevisionSeen: s.WorkflowRevisionSeen,
		Node:                 node,
		Nodes:                append([]nodeContractSnapshot(nil), s.Nodes...),
		TransitionGroups:     cloneTransitionContractSnapshots(s.TransitionGroups),
	}, true, nil
}

func (s runStartSnapshot) nodeByID(nodeID workflow.NodeID) (nodeContractSnapshot, bool) {
	for _, node := range s.Nodes {
		if node.ID == nodeID {
			return node, true
		}
	}
	return nodeContractSnapshot{}, false
}

func (s runStartSnapshot) nodeByKey(nodeKey workflow.ModelKey) (nodeContractSnapshot, bool) {
	for _, node := range s.Nodes {
		if node.Key == nodeKey {
			return node, true
		}
	}
	return nodeContractSnapshot{}, false
}

func cloneTransitionContractSnapshots(groups []transitionContractSnapshot) []transitionContractSnapshot {
	out := make([]transitionContractSnapshot, 0, len(groups))
	for _, group := range groups {
		group.Edges = append([]edgeContractSnapshot(nil), group.Edges...)
		out = append(out, group)
	}
	return out
}

func (g transitionContractSnapshot) unsupportedRuntimeIssues() []workflow.RuntimeSupportIssue {
	issues := []workflow.RuntimeSupportIssue{}
	for _, edge := range g.Edges {
		issues = append(issues, workflow.UnsupportedRuntimeFeatures(workflow.RuntimeSupportEdge{
			ContextMode:      edge.ContextMode,
			RequiresApproval: edge.RequiresApproval,
			TargetKind:       edge.TargetNode.Kind,
			InputBindings:    edge.InputBindings,
		})...)
	}
	return issues
}

func requiredOutputIssues(group transitionContractSnapshot, values map[string]string) []CompletionValidationIssue {
	issues := []CompletionValidationIssue{}
	for _, edge := range group.Edges {
		for _, requirement := range edge.OutputRequirements {
			if strings.TrimSpace(values[requirement.FieldName]) == "" {
				issues = append(issues, CompletionValidationIssue{Code: CompletionCodeRequiredOutputMissing, Field: requirement.FieldName, Message: "required output is missing"})
			}
		}
	}
	return issues
}

func knownOutputIssues(group transitionContractSnapshot, values map[string]string) []CompletionValidationIssue {
	known := map[string]bool{}
	for _, edge := range group.Edges {
		for _, requirement := range edge.OutputRequirements {
			name := strings.TrimSpace(requirement.FieldName)
			if name != "" {
				known[name] = true
			}
		}
	}
	issues := []CompletionValidationIssue{}
	for _, name := range sortedStringKeys(values) {
		field := strings.TrimSpace(name)
		if field == "" {
			continue
		}
		if !known[field] {
			issues = append(issues, CompletionValidationIssue{Code: CompletionCodeUnknownOutputField, Field: field, Message: "output field is not declared by source node"})
		}
	}
	return issues
}

func sortedStringKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
