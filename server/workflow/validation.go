package workflow

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/toolspec"
	"core/shared/workflowcontract"
	"core/shared/workflowkey"
)

func ValidateDefinition(def Definition, opts ValidationOptions) ValidationResult {
	context := opts.Context
	if context == "" {
		context = ValidationContextDraft
	}
	state := newValidationState(def, opts, context)
	state.validateShape()
	if def.ID.IsZero() {
		return ValidationResult{Context: context, Errors: state.errors}
	}
	state.validateGraph()
	state.validateFanouts()
	state.validateDerivedWiring()
	return ValidationResult{Context: context, Errors: state.errors}
}

type validationState struct {
	def            Definition
	opts           ValidationOptions
	context        ValidationContext
	errors         []ValidationError
	nodesByID      map[NodeID]Node
	nodeKeys       map[ModelKey]NodeID
	nodeGroupsByID map[string]NodeGroup
	groupsByID     map[TransitionGroupID]TransitionGroup
	edgesByID      map[EdgeID]Edge
	edgesByGroup   map[TransitionGroupID][]Edge
	groupsBySource map[NodeID][]TransitionGroup
	outgoingByNode map[NodeID][]Edge
	incomingByNode map[NodeID][]Edge
	startNodes     []Node
}

func newValidationState(def Definition, opts ValidationOptions, context ValidationContext) *validationState {
	return &validationState{
		def:            def,
		opts:           opts,
		context:        context,
		nodesByID:      map[NodeID]Node{},
		nodeKeys:       map[ModelKey]NodeID{},
		nodeGroupsByID: map[string]NodeGroup{},
		groupsByID:     map[TransitionGroupID]TransitionGroup{},
		edgesByID:      map[EdgeID]Edge{},
		edgesByGroup:   map[TransitionGroupID][]Edge{},
		groupsBySource: map[NodeID][]TransitionGroup{},
		outgoingByNode: map[NodeID][]Edge{},
		incomingByNode: map[NodeID][]Edge{},
	}
}

func (s *validationState) validateShape() {
	if s.def.ID.IsZero() {
		s.addHard(CodeMissingWorkflowID, "workflow id is required", ValidationError{})
		return
	}
	if !validDisplayName(s.def.DisplayName) {
		s.addHard(CodeInvalidDisplayName, "workflow display name must be non-empty and at most 120 characters", ValidationError{WorkflowID: WorkflowIDPointer(s.def.ID)})
	}
	s.validateExecutionTargetPolicy()

	s.indexNodeGroups()
	s.indexNodes()
	s.indexTransitionGroups()
	s.indexEdges()
	s.validateNodes()
	s.validateNodeGroups()
	s.validateTransitionGroups()
	s.validateEdges()
}

func (s *validationState) validateExecutionTargetPolicy() {
	policy := s.def.ExecutionTargetPolicy.Canonical()
	if !validExecutionTargetPolicyMode(policy.Mode) {
		s.addHard(CodeInvalidExecutionTargetPolicy, "execution target policy mode is invalid", ValidationError{WorkflowID: WorkflowIDPointer(s.def.ID)})
		return
	}
	if policy.Mode != ExecutionTargetModeCustomRef {
		if policy.CustomRef != nil {
			s.addHard(CodeInvalidExecutionTargetPolicy, "execution target custom ref is only valid for custom_ref policy", ValidationError{WorkflowID: WorkflowIDPointer(s.def.ID)})
		}
		return
	}
	if policy.CustomRef == nil || strings.TrimSpace(*policy.CustomRef) == "" {
		s.addSemantic(CodeExecutionTargetCustomRefRequired, "custom_ref execution target policy requires a custom ref", ValidationError{WorkflowID: WorkflowIDPointer(s.def.ID)})
	}
}

func (s *validationState) indexNodeGroups() {
	seenKeys := map[ModelKey]string{}
	for _, group := range s.def.NodeGroups {
		ref := (ValidationError{WorkflowID: WorkflowIDPointer(s.def.ID)}).withRelatedEntity(workflowcontract.WorkflowGraphEntityTypeNodeGroup, group.ID)
		if group.WorkflowID.IsZero() {
			s.addHard(CodeMissingWorkflowID, "node group workflow id is required", ref)
		} else if group.WorkflowID != s.def.ID {
			s.addHard(CodeCrossWorkflowReference, "node group references another workflow", ref)
		}
		id := strings.TrimSpace(group.ID)
		if id == "" {
			s.addHard(CodeInvalidNodeGroup, "node group id is required", ref)
			continue
		}
		if _, exists := s.nodeGroupsByID[id]; exists {
			s.addHard(CodeInvalidNodeGroup, "node group id must be unique", ref)
			continue
		}
		key := ModelKey(strings.TrimSpace(string(group.Key)))
		if key == "" || !workflowkey.Valid(string(key)) {
			s.addHard(CodeInvalidNodeGroup, "node group key is invalid", ref)
		} else if previousID, exists := seenKeys[key]; exists && previousID != id {
			s.addHard(CodeInvalidNodeGroup, "node group key must be unique", ref.withRelatedEntity(workflowcontract.WorkflowGraphEntityTypeNodeGroup, previousID))
		} else {
			seenKeys[key] = id
		}
		if !validDisplayName(group.DisplayName) {
			s.addHard(CodeInvalidDisplayName, "node group display name must be non-empty and at most 120 characters", ref)
		}
		s.nodeGroupsByID[id] = group
	}
}

func (s *validationState) indexNodes() {
	for _, node := range s.def.Nodes {
		nodeID := NodeIDOf(node)
		ref := ValidationError{WorkflowID: WorkflowIDPointer(s.def.ID), NodeID: &nodeID}
		nodeWorkflowID := NodeWorkflowID(node)
		if nodeWorkflowID == nil || nodeWorkflowID.IsZero() {
			s.addHard(CodeMissingWorkflowID, "node workflow id is required", ref)
		} else if *nodeWorkflowID != s.def.ID {
			s.addHard(CodeCrossWorkflowReference, "node references another workflow", ref)
		}
		if strings.TrimSpace(string(nodeID)) == "" {
			s.addHard(CodeMissingNodeID, "node id is required", ref)
			continue
		}
		if _, exists := s.nodesByID[nodeID]; exists {
			s.addHard(CodeDuplicateNodeID, "node id must be unique", ref)
			continue
		}
		s.nodesByID[nodeID] = node
		if node.Kind() == NodeKindStart {
			s.startNodes = append(s.startNodes, node)
		}
	}
}

