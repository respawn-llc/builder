package workflow

import (
	"strings"

	"core/shared/runtimeids"
)

// SessionReuseClassification is the ephemeral decision used by Workflow
// Execution at a completed Agent boundary.
type SessionReuseClassification string

const (
	SessionReuseNone                   SessionReuseClassification = "none"
	SessionReuseThresholdPossibleReuse SessionReuseClassification = "threshold_possible_reuse"
	SessionReuseGuaranteedCACReuse     SessionReuseClassification = "guaranteed_cac_reuse"
)

// SessionReuseAssociation is bounded retained provenance supplied by the
// Workflow store. Associations are ordered from oldest to newest.
type SessionReuseAssociation struct {
	SessionID   runtimeids.SessionID
	CurrentNode CurrentNodeReference
}

// SessionReuseAnalysisInput contains the execution-valid graph slice needed
// to classify reuse without consulting persistence or runtime state.
type SessionReuseAnalysisInput struct {
	Workflow             Definition
	AcceptedBranches     []Edge
	CompletedCurrentNode CurrentNode
	RetainedAssociations []SessionReuseAssociation
}

// SessionReuseAssociationReferences returns the bounded Current Node
// references whose retained associations can affect the analysis. It follows
// only Context Source forms that consult retained provenance.
func SessionReuseAssociationReferences(input SessionReuseAnalysisInput) []CurrentNodeReference {
	taskID := input.CompletedCurrentNode.Reference.TaskID
	nodeIDsByKey := make(map[ModelKey]NodeID, len(input.Workflow.Nodes))
	for _, node := range input.Workflow.Nodes {
		nodeIDsByKey[NodeKey(node)] = NodeIDOf(node)
	}
	nodeIDs := make(map[NodeID]struct{})
	for _, edge := range input.Workflow.Edges {
		source := CanonicalContextSource(edge.ContextSource)
		switch source.Kind {
		case ContextSourceSelectedNode:
			if nodeID, ok := nodeIDsByKey[source.NodeKey]; ok {
				nodeIDs[nodeID] = struct{}{}
			}
		case ContextSourcePreviousTarget, ContextSourcePreviousTargetOrNew:
			nodeIDs[edge.TargetNodeID] = struct{}{}
		}
	}
	if input.CompletedCurrentNode.SessionID != nil {
		nodeIDs[input.CompletedCurrentNode.Reference.NodeID] = struct{}{}
	}
	branches := make(map[TransitionBranchKey]struct{})
	if branch, scoped := input.CompletedCurrentNode.Reference.TransitionBranchKey(); scoped {
		branches[branch] = struct{}{}
	}
	for _, edge := range input.AcceptedBranches {
		if branch := TransitionBranchKey(strings.TrimSpace(string(edge.Key))); branch != "" {
			branches[branch] = struct{}{}
		}
	}

	references := make([]CurrentNodeReference, 0, len(nodeIDs)*(len(branches)+1))
	seen := make(map[CurrentNodeReferenceKey]struct{}, cap(references))
	appendReference := func(nodeID NodeID, branch *TransitionBranchKey) {
		reference, err := NewCurrentNodeReference(taskID, nodeID, branch)
		if err != nil {
			return
		}
		key, err := reference.Key()
		if err != nil {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		references = append(references, reference)
	}
	for nodeID := range nodeIDs {
		appendReference(nodeID, nil)
		for branch := range branches {
			value := branch
			appendReference(nodeID, &value)
		}
	}
	return references
}

// ClassifyWorkflowSessionReuse classifies whether a completed Session can be
// selected again on a reachable future Workflow path.
func ClassifyWorkflowSessionReuse(input SessionReuseAnalysisInput) SessionReuseClassification {
	if input.CompletedCurrentNode.SessionID == nil {
		return SessionReuseNone
	}
	completedSessionID := *input.CompletedCurrentNode.SessionID
	if completedSessionID.IsZero() || len(input.AcceptedBranches) == 0 {
		return SessionReuseNone
	}

	analyzer := sessionReuseAnalyzer{
		input:              input,
		completedSessionID: completedSessionID,
		nodesByID:          make(map[NodeID]Node, len(input.Workflow.Nodes)),
		groupsBySource:     make(map[NodeID][]TransitionGroup, len(input.Workflow.TransitionGroups)),
		edgesByGroup:       make(map[TransitionGroupID][]Edge, len(input.Workflow.Edges)),
		nodeIDsByKey:       make(map[ModelKey]NodeID, len(input.Workflow.Nodes)),
	}
	for _, node := range input.Workflow.Nodes {
		analyzer.nodesByID[NodeIDOf(node)] = node
		analyzer.nodeIDsByKey[NodeKey(node)] = NodeIDOf(node)
	}
	for _, group := range input.Workflow.TransitionGroups {
		analyzer.groupsBySource[group.SourceNodeID] = append(analyzer.groupsBySource[group.SourceNodeID], group)
	}
	for _, edge := range input.Workflow.Edges {
		analyzer.edgesByGroup[edge.TransitionGroupID] = append(analyzer.edgesByGroup[edge.TransitionGroupID], edge)
	}

	initial := reusePathState{
		nodeID:           input.CompletedCurrentNode.Reference.NodeID,
		branch:           reuseBranchForReference(input.CompletedCurrentNode.Reference),
		currentLineage:   reuseLineageCompleted,
		completedDormant: acceptedBranchesFanout(input.AcceptedBranches),
	}
	initialTransitions := make([]reuseTransition, 0, len(input.AcceptedBranches))
	for _, edge := range input.AcceptedBranches {
		transition, ok := analyzer.applyEdge(initial, edge)
		if ok {
			initialTransitions = append(initialTransitions, transition)
		}
	}
	if len(initialTransitions) == 0 {
		return SessionReuseNone
	}

	graph := analyzer.buildGraph(initialTransitions)
	if analyzer.guaranteedAccepted(initialTransitions, graph) {
		return SessionReuseGuaranteedCACReuse
	}
	if graph.possibleReuse {
		return SessionReuseThresholdPossibleReuse
	}
	return SessionReuseNone
}

type reuseLineage uint8

const (
	reuseLineageAbsent reuseLineage = iota
	reuseLineageCompleted
	reuseLineageOther
	reuseLineageUnknown
)

type reuseBranch struct {
	key    TransitionBranchKey
	scoped bool
}

type reusePathState struct {
	nodeID           NodeID
	branch           reuseBranch
	currentLineage   reuseLineage
	completedDormant bool
	dormancyBlocked  bool
	overwritten      map[NodeID]struct{}
}

type reuseTransition struct {
	target        reusePathState
	possibleReuse bool
	guaranteedCAC bool
}

type reuseGroup struct {
	transitions []reuseTransition
}

type sessionReuseAnalyzer struct {
	input              SessionReuseAnalysisInput
	completedSessionID runtimeids.SessionID
	nodesByID          map[NodeID]Node
	nodeIDsByKey       map[ModelKey]NodeID
	groupsBySource     map[NodeID][]TransitionGroup
	edgesByGroup       map[TransitionGroupID][]Edge
}

func (a sessionReuseAnalyzer) applyEdge(state reusePathState, edge Edge) (reuseTransition, bool) {
	target, exists := a.nodesByID[edge.TargetNodeID]
	if !exists {
		return reuseTransition{}, false
	}
	groupEdges := a.edgesByGroup[edge.TransitionGroupID]
	targetLineage, selected := a.targetLineage(state, edge, target)
	if !selected {
		return reuseTransition{}, false
	}

	completedDormant := state.completedDormant
	dormancyBlocked := state.dormancyBlocked
	if !state.completedDormant &&
		state.currentLineage == reuseLineageCompleted &&
		targetLineage == reuseLineageCompleted &&
		!edge.RequiresApproval {
		dormancyBlocked = true
	}
	if !dormancyBlocked && (edge.RequiresApproval || targetLineage != reuseLineageCompleted) {
		completedDormant = true
	}
	targetState := reusePathState{
		nodeID:           edge.TargetNodeID,
		branch:           a.nextBranch(state.branch, edge, target, len(groupEdges)),
		currentLineage:   targetLineage,
		completedDormant: completedDormant,
		dormancyBlocked:  dormancyBlocked,
		overwritten:      cloneOverwritten(state.overwritten),
	}
	if target.Kind() == NodeKindAgent &&
		targetLineage != reuseLineageAbsent &&
		targetLineage != reuseLineageCompleted {
		if targetState.overwritten == nil {
			targetState.overwritten = make(map[NodeID]struct{})
		}
		targetState.overwritten[edge.TargetNodeID] = struct{}{}
	}
	selectedCompleted := targetLineage == reuseLineageCompleted || targetLineage == reuseLineageUnknown
	possibleReuse := selectedCompleted && completedDormant && !dormancyBlocked
	guaranteedCAC := edge.ContextMode == ContextModeCompactAndContinueSession &&
		targetLineage == reuseLineageCompleted &&
		state.completedDormant &&
		!state.dormancyBlocked
	return reuseTransition{
		target:        targetState,
		possibleReuse: possibleReuse,
		guaranteedCAC: guaranteedCAC,
	}, true
}

func (a sessionReuseAnalyzer) targetLineage(state reusePathState, edge Edge, target Node) (reuseLineage, bool) {
	if target.Kind() != NodeKindAgent {
		return reuseLineageAbsent, true
	}
	switch edge.ContextMode {
	case ContextModeNewSession:
		return reuseLineageOther, true
	case ContextModeContinueSession, ContextModeCompactAndContinueSession:
		return a.resolveContextSource(state, edge.ContextSource, target)
	default:
		return reuseLineageUnknown, true
	}
}

func (a sessionReuseAnalyzer) resolveContextSource(state reusePathState, source ContextSource, target Node) (reuseLineage, bool) {
	canonical := CanonicalContextSource(source)
	switch canonical.Kind {
	case ContextSourceImmediateSource:
		return state.currentLineage, true
	case ContextSourceSelectedNode:
		nodeID, exists := a.nodeIDsByKey[canonical.NodeKey]
		if !exists {
			return reuseLineageUnknown, true
		}
		return a.associatedLineage(state.branch, state.overwritten, nodeID, true)
	case ContextSourcePreviousTarget:
		return a.associatedLineage(state.branch, state.overwritten, NodeIDOf(target), false)
	case ContextSourcePreviousTargetOrNew:
		lineage, found := a.associatedLineage(state.branch, state.overwritten, NodeIDOf(target), true)
		if !found {
			return reuseLineageOther, true
		}
		return lineage, true
	default:
		return reuseLineageUnknown, true
	}
}

func (a sessionReuseAnalyzer) associatedLineage(branch reuseBranch, overwritten map[NodeID]struct{}, nodeID NodeID, optional bool) (reuseLineage, bool) {
	if _, exists := overwritten[nodeID]; exists {
		return reuseLineageOther, true
	}
	for index := len(a.input.RetainedAssociations) - 1; index >= 0; index-- {
		association := a.input.RetainedAssociations[index]
		if association.CurrentNode.TaskID != a.input.CompletedCurrentNode.Reference.TaskID ||
			association.CurrentNode.NodeID != nodeID ||
			!sameReuseBranch(branch, association.CurrentNode) {
			continue
		}
		if association.SessionID == a.completedSessionID {
			return reuseLineageCompleted, true
		}
		return reuseLineageOther, true
	}
	if optional {
		return reuseLineageOther, false
	}
	return reuseLineageAbsent, false
}

func sameReuseBranch(branch reuseBranch, reference CurrentNodeReference) bool {
	associationBranch, associationScoped := reference.TransitionBranchKey()
	if branch.scoped != associationScoped {
		return false
	}
	return !branch.scoped || branch.key == associationBranch
}

func reuseBranchForReference(reference CurrentNodeReference) reuseBranch {
	key, scoped := reference.TransitionBranchKey()
	return reuseBranch{key: key, scoped: scoped}
}

func acceptedBranchesFanout(edges []Edge) bool {
	if len(edges) < 2 {
		return false
	}
	groupID := edges[0].TransitionGroupID
	for _, edge := range edges[1:] {
		if edge.TransitionGroupID != groupID {
			return false
		}
	}
	return true
}

func (a sessionReuseAnalyzer) nextBranch(current reuseBranch, edge Edge, target Node, groupEdgeCount int) reuseBranch {
	if target.Kind() == NodeKindJoin {
		return reuseBranch{}
	}
	if groupEdgeCount > 1 {
		return reuseBranch{key: TransitionBranchKey(edge.Key), scoped: true}
	}
	return current
}

type reuseGraph struct {
	states        []reusePathState
	groups        [][]reuseGroup
	possibleReuse bool
}

func (a sessionReuseAnalyzer) buildGraph(initial []reuseTransition) reuseGraph {
	graph := reuseGraph{}
	addState := func(state reusePathState) int {
		if id, exists := graphStateID(state, graph.states); exists {
			return id
		}
		id := len(graph.states)
		graph.states = append(graph.states, state)
		graph.groups = append(graph.groups, nil)
		return id
	}
	queue := make([]int, 0, len(initial))
	for _, transition := range initial {
		if transition.possibleReuse {
			graph.possibleReuse = true
		}
		queue = append(queue, addState(transition.target))
	}
	queued := make(map[int]struct{}, len(queue))
	for _, stateID := range queue {
		queued[stateID] = struct{}{}
	}
	for len(queue) > 0 {
		stateID := queue[0]
		queue = queue[1:]
		groups := a.stateGroups(graph.states[stateID])
		if groups == nil {
			groups = []reuseGroup{}
		}
		graph.groups[stateID] = groups
		for _, group := range groups {
			for _, transition := range group.transitions {
				if transition.possibleReuse {
					graph.possibleReuse = true
				}
				targetID := addState(transition.target)
				if _, exists := queued[targetID]; !exists {
					queued[targetID] = struct{}{}
					queue = append(queue, targetID)
				}
			}
		}
	}
	return graph
}

func (a sessionReuseAnalyzer) stateGroups(state reusePathState) []reuseGroup {
	groups := a.groupsBySource[state.nodeID]
	if len(groups) == 0 {
		return nil
	}
	result := make([]reuseGroup, 0, len(groups))
	for _, group := range groups {
		edges := a.edgesByGroup[group.ID]
		transitions := make([]reuseTransition, 0, len(edges))
		for _, edge := range edges {
			transition, ok := a.applyEdge(state, edge)
			if ok {
				transitions = append(transitions, transition)
			}
		}
		result = append(result, reuseGroup{transitions: transitions})
	}
	return result
}

func (a sessionReuseAnalyzer) guaranteedAccepted(initial []reuseTransition, graph reuseGraph) bool {
	if len(initial) > 1 {
		for _, transition := range initial {
			if transition.guaranteedCAC || a.guaranteedState(transition.target, graph) {
				return true
			}
		}
		return false
	}
	transition := initial[0]
	return transition.guaranteedCAC || a.guaranteedState(transition.target, graph)
}

func (a sessionReuseAnalyzer) guaranteedState(state reusePathState, graph reuseGraph) bool {
	stateID, exists := graphStateID(state, graph.states)
	if !exists {
		return false
	}
	canReach := make([]bool, len(graph.states))
	for id := range graph.states {
		canReach[id] = a.canReachTerminalOrCAC(id, graph)
	}
	guaranteed := append([]bool(nil), canReach...)
	for changed := true; changed; {
		changed = false
		for id, groups := range graph.groups {
			next := false
			if len(groups) > 0 {
				next = true
				for _, group := range groups {
					groupGuaranteed := false
					for _, transition := range group.transitions {
						if transition.guaranteedCAC {
							groupGuaranteed = true
							break
						}
						targetID, targetExists := graphStateID(transition.target, graph.states)
						if targetExists && guaranteed[targetID] {
							groupGuaranteed = true
							break
						}
					}
					if !groupGuaranteed {
						next = false
						break
					}
				}
			}
			if !canReach[id] {
				next = false
			}
			if next != guaranteed[id] {
				guaranteed[id] = next
				changed = true
			}
		}
	}
	return guaranteed[stateID]
}

func (a sessionReuseAnalyzer) canReachTerminalOrCAC(stateID int, graph reuseGraph) bool {
	visited := make(map[int]struct{}, len(graph.states))
	queue := []int{stateID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if _, exists := visited[id]; exists {
			continue
		}
		visited[id] = struct{}{}
		if len(graph.groups[id]) == 0 {
			return true
		}
		for _, group := range graph.groups[id] {
			for _, transition := range group.transitions {
				if transition.guaranteedCAC {
					return true
				}
				targetID, exists := graphStateID(transition.target, graph.states)
				if exists {
					queue = append(queue, targetID)
				}
			}
		}
	}
	return false
}

func graphStateID(state reusePathState, states []reusePathState) (int, bool) {
	for id, candidate := range states {
		if sameReusePathState(candidate, state) {
			return id, true
		}
	}
	return 0, false
}

func cloneOverwritten(value map[NodeID]struct{}) map[NodeID]struct{} {
	if len(value) == 0 {
		return nil
	}
	cloned := make(map[NodeID]struct{}, len(value))
	for nodeID := range value {
		cloned[nodeID] = struct{}{}
	}
	return cloned
}

func sameReusePathState(left, right reusePathState) bool {
	if left.nodeID != right.nodeID ||
		left.branch != right.branch ||
		left.currentLineage != right.currentLineage ||
		left.completedDormant != right.completedDormant ||
		left.dormancyBlocked != right.dormancyBlocked ||
		len(left.overwritten) != len(right.overwritten) {
		return false
	}
	for nodeID := range left.overwritten {
		if _, exists := right.overwritten[nodeID]; !exists {
			return false
		}
	}
	return true
}