func (s *validationState) indexTransitionGroups() {
	seenTransitions := map[string]TransitionGroupID{}
	for _, group := range s.def.TransitionGroups {
		ref := ValidationError{
			WorkflowID:        WorkflowIDPointer(s.def.ID),
			TransitionGroupID: &group.ID,
			NodeID:            &group.SourceNodeID,
		}
		if group.WorkflowID.IsZero() {
			s.addHard(CodeMissingWorkflowID, "transition group workflow id is required", ref)
		} else if group.WorkflowID != s.def.ID {
			s.addHard(CodeCrossWorkflowReference, "transition group references another workflow", ref)
		}
		if strings.TrimSpace(string(group.ID)) == "" {
			s.addHard(CodeMissingTransitionGroupID, "transition group id is required", ref)
			continue
		}
		if _, exists := s.groupsByID[group.ID]; exists {
			s.addHard(CodeDuplicateTransitionGroupID, "transition group id must be unique", ref)
			continue
		}
		s.groupsByID[group.ID] = group
		s.groupsBySource[group.SourceNodeID] = append(s.groupsBySource[group.SourceNodeID], group)
		transitionID := strings.TrimSpace(string(group.TransitionID))
		if transitionID == "" {
			s.addHard(CodeMissingTransitionID, "transition id is required", ref)
		} else if !workflowkey.Valid(transitionID) {
			s.addHard(CodeInvalidTransitionID, "transition id must "+workflowkey.Description, ref)
		} else {
			if previousID, exists := seenTransitions[transitionID]; exists {
				s.addHard(CodeDuplicateTransitionID, "transition id must be unique across the Workflow", ref.withRelatedEntity(workflowcontract.WorkflowGraphEntityTypeTransitionGroup, string(previousID)))
			}
			seenTransitions[transitionID] = group.ID
		}
		if !validDisplayName(group.DisplayName) {
			s.addHard(CodeInvalidDisplayName, "transition group display name must be non-empty and at most 120 characters", ref)
		}
	}
}

func (s *validationState) indexEdges() {
	for _, edge := range s.def.Edges {
		ref := ValidationError{
			WorkflowID:        WorkflowIDPointer(s.def.ID),
			EdgeID:            &edge.ID,
			TransitionGroupID: &edge.TransitionGroupID,
		}
		if edge.WorkflowID.IsZero() {
			s.addHard(CodeMissingWorkflowID, "edge workflow id is required", ref)
		} else if edge.WorkflowID != s.def.ID {
			s.addHard(CodeCrossWorkflowReference, "edge references another workflow", ref)
		}
		if strings.TrimSpace(string(edge.ID)) == "" {
			s.addHard(CodeMissingEdgeID, "edge id is required", ref)
			continue
		}
		if _, exists := s.edgesByID[edge.ID]; exists {
			s.addHard(CodeDuplicateEdgeID, "edge id must be unique", ref)
			continue
		}
		s.edgesByID[edge.ID] = edge
		s.edgesByGroup[edge.TransitionGroupID] = append(s.edgesByGroup[edge.TransitionGroupID], edge)
		group, exists := s.groupsByID[edge.TransitionGroupID]
		if exists {
			s.outgoingByNode[group.SourceNodeID] = append(s.outgoingByNode[group.SourceNodeID], edge)
			s.incomingByNode[edge.TargetNodeID] = append(s.incomingByNode[edge.TargetNodeID], edge)
		}
	}
}

func (s *validationState) validateNodes() {
	for _, node := range s.def.Nodes {
		nodeID := NodeIDOf(node)
		ref := ValidationError{WorkflowID: WorkflowIDPointer(s.def.ID), NodeID: &nodeID}
		if strings.TrimSpace(string(NodeKey(node))) == "" {
			s.addHard(CodeMissingNodeKey, "node key is required", ref)
		} else if !workflowkey.Valid(string(NodeKey(node))) {
			s.addHard(CodeInvalidNodeKey, "node key must "+workflowkey.Description, ref)
		} else if previousNodeID, exists := s.nodeKeys[NodeKey(node)]; exists && previousNodeID != NodeIDOf(node) {
			s.addHard(CodeDuplicateNodeKey, "node key must be unique", ref.withRelatedEntity(workflowcontract.WorkflowGraphEntityTypeNode, string(previousNodeID)))
		} else {
			s.nodeKeys[NodeKey(node)] = NodeIDOf(node)
		}
		if !validDisplayName(NodeDisplayName(node)) {
			s.addHard(CodeInvalidDisplayName, "node display name must be non-empty and at most 120 characters", ref)
		}
		switch node.Kind() {
		case NodeKindStart, NodeKindAgent, NodeKindScript, NodeKindJoin, NodeKindTerminal:
		default:
			s.addHard(CodeInvalidNodeKind, "node kind is invalid", ref)
		}
		if groupID, present := NodeGroupID(node); present {
			if _, exists := s.nodeGroupsByID[strings.TrimSpace(groupID)]; !exists {
				s.addHard(CodeInvalidNodeGroup, "node references a missing node group", ref)
			}
		}
	}
	if len(s.startNodes) == 0 {
		s.addHard(CodeMissingStartNode, "workflow must contain exactly one start node", ValidationError{WorkflowID: WorkflowIDPointer(s.def.ID)})
	}
	if len(s.startNodes) > 1 {
		ref := ValidationError{WorkflowID: WorkflowIDPointer(s.def.ID)}
		for _, node := range s.startNodes {
			ref = ref.withRelatedEntity(workflowcontract.WorkflowGraphEntityTypeNode, string(NodeIDOf(node)))
		}
		s.addHard(CodeMultipleStartNodes, "workflow must contain exactly one start node", ref)
	}
}

func (s *validationState) validateNodeGroups() {
	for _, group := range s.def.NodeGroups {
		ref := (ValidationError{WorkflowID: WorkflowIDPointer(s.def.ID)}).withRelatedEntity(workflowcontract.WorkflowGraphEntityTypeNodeGroup, group.ID)
		members := s.nodeGroupMembers(group)
		branchIDs := map[NodeID]bool{}
		joinIDs := []NodeID{}
		for _, node := range members {
			switch node.Kind() {
			case NodeKindAgent, NodeKindScript:
				branchIDs[NodeIDOf(node)] = true
			case NodeKindJoin:
				joinIDs = append(joinIDs, NodeIDOf(node))
			default:
				s.addHard(CodeInvalidNodeGroup, "node group members must be agent branches plus one join", ref)
			}
		}
		if len(branchIDs) < 2 {
			s.addHard(CodeInvalidNodeGroup, "node group must contain at least two branch nodes", ref)
		}
		if len(joinIDs) != 1 {
			s.addHard(CodeInvalidNodeGroup, "node group must contain exactly one join node", ref)
			continue
		}
		if len(branchIDs) >= 2 {
			message := s.nodeGroupV1FanoutTopologyError(branchIDs, joinIDs[0])
			if message != "" {
				s.addHard(CodeInvalidNodeGroup, message, ref)
			}
		}
	}
}

func (s *validationState) nodeGroupMembers(group NodeGroup) []Node {
	memberIDs := map[NodeID]bool{}
	for _, nodeID := range group.MemberNodeIDs {
		memberIDs[nodeID] = true
	}
	for _, node := range s.def.Nodes {
		if groupID, present := NodeGroupID(node); present && strings.TrimSpace(groupID) == group.ID {
			memberIDs[NodeIDOf(node)] = true
		}
	}
	members := make([]Node, 0, len(memberIDs))
	for nodeID := range memberIDs {
		if node, exists := s.nodesByID[nodeID]; exists {
			members = append(members, node)
		}
	}
	return members
}

func (s *validationState) nodeGroupV1FanoutTopologyError(branchIDs map[NodeID]bool, joinID NodeID) string {
	fanoutGroups := []TransitionGroup{}
	for _, group := range s.def.TransitionGroups {
		edges := s.edgesByGroup[group.ID]
		if len(edges) < 2 {
			continue
		}
		if transitionGroupTargetsExactly(edges, branchIDs) {
			fanoutGroups = append(fanoutGroups, group)
		}
	}
	if len(fanoutGroups) != 1 {
		if s.nodeGroupBranchesHaveStartIncoming(branchIDs) {
			return fmt.Sprintf("%s cannot directly fan out into a node group yet; insert one split agent after it, fan out from that agent into the group, then join the branches", fmt.Sprintf("Node %s", nodeDisplayName(s.startNodes[0])))
		}
		if message := s.nodeGroupSeparateFanoutMessage(branchIDs); message != "" {
			return message
		}
		return "node group must be represented by one fan-out transition group and branch edges into its join"
	}
	source, exists := s.nodesByID[fanoutGroups[0].SourceNodeID]
	if exists && source.Kind() == NodeKindStart {
		return fmt.Sprintf("%s cannot directly fan out into a node group yet; insert one split agent after it, fan out from that agent into the group, then join the branches", fmt.Sprintf("Node %s", nodeDisplayName(source)))
	}
	if s.nodeGroupBranchesHaveStartIncoming(branchIDs) {
		return fmt.Sprintf("%s cannot directly fan out into a node group yet; insert one split agent after it, fan out from that agent into the group, then join the branches", fmt.Sprintf("Node %s", nodeDisplayName(s.startNodes[0])))
	}
	if message := s.nodeGroupSeparateFanoutMessage(branchIDs); message != "" {
		return message
	}
	for branchID := range branchIDs {
		if !s.nodeHasOutgoingEdgeTo(branchID, joinID) {
			return "node group must be represented by one fan-out transition group and branch edges into its join"
		}
	}
	return ""
}

func transitionGroupTargetsExactly(edges []Edge, branchIDs map[NodeID]bool) bool {
	if len(edges) != len(branchIDs) {
		return false
	}
	targets := map[NodeID]bool{}
	for _, edge := range edges {
		targets[edge.TargetNodeID] = true
	}
	return nodeIDSetEqual(branchIDs, targets)
}

func (s *validationState) nodeGroupBranchesHaveStartIncoming(branchIDs map[NodeID]bool) bool {
	if len(s.startNodes) != 1 {
		return false
	}
	startID := NodeIDOf(s.startNodes[0])
	for branchID := range branchIDs {
		hasStartIncoming := false
		for _, edge := range s.incomingByNode[branchID] {
			if s.groupsByID[edge.TransitionGroupID].SourceNodeID == startID {
				hasStartIncoming = true
				break
			}
		}
		if !hasStartIncoming {
			return false
		}
	}
	return true
}

func (s *validationState) nodeGroupSeparateFanoutMessage(branchIDs map[NodeID]bool) string {
	type sourceFanout struct {
		branchIDs          map[NodeID]bool
		transitionGroupIDs map[TransitionGroupID]bool
	}
	bySource := map[NodeID]sourceFanout{}
	for branchID := range branchIDs {
		for _, edge := range s.incomingByNode[branchID] {
			group := s.groupsByID[edge.TransitionGroupID]
			fanout := bySource[group.SourceNodeID]
			if fanout.branchIDs == nil {
				fanout = sourceFanout{
					branchIDs:          map[NodeID]bool{},
					transitionGroupIDs: map[TransitionGroupID]bool{},
				}
			}
			fanout.branchIDs[branchID] = true
			fanout.transitionGroupIDs[edge.TransitionGroupID] = true
			bySource[group.SourceNodeID] = fanout
		}
	}
	for sourceID, fanout := range bySource {
		if len(fanout.branchIDs) != len(branchIDs) || len(fanout.transitionGroupIDs) < 2 {
			continue
		}
		source, exists := s.nodesByID[sourceID]
		if !exists {
			continue
		}
		return fmt.Sprintf("%s uses separate transitions into the node group branches; use one transition from %s with one edge to each branch, then connect every branch to the join", fmt.Sprintf("Node %s", nodeDisplayName(source)), NodeDisplayName(source))
	}
	return ""
}

func (s *validationState) nodeHasOutgoingEdgeTo(sourceID NodeID, targetID NodeID) bool {
	for _, edge := range s.outgoingByNode[sourceID] {
		if edge.TargetNodeID == targetID {
			return true
		}
	}
	return false
}

func nodeIDSetEqual(left map[NodeID]bool, right map[NodeID]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for id := range left {
		if !right[id] {
			return false
		}
	}
	return true
}

func (s *validationState) validateTransitionGroups() {
	for _, group := range s.def.TransitionGroups {
		ref := ValidationError{
			WorkflowID:        WorkflowIDPointer(s.def.ID),
			TransitionGroupID: &group.ID,
			NodeID:            &group.SourceNodeID,
		}
		if strings.TrimSpace(string(group.SourceNodeID)) == "" {
			s.addHard(CodeEdgeTransitionGroupMissing, "transition group source node is required", ref)
		} else if _, exists := s.nodesByID[group.SourceNodeID]; !exists {
			s.addHard(CodeEdgeTransitionGroupMissing, "transition group source node must exist", ref)
		}
		if len(s.edgesByGroup[group.ID]) == 0 {
			s.addHard(CodeEmptyTransitionGroup, "transition group must contain at least one edge", ref)
		}
		seenEdgeKeys := map[ModelKey]EdgeID{}
		for _, edge := range s.edgesByGroup[group.ID] {
			if strings.TrimSpace(string(edge.Key)) == "" || !workflowkey.Valid(string(edge.Key)) {
				continue
			}
			if previousID, exists := seenEdgeKeys[edge.Key]; exists && previousID != edge.ID {
				s.addHard(CodeDuplicateEdgeKey, "edge key must be unique per transition group", (ValidationError{
					WorkflowID:        WorkflowIDPointer(s.def.ID),
					EdgeID:            &edge.ID,
					TransitionGroupID: &edge.TransitionGroupID,
				}).withRelatedEntity(workflowcontract.WorkflowGraphEntityTypeEdge, string(previousID)))
			}
			seenEdgeKeys[edge.Key] = edge.ID
		}
	}
}

func (s *validationState) validateEdges() {
	for _, edge := range s.def.Edges {
		ref := ValidationError{
			WorkflowID:        WorkflowIDPointer(s.def.ID),
			EdgeID:            &edge.ID,
			TransitionGroupID: &edge.TransitionGroupID,
		}
		if _, groupExists := s.groupsByID[edge.TransitionGroupID]; !groupExists {
			s.addHard(CodeEdgeTransitionGroupMissing, "edge transition group must exist", ref)
		}
		if strings.TrimSpace(string(edge.Key)) == "" {
			s.addHard(CodeMissingEdgeKey, "edge key is required", ref)
		} else if !workflowkey.Valid(string(edge.Key)) {
			s.addHard(CodeInvalidEdgeKey, "edge key must "+workflowkey.Description, ref)
		}
		if strings.TrimSpace(string(edge.TargetNodeID)) == "" {
			s.addHard(CodeEdgeTargetMissing, "edge target node is required", ref)
		} else if _, exists := s.nodesByID[edge.TargetNodeID]; !exists {
			s.addHard(CodeEdgeTargetMissing, "edge target node must exist", ref)
		}
		if !validContextMode(edge.ContextMode) {
			s.addHard(CodeInvalidContextMode, "edge context mode is invalid", ref)
		}
		if selection := CanonicalAssigneeSelection(edge.AssigneeSelection); selection != AssigneeSelectionConfigured && selection != AssigneeSelectionPreviousNode {
			s.addHard(CodeInvalidAssigneeSelection, "edge assignee selection is invalid", ref)
		}
		if selection := CanonicalThinkingSelection(edge.ThinkingSelection); selection != ThinkingSelectionConfigured && selection != ThinkingSelectionPreviousNode {
			s.addHard(CodeInvalidThinkingSelection, "edge thinking selection is invalid", ref)
		}
		s.validateEdgeInvocationContract(edge, ref)
	}
}

func (s *validationState) validateEdgeInvocationContract(edge Edge, ref ValidationError) {
	target, targetExists := s.nodesByID[edge.TargetNodeID]
	source, sourceExists := s.edgeSource(edge)
	prompt := strings.TrimSpace(edge.PromptTemplate)
	if targetExists && target.Kind() == NodeKindAgent {
		if prompt == "" && s.context != ValidationContextDraft {
			s.addHard(CodeTransitionPromptRequired, "transition into an agent node requires a prompt", ref)
		}
	} else if prompt != "" {
		s.addHard(CodeTransitionPromptForbidden, "transition into a non-agent node cannot have a prompt", ref)
	}
	if sourceExists {
		switch source.Kind() {
		case NodeKindStart:
			if len(edge.Parameters) > 0 {
				s.addHard(CodeInvalidParameter, "start transitions cannot declare parameters", ref)
			}
		case NodeKindJoin:
			if len(edge.Parameters) > 0 {
				s.addHard(CodeInvalidParameter, "join outgoing transitions cannot declare parameters", ref)
			}
		}
	}
	s.validateParameters(edge, ref)
}

func (s *validationState) edgeSource(edge Edge) (Node, bool) {
	group, groupExists := s.groupsByID[edge.TransitionGroupID]
	if !groupExists {
		return nil, false
	}
	source, sourceExists := s.nodesByID[group.SourceNodeID]
	return source, sourceExists
}

func (s *validationState) validateParameters(edge Edge, ref ValidationError) {
	seen := map[string]bool{}
	seenPurposes := map[ParameterPurpose]bool{}
	for index, parameter := range edge.Parameters {
		ordinal := index + 1
		key := strings.TrimSpace(parameter.Key)
		parameterRef := ref
		parameterRef.FieldName = key
		purpose := CanonicalParameterPurpose(parameter.Purpose)
		switch purpose {
		case ParameterPurposeOrdinary, ParameterPurposeTargetAssignee, ParameterPurposeTargetThinking:
		default:
			s.addHard(CodeInvalidParameterPurpose, fmt.Sprintf("%s: parameter #%d purpose is invalid", edgeMessageSubject(edge), ordinal), parameterRef)
		}
		if purpose != ParameterPurposeOrdinary {
			if seenPurposes[purpose] {
				s.addHard(CodeDuplicateProtectedParameter, fmt.Sprintf("%s: parameter #%d protected purpose must be unique", edgeMessageSubject(edge), ordinal), parameterRef)
			}
			seenPurposes[purpose] = true
		}
		if key == "" || !workflowkey.Valid(key) || len(key) > MaxParameterKeyChars || workflowkey.ReservedParameter(key) {
			s.addHard(CodeInvalidParameter, fmt.Sprintf("%s: parameter #%d %s", edgeMessageSubject(edge), ordinal, "key is invalid"), parameterRef)
		}
		if seen[key] {
			s.addHard(CodeDuplicateParameter, fmt.Sprintf("%s: parameter #%d %s", edgeMessageSubject(edge), ordinal, "key must be unique per transition branch"), parameterRef)
		}
		seen[key] = true
		description := strings.TrimSpace(parameter.Description)
		if purpose == ParameterPurposeOrdinary && description == "" {
			s.addHard(CodeParameterDescriptionRequired, fmt.Sprintf("%s: parameter #%d %s", edgeMessageSubject(edge), ordinal, "description is required"), parameterRef)
		} else if len(description) > MaxParameterDescriptionChars {
			s.addHard(CodeParameterSchemaTooLarge, fmt.Sprintf("%s: parameter #%d %s", edgeMessageSubject(edge), ordinal, "description is too large"), parameterRef)
		}
	}
	if CanonicalAssigneeSelection(edge.AssigneeSelection) == AssigneeSelectionPreviousNode && !seenPurposes[ParameterPurposeTargetAssignee] {
		s.addHard(CodeMissingProtectedParameter, "edge assignee selection requires a target-assignee protected parameter", ref)
	}
	if CanonicalThinkingSelection(edge.ThinkingSelection) == ThinkingSelectionPreviousNode && !seenPurposes[ParameterPurposeTargetThinking] {
		s.addHard(CodeMissingProtectedParameter, "edge thinking selection requires a target-thinking protected parameter", ref)
	}
}

func (s *validationState) validateGraph() {
	s.validateKindConstraints()
	s.validateRuntimeSupport()
	if len(s.startNodes) != 1 {
		return
	}
	s.validateStartOutgoingShape()
	reachable := s.reachableFrom(NodeIDOf(s.startNodes[0]))
	for nodeID, node := range s.nodesByID {
		if !reachable[nodeID] {
			s.addSemantic(CodeNodeUnreachableFromStart, fmt.Sprintf("%s not reachable", fmt.Sprintf("Node %s", nodeDisplayName(node))), ValidationError{WorkflowID: WorkflowIDPointer(s.def.ID), NodeID: &nodeID})
		}
		if node.Kind() != NodeKindTerminal && !s.canReachTerminal(nodeID) {
			s.addSemantic(CodeNonTerminalCannotReachTerminal, fmt.Sprintf("%s cannot reach a terminal", fmt.Sprintf("Node %s", nodeDisplayName(node))), ValidationError{WorkflowID: WorkflowIDPointer(s.def.ID), NodeID: &nodeID})
		}
	}
	s.validatePromptPlaceholders()
}

func (s *validationState) validateKindConstraints() {
	for _, node := range s.def.Nodes {
		nodeID := NodeIDOf(node)
		ref := ValidationError{WorkflowID: WorkflowIDPointer(s.def.ID), NodeID: &nodeID}
		incoming := len(s.incomingByNode[nodeID])
		outgoingGroups := s.groupsBySource[nodeID]
		switch node.Kind() {
		case NodeKindStart:
			if strings.TrimSpace(NodeSubagentRole(node)) != "" || incoming > 0 {
				s.addHard(CodeInvalidStartNode, "start node must be non-executable and have no inputs", ref)
			}
		case NodeKindAgent:
			if issue, exists := validateAgentRoleRequirement(s.def.ID, node, toolspec.ToolAskQuestion, s.opts.RoleResolver); exists {
				s.addSemantic(issue.Code, issue.Message, issue)
			}
		case NodeKindJoin:
			if strings.TrimSpace(NodeSubagentRole(node)) != "" {
				s.addHard(CodeJoinIsExecutable, "join node must be non-executable", ref)
			}
			if incoming < 2 {
				s.addHard(CodeInvalidJoinNode, "join node must have at least two incoming branches", ref)
			}
			if len(outgoingGroups) != 1 {
				s.addSemantic(CodeInvalidJoinOutgoingShape, "join node must have exactly one outgoing transition group", ref)
			}
		case NodeKindTerminal:
			if strings.TrimSpace(NodeSubagentRole(node)) != "" {
				s.addHard(CodeTerminalIsExecutable, "terminal node must be non-executable", ref)
			}
			if len(outgoingGroups) > 0 {
				s.addHard(CodeTerminalHasOutgoingEdge, "terminal node cannot have outgoing edges", ref)
			}
		}
	}
}

func validateAgentRoleRequirement(workflowID runtimeids.WorkflowID, node Node, requiredTool toolspec.ID, catalog TargetAgentCatalog) (ValidationError, bool) {
	nodeID := NodeIDOf(node)
	ref := ValidationError{WorkflowID: WorkflowIDPointer(workflowID), NodeID: &nodeID}
	role := strings.TrimSpace(NodeSubagentRole(node))
	if role == "" {
		return ValidationError{
			Code:       CodeAgentRoleRequired,
			Message:    "agent node requires a subagent role",
			WorkflowID: ref.WorkflowID,
			NodeID:     ref.NodeID,
		}, true
	}
	ref.AgentRole = &role
	if catalog == nil {
		ref.Code = CodeAgentRoleMissing
		ref.Message = fmt.Sprintf("Agent node %s references missing subagent role %s", nodeDisplayName(node), role)
		return ref, true
	}
	_, err := PlanTargetAgentSelection(catalog, TargetAgentSelectionRequest{FallbackRole: role})
	if err == nil {
		return ValidationError{}, false
	}
	var selectionErr TargetAgentSelectionError
	if errors.As(err, &selectionErr) {
		switch selectionErr.Code {
		case TargetAgentSelectionErrorUnavailableRole:
			ref.Code = CodeAgentRoleMissing
			ref.Message = fmt.Sprintf("Agent node %s references missing subagent role %s", nodeDisplayName(node), role)
			return ref, true
		case TargetAgentSelectionErrorQuestionsDisabled:
			ref.RequiredTool = &requiredTool
			ref.Code = CodeAgentRoleRequiredToolDisabled
			ref.Message = fmt.Sprintf("Agent node %s uses role %s without required tool %s; enable it in the role's effective configuration", nodeDisplayName(node), role, requiredTool)
			return ref, true
		}
	}
	ref.Code = CodeAgentRoleMissing
	ref.Message = fmt.Sprintf("Agent node %s references unavailable subagent role %s", nodeDisplayName(node), role)
	return ref, true
}

func (s *validationState) validateRuntimeSupport() {
	for _, edge := range s.def.Edges {
		ref := ValidationError{
			WorkflowID:        WorkflowIDPointer(s.def.ID),
			EdgeID:            &edge.ID,
			TransitionGroupID: &edge.TransitionGroupID,
		}
		targetKind := NodeKind("")
		var target Node
		targetExists := false
		if resolvedTarget, exists := s.nodesByID[edge.TargetNodeID]; exists {
			target = resolvedTarget
			targetKind = target.Kind()
			targetExists = true
		}
		var source Node
		sourceExists := false
		if group, groupExists := s.groupsByID[edge.TransitionGroupID]; groupExists {
			source, sourceExists = s.nodesByID[group.SourceNodeID]
		}
		sourceKind := NodeKind("")
		if sourceExists {
			sourceKind = source.Kind()
		}
		contextSource, contextSourceValid := s.validateContextSource(edge, source, sourceExists, target, targetExists, ref)
		s.validateSelectorApplicability(edge, source, sourceExists, target, targetExists, contextSource, ref)
		if edge.ContextMode == ContextModeContinueSession && contextSourceValid {
			selectedSource, selectedSourceExists := s.contextSourceNode(contextSource, source, sourceExists, target, targetExists)
			if selectedSourceExists && targetExists &&
				selectedSource.Kind() == NodeKindAgent && target.Kind() == NodeKindAgent &&
				!sameAgentRoleIdentity(NodeSubagentRole(selectedSource), NodeSubagentRole(target)) {
				s.addSemantic(CodeInvalidContinueSessionRole, "continue_session requires source and target agent nodes to use the same normalized subagent role", ref)
			}
		}
		for _, issue := range UnsupportedRuntimeFeatures(RuntimeSupportEdge{SourceKind: sourceKind, ContextMode: edge.ContextMode, RequiresApproval: edge.RequiresApproval, TargetKind: targetKind, InputBindings: edge.InputBindings}) {
			s.addSemantic(issue.Code, issue.Message, ref)
		}
	}
}

func (s *validationState) validateSelectorApplicability(edge Edge, source Node, sourceExists bool, target Node, targetExists bool, contextSource ContextSource, ref ValidationError) {
	assigneeSelection := CanonicalAssigneeSelection(edge.AssigneeSelection)
	thinkingSelection := CanonicalThinkingSelection(edge.ThinkingSelection)
	sourceKind := NodeKind("")
	if sourceExists {
		sourceKind = source.Kind()
	}
	targetKind := NodeKind("")
	targetRole := ""
	if targetExists {
		targetKind = target.Kind()
		targetRole = NodeSubagentRole(target)
	}
	plan, planErr := PlanTransitionSelection(TransitionParameterContractRequest{
		Edge:                         edge,
		SourceKind:                   sourceKind,
		TargetKind:                   targetKind,
		TargetRole:                   targetRole,
		FanOut:                       len(s.edgesByGroup[edge.TransitionGroupID]) > 1,
		TargetSessionPolicy:          AssigneeSessionPolicyEstablishTarget,
		Catalog:                      s.opts.RoleResolver,
		RequireExecutionDescriptions: s.context == ValidationContextExecution,
	})
	applicability := plan.Applicability
	if assigneeSelection == AssigneeSelectionPreviousNode {
		if applicability.Assignee.Reason == SelectorApplicabilityTopology ||
			applicability.Assignee.Reason == SelectorApplicabilityContextSource {
			s.addSemantic(CodeAssigneeSelectionInapplicable, "edge Assignee selection is not applicable to this serial Agent transition", ref)
		} else if !applicability.Assignee.Available {
			s.addSemantic(CodeAssigneeSelectionUnavailable, "edge Assignee selection is unavailable for the current role configuration", ref)
		}
	}
	if thinkingSelection != ThinkingSelectionPreviousNode {
		return
	}
	if applicability.Thinking.Reason == SelectorApplicabilityTopology {
		s.addSemantic(CodeThinkingSelectionInapplicable, "edge thinking selection is not applicable to this serial Agent transition", ref)
		return
	}
	if !applicability.Thinking.Available {
		s.addSemantic(CodeThinkingSelectionUnavailable, "edge thinking selection is unavailable for the applicable target models", ref)
	}
	if s.context == ValidationContextExecution &&
		thinkingSelection == ThinkingSelectionPreviousNode &&
		sourceExists &&
		targetExists {
		err := planErr
		var selectionErr TargetAgentSelectionError
		if errors.As(err, &selectionErr) &&
			selectionErr.Code == TargetAgentSelectionErrorThinkingDescriptionRequired {
			s.addSemantic(CodeThinkingSelectionUnavailable, "edge thinking selection requires an authored description for the open thinking catalog", ref)
		}
	}
}

type agentRoleIdentity struct {
	value string
}

func sameAgentRoleIdentity(left, right string) bool {
	leftIdentity, leftValid := canonicalAgentRoleIdentity(left)
	rightIdentity, rightValid := canonicalAgentRoleIdentity(right)
	return leftValid && rightValid && leftIdentity == rightIdentity
}

func canonicalAgentRoleIdentity(raw string) (agentRoleIdentity, bool) {
	if strings.EqualFold(strings.TrimSpace(raw), DefaultAgentRole) {
		return agentRoleIdentity{value: DefaultAgentRole}, true
	}
	normalized := config.NormalizeSubagentSelector(raw)
	if normalized == "" {
		return agentRoleIdentity{}, false
	}
	return agentRoleIdentity{value: normalized}, true
}

func (s *validationState) validateContextSource(edge Edge, source Node, sourceExists bool, target Node, targetExists bool, ref ValidationError) (ContextSource, bool) {
	contextSource := CanonicalContextSource(edge.ContextSource)
	if contextSource.Kind != ContextSourceImmediateSource && sourceExists && source.Kind() == NodeKindStart {
		s.addSemantic(CodeInvalidContextSource, "start transitions require immediate context source", ref)
		return contextSource, false
	}
	switch contextSource.Kind {
	case ContextSourceImmediateSource:
		if edge.ContextMode != ContextModeNewSession && sourceExists && source.Kind() != NodeKindAgent {
			s.addSemantic(CodeInvalidContextSource, "immediate context source for continuation must be an agent node", ref)
			return contextSource, false
		}
		return contextSource, true
	case ContextSourceSelectedNode:
		if edge.ContextMode == ContextModeNewSession {
			s.addSemantic(CodeInvalidContextSource, "selected context source requires a continuation context mode", ref)
			return contextSource, false
		}
		nodeKey := strings.TrimSpace(string(contextSource.NodeKey))
		if nodeKey == "" || !workflowkey.Valid(nodeKey) {
			s.addSemantic(CodeInvalidContextSource, "selected context source node key is invalid", ref)
			return contextSource, false
		}
		selectedID, exists := s.nodeKeys[contextSource.NodeKey]
		if !exists {
			s.addSemantic(CodeInvalidContextSource, "selected context source node does not exist", ref)
			return contextSource, false
		}
		selected := s.nodesByID[selectedID]
		if selected.Kind() != NodeKindAgent {
			s.addSemantic(CodeInvalidContextSource, "selected context source must be an agent node", ref)
			return contextSource, false
		}
		if targetExists && NodeIDOf(selected) == NodeIDOf(target) {
			s.addSemantic(CodeInvalidContextSource, "selected context source cannot be the edge target node", ref)
			return contextSource, false
		}
		if !sourceExists || len(s.startNodes) != 1 {
			return contextSource, true
		}
		if NodeIDOf(selected) != NodeIDOf(source) && !s.nodeDominates(NodeIDOf(selected), NodeIDOf(source)) {
			s.addSemantic(CodeInvalidContextSource, "selected context source must be guaranteed before the edge source", ref)
			return contextSource, false
		}
		return contextSource, true
	case ContextSourcePreviousTarget:
		if edge.ContextMode == ContextModeNewSession {
			s.addSemantic(CodeInvalidContextSource, "previous target context source requires a continuation context mode", ref)
			return contextSource, false
		}
		if !targetExists {
			return contextSource, true
		}
		if target.Kind() != NodeKindAgent {
			s.addSemantic(CodeInvalidContextSource, "previous target context source requires an agent target node", ref)
			return contextSource, false
		}
		if !sourceExists || len(s.startNodes) != 1 {
			return contextSource, true
		}
		if NodeIDOf(target) != NodeIDOf(source) && !s.nodeDominates(NodeIDOf(target), NodeIDOf(source)) {
			s.addSemantic(CodeInvalidContextSource, "previous target context source requires the target node to be guaranteed before the edge source", ref)
			return contextSource, false
		}
		return contextSource, true
	case ContextSourcePreviousTargetOrNew:
		if edge.ContextMode == ContextModeNewSession {
			s.addSemantic(CodeInvalidContextSource, "previous target or new context source requires a continuation context mode", ref)
			return contextSource, false
		}
		if !targetExists {
			return contextSource, true
		}
		if target.Kind() != NodeKindAgent {
			s.addSemantic(CodeInvalidContextSource, "previous target or new context source requires an agent target node", ref)
			return contextSource, false
		}
		return contextSource, true
	default:
		s.addSemantic(CodeInvalidContextSource, "context source kind is invalid", ref)
		return contextSource, false
	}
}

func (s *validationState) contextSourceNode(contextSource ContextSource, immediate Node, immediateExists bool, target Node, targetExists bool) (Node, bool) {
	switch contextSource.Kind {
	case ContextSourceImmediateSource:
		return immediate, immediateExists
	case ContextSourceSelectedNode:
		nodeID, exists := s.nodeKeys[contextSource.NodeKey]
		if !exists {
			return nil, false
		}
		node, exists := s.nodesByID[nodeID]
		return node, exists
	case ContextSourcePreviousTarget, ContextSourcePreviousTargetOrNew:
		return target, targetExists
	default:
		return nil, false
	}
}

func (s *validationState) validateStartOutgoingShape() {
	start := s.startNodes[0]
	startID := NodeIDOf(start)
	groups := s.groupsBySource[startID]
	if s.context == ValidationContextDraft {
		return
	}
	if len(groups) != 1 {
		s.addSemantic(CodeInvalidStartOutgoingShape, "task start requires exactly one outgoing transition group", ValidationError{WorkflowID: WorkflowIDPointer(s.def.ID), NodeID: &startID})
		return
	}
	edges := s.edgesByGroup[groups[0].ID]
	if len(edges) != 1 {
		s.addSemantic(CodeInvalidStartOutgoingShape, "task start transition group requires exactly one edge", ValidationError{
			WorkflowID:        WorkflowIDPointer(s.def.ID),
			NodeID:            &startID,
			TransitionGroupID: &groups[0].ID,
		})
		return
	}
	target, exists := s.nodesByID[edges[0].TargetNodeID]
	if !exists || !IsExecutableNode(target) {
		s.addSemantic(CodeInvalidStartOutgoingShape, "task start edge must target an executable node", ValidationError{
			WorkflowID: WorkflowIDPointer(s.def.ID),
			NodeID:     &startID,
			EdgeID:     &edges[0].ID,
		})
	}
}

func (s *validationState) validatePromptPlaceholders() {
	derived := DeriveWiringWithCatalog(s.def, s.opts.RoleResolver)
	for _, edge := range s.def.Edges {
		prompt := strings.TrimSpace(edge.PromptTemplate)
		if prompt == "" {
			continue
		}
		ref := ValidationError{
			WorkflowID:        WorkflowIDPointer(s.def.ID),
			EdgeID:            &edge.ID,
			TransitionGroupID: &edge.TransitionGroupID,
			NodeID:            &edge.TargetNodeID,
		}
		refs, err := ExtractPromptTemplateReferences(prompt)
		if err != nil {
			s.addHard(CodeInvalidTemplatePlaceholder, "prompt template syntax is invalid", ref)
			continue
		}
		for _, invalid := range refs.Invalid {
			invalidRef := ref
			invalidRef.Placeholder = invalid.Placeholder
			s.addHard(CodeInvalidTemplatePlaceholder, invalid.Message, invalidRef)
		}
		target, targetExists := s.nodesByID[edge.TargetNodeID]
		currentParams := edgeParameterNameSet(edge)
		source, sourceExists := s.edgeSource(edge)
		if sourceExists && source.Kind() == NodeKindJoin {
			currentParams = outputFieldNameSet(derived.JoinOutputFieldsForNode(NodeIDOf(source)))
		} else if sourceExists && targetExists {
			currentParams = s.promptParameterNameSet(edge, source, target)
		}
		for _, param := range refs.Params {
			name := strings.TrimSpace(param.Name)
			paramRef := ref
			paramRef.InputName = name
			paramRef.Placeholder = param.Placeholder
			if name == RuntimePromptParameterCommentary {
				continue
			}
			if name == "" || !workflowkey.Valid(name) || !currentParams[name] {
				s.addHard(CodeInvalidTemplatePlaceholder, "prompt template references an unknown transition parameter", paramRef)
			}
		}
		for _, priorParam := range refs.PriorParams {
			s.validatePriorParameterReference(edge, priorParam, ref, derived)
		}
	}
}

func (s *validationState) promptParameterNameSet(edge Edge, source Node, target Node) map[string]bool {
	names := edgeParameterNameSet(edge)
	consumption := PlanTransitionProtectedParameterConsumption(TransitionParameterContractRequest{
		Edge:                edge,
		SourceKind:          source.Kind(),
		TargetKind:          target.Kind(),
		TargetRole:          NodeSubagentRole(target),
		FanOut:              len(s.edgesByGroup[edge.TransitionGroupID]) > 1,
		Catalog:             s.opts.RoleResolver,
		TargetSessionPolicy: AssigneeSessionPolicyEstablishTarget,
	})
	contextSourceKind := CanonicalContextSource(edge.ContextSource).Kind
	retainedSessionPossible := edge.ContextMode == ContextModeContinueSession &&
		(contextSourceKind == ContextSourcePreviousTarget || contextSourceKind == ContextSourcePreviousTargetOrNew)
	for _, parameter := range edge.Parameters {
		var policy ProtectedParameterConsumptionPolicy
		switch CanonicalParameterPurpose(parameter.Purpose) {
		case ParameterPurposeTargetAssignee:
			policy = consumption.Assignee
		case ParameterPurposeTargetThinking:
			policy = consumption.Thinking
		default:
			continue
		}
		if retainedSessionPossible || policy != ProtectedParameterConsumptionRequiredValidate {
			delete(names, strings.TrimSpace(parameter.Key))
		}
	}
	return names
}

func edgeParameterNameSet(edge Edge) map[string]bool {
	out := map[string]bool{}
	for _, parameter := range edge.Parameters {
		key := strings.TrimSpace(parameter.Key)
		if key != "" {
			out[key] = true
		}
	}
	return out
}

func outputFieldNameSet(fields []OutputField) map[string]bool {
	out := map[string]bool{}
	for _, field := range fields {
		name := strings.TrimSpace(field.Name)
		if name != "" {
			out[name] = true
		}
	}
	return out
}

func (s *validationState) validatePriorParameterReference(edge Edge, param PromptPriorParameterReference, baseRef ValidationError, derived DerivedWiring) {
	transitionKey := strings.TrimSpace(string(param.TransitionKey))
	parameterKey := strings.TrimSpace(param.ParameterKey)
	ref := baseRef
	ref.FieldName = parameterKey
	ref.Placeholder = param.Placeholder
	if transitionKey == "" || !workflowkey.Valid(transitionKey) || parameterKey == "" || !workflowkey.Valid(parameterKey) {
		s.addHard(CodeInvalidTemplatePlaceholder, fmt.Sprintf(
			"prompt for %s has an invalid previous-parameter reference; use .Params.<transition_key>.<parameter_key> with valid keys.",
			s.priorParameterConsumerName(edge)), ref)
		return
	}
	source, sourceExists := s.edgeSource(edge)
	if !sourceExists || len(s.startNodes) != 1 {
		return
	}
	consumer := s.priorParameterConsumerName(edge)
	resolution := resolvePriorParameterTransitionGroups(
		s.def.TransitionGroups,
		ModelKey(transitionKey),
		NodeIDOf(source),
		NodeIDOf(s.startNodes[0]),
		s.outgoingByNode,
	)
	switch {
	case resolution.matched == 0:
		s.addHard(CodeInvalidTemplatePlaceholder, fmt.Sprintf(
			"prompt for %s references parameter %q from transition %q, but no %q transition exists in this workflow. Check the transition key for a typo, or define the transition that produces it.",
			consumer, parameterKey, transitionKey, transitionKey), ref)
	case len(resolution.guaranteed) == 0:
		s.addHard(CodeInvalidTemplatePlaceholder, fmt.Sprintf(
			"prompt for %s references parameter %q from transition %q, but %q is not guaranteed to run before %s: another branch can reach %s without it. Reference a parameter that every incoming branch provides, or remove the bypassing branch.",
			consumer, parameterKey, transitionKey, transitionKey, consumer, consumer), ref)
	case len(resolution.guaranteed) > 1:
		s.addHard(CodeInvalidTemplatePlaceholder, fmt.Sprintf(
			"prompt for %s references parameter %q from transition %q, but more than one %q transition can run before %s. Give the producing transitions distinct keys and reference one.",
			consumer, parameterKey, transitionKey, transitionKey, consumer), ref)
	case parameterKey == RuntimePromptParameterCommentary:
		return
	case !s.transitionGroupParameterSet(resolution.guaranteed[0].ID, derived)[parameterKey]:
		s.addHard(CodeInvalidTemplatePlaceholder, fmt.Sprintf(
			"prompt for %s references parameter %q from transition %q, but transition %q does not declare a %q parameter.",
			consumer, parameterKey, transitionKey, transitionKey, parameterKey), ref)
	}
}

// priorParameterConsumerName names the node whose prompt carries a previous-parameter
// reference for use in operator-facing validation messages. The prompt is rendered for
// the edge's target node, so that node is the consumer.
func (s *validationState) priorParameterConsumerName(edge Edge) string {
	if target, exists := s.nodesByID[edge.TargetNodeID]; exists {
		return nodeDisplayName(target)
	}
	return "the target node"
}

func (s *validationState) transitionGroupParameterSet(groupID TransitionGroupID, derived DerivedWiring) map[string]bool {
	out := map[string]bool{}
	// Transitions leaving a join node declare no per-edge parameters; their
	// effective parameters are the join's aggregated output fields, so resolve
	// those when the group's source is a join.
	if group, exists := s.groupsByID[groupID]; exists {
		if source, sourceExists := s.nodesByID[group.SourceNodeID]; sourceExists && source.Kind() == NodeKindJoin {
			for _, field := range derived.JoinOutputFieldsForNode(NodeIDOf(source)) {
				name := strings.TrimSpace(field.Name)
				if name != "" {
					out[name] = true
				}
			}
		}
	}
	for _, edge := range s.edgesByGroup[groupID] {
		group := s.groupsByID[groupID]
		source, sourceExists := s.nodesByID[group.SourceNodeID]
		target, targetExists := s.nodesByID[edge.TargetNodeID]
		if sourceExists && targetExists {
			for key := range s.promptParameterNameSet(edge, source, target) {
				out[key] = true
			}
			continue
		}
		for _, parameter := range edge.Parameters {
			if key := strings.TrimSpace(parameter.Key); key != "" {
				out[key] = true
			}
		}
	}
	return out
}

func (s *validationState) nodeDominates(candidate NodeID, target NodeID) bool {
	if len(s.startNodes) != 1 {
		return false
	}
	return nodeDominatesFromStart(NodeIDOf(s.startNodes[0]), candidate, target, s.outgoingByNode)
}

func (s *validationState) reachableFrom(start NodeID) map[NodeID]bool {
	visited := map[NodeID]bool{}
	stack := []NodeID{start}
	for len(stack) > 0 {
		nodeID := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[nodeID] {
			continue
		}
		visited[nodeID] = true
		for _, edge := range s.outgoingByNode[nodeID] {
			if !visited[edge.TargetNodeID] {
				stack = append(stack, edge.TargetNodeID)
			}
		}
	}
	return visited
}

func (s *validationState) canReachTerminal(start NodeID) bool {
	visited := map[NodeID]bool{}
	stack := []NodeID{start}
	for len(stack) > 0 {
		nodeID := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[nodeID] {
			continue
		}
		visited[nodeID] = true
		node, exists := s.nodesByID[nodeID]
		if !exists {
			continue
		}
		if node.Kind() == NodeKindTerminal {
			return true
		}
		for _, edge := range s.outgoingByNode[nodeID] {
			if !visited[edge.TargetNodeID] {
				stack = append(stack, edge.TargetNodeID)
			}
		}
	}
	return false
}

func (s *validationState) validateFanouts() {
	for _, group := range s.def.TransitionGroups {
		edges := s.edgesByGroup[group.ID]
		if len(edges) <= 1 {
			continue
		}
		if !s.fanoutHasValidJoin(group, edges) {
			s.addSemantic(CodeInvalidFanoutJoinTopology, "fan-out transition group must have one unambiguous nearest common join without terminal, nested fan-out, or cycle before it", ValidationError{
				WorkflowID:        WorkflowIDPointer(s.def.ID),
				TransitionGroupID: &group.ID,
				NodeID:            &group.SourceNodeID,
			})
		}
	}
}

func (s *validationState) validateDerivedWiring() {
	derived := DeriveWiring(s.def)
	for _, diagnostic := range derived.Diagnostics {
		if diagnostic.WorkflowID == nil {
			diagnostic.WorkflowID = WorkflowIDPointer(s.def.ID)
		}
		s.errors = append(s.errors, diagnostic)
	}
}

func (s *validationState) fanoutHasValidJoin(group TransitionGroup, edges []Edge) bool {
	branchJoinDistances := make([]map[NodeID]int, 0, len(edges))
	for _, edge := range edges {
		distances, ok := s.branchJoinDistances(edge.TargetNodeID)
		if !ok || len(distances) == 0 {
			return false
		}
		branchJoinDistances = append(branchJoinDistances, distances)
	}
	nearestJoinID, found := fanoutNearestCommonJoin(branchJoinDistances)
	return found && nearestJoinID != group.SourceNodeID
}

func (s *validationState) branchJoinDistances(start NodeID) (map[NodeID]int, bool) {
	return fanoutBranchJoinDistances(s.nodesByID, s.groupsBySource, s.edgesByGroup, s.outgoingByNode, start)
}

func (s *validationState) addHard(code ValidationErrorCode, message string, ref ValidationError) {
	ref.Code = code
	ref.Message = message
	ref.BlocksContext = true
	s.errors = append(s.errors, ref)
}

func (s *validationState) addSemantic(code ValidationErrorCode, message string, ref ValidationError) {
	ref.Code = code
	ref.Message = message
	ref.BlocksContext = s.context != ValidationContextDraft
	s.errors = append(s.errors, ref)
}

func edgeMessageSubject(edge Edge) string {
	if key := strings.TrimSpace(string(edge.Key)); key != "" {
		return fmt.Sprintf("Transition branch %s", key)
	}
	if id := strings.TrimSpace(string(edge.ID)); id != "" {
		return fmt.Sprintf("Transition branch %s", id)
	}
	return "Transition branch unknown"
}

func nodeDisplayName(node Node) string {
	if name := strings.TrimSpace(NodeDisplayName(node)); name != "" {
		return name
	}
	if key := strings.TrimSpace(string(NodeKey(node))); key != "" {
		return key
	}
	if id := strings.TrimSpace(string(NodeIDOf(node))); id != "" {
		return id
	}
	return "unknown"
}

func validDisplayName(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && len(trimmed) <= MaxDisplayNameChars
}

func validContextMode(value ContextMode) bool {
	switch value {
	case ContextModeNewSession, ContextModeContinueSession, ContextModeCompactAndContinueSession:
		return true
	default:
		return false
	}
}

func (c ValidationErrorCode) Error() string {
	return string(c)
}

func (e ValidationError) String() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
