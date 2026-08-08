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
	topology := newFanoutTopology(input.Workflow)
	nodeIDsByKey := make(map[ModelKey]NodeID, len(input.Workflow.Nodes))
	for _, node := range input.Workflow.Nodes {
		nodeIDsByKey[NodeKey(node)] = NodeIDOf(node)
	}

	references := make([]CurrentNodeReference, 0, len(input.Workflow.Edges)+1)
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

	if input.CompletedCurrentNode.SessionID != nil {
		appendReference(
			input.CompletedCurrentNode.Reference.NodeID,
			transitionBranchPointer(input.CompletedCurrentNode.Reference),
		)
	}

	type referenceState struct {
		nodeID NodeID
		branch reuseBranch
	}
	initial := referenceState{
		nodeID: input.CompletedCurrentNode.Reference.NodeID,
		branch: reuseBranchForReference(input.CompletedCurrentNode.Reference),
	}
	visited := make(map[referenceState]struct{})
	queue := make([]referenceState, 0, len(input.AcceptedBranches))

	appendContextReference := func(sourceState referenceState, targetBranch reuseBranch, edge Edge, target Node) {
		if target == nil {
			return
		}
		switch edge.ContextMode {
		case ContextModeContinueSession, ContextModeCompactAndContinueSession:
		default:
			return
		}
		source := CanonicalContextSource(edge.ContextSource)
		lookupBranch, usesAssociation := reuseContextLookupBranch(sourceState.branch, targetBranch, source)
		if !usesAssociation {
			return
		}
		var branch *TransitionBranchKey
		if lookupBranch.scoped {
			value := lookupBranch.key
			branch = &value
		}
		switch source.Kind {
		case ContextSourceSelectedNode:
			if nodeID, ok := nodeIDsByKey[source.NodeKey]; ok {
				appendReference(nodeID, branch)
			}
		case ContextSourcePreviousTarget, ContextSourcePreviousTargetOrNew:
			appendReference(NodeIDOf(target), branch)
		}
	}

	enqueue := func(state referenceState, edge Edge) {
		target, exists := topology.nodesByID[edge.TargetNodeID]
		if !exists {
			return
		}
		targetBranch := nextReuseBranch(
			state.branch,
			edge,
			target,
			len(topology.edgesByGroup[edge.TransitionGroupID]),
		)
		appendContextReference(state, targetBranch, edge, target)
		next := referenceState{nodeID: edge.TargetNodeID, branch: targetBranch}
		if _, exists := visited[next]; exists {
			return
		}
		visited[next] = struct{}{}
		queue = append(queue, next)
	}

	for _, edge := range input.AcceptedBranches {
		enqueue(initial, edge)
	}
	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]
		for _, group := range topology.groupsBySource[state.nodeID] {
			for _, edge := range topology.edgesByGroup[group.ID] {
				enqueue(state, edge)
			}
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
	overwritten      map[reuseOverwriteKey]reuseOverwriteStatus
}

type reuseOverwriteKey struct {
	nodeID NodeID
	branch reuseBranch
}

type reuseOverwriteStatus uint8

const (
	reuseOverwriteMaybe reuseOverwriteStatus = iota
	reuseOverwriteDefinite
)

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
	targetBranch := a.nextBranch(state.branch, edge, target, len(groupEdges))
	targetLineage, selected := a.targetLineage(state, edge, target, targetBranch)
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
		branch:           targetBranch,
		currentLineage:   targetLineage,
		completedDormant: completedDormant,
		dormancyBlocked:  dormancyBlocked,
		overwritten:      cloneOverwritten(state.overwritten),
	}
	if target.Kind() == NodeKindAgent &&
		targetLineage != reuseLineageAbsent &&
		targetLineage != reuseLineageCompleted {
		if targetState.overwritten == nil {
			targetState.overwritten = make(map[reuseOverwriteKey]reuseOverwriteStatus)
		}
		targetState.overwritten[reuseOverwriteKey{
			nodeID: edge.TargetNodeID,
			branch: targetBranch,
		}] = reuseOverwriteDefinite
	}
	selectedCompleted := targetLineage == reuseLineageCompleted || targetLineage == reuseLineageUnknown
	possibleReuse := selectedCompleted && completedDormant && !dormancyBlocked
	guaranteedCAC := edge.ContextMode == ContextModeCompactAndContinueSession &&
		targetLineage == reuseLineageCompleted &&
		completedDormant &&
		!dormancyBlocked
	return reuseTransition{
		target:        targetState,
		possibleReuse: possibleReuse,
		guaranteedCAC: guaranteedCAC,
	}, true
}

func (a sessionReuseAnalyzer) targetLineage(state reusePathState, edge Edge, target Node, targetBranch reuseBranch) (reuseLineage, bool) {
	if target.Kind() != NodeKindAgent {
		return reuseLineageAbsent, true
	}
	switch edge.ContextMode {
	case ContextModeNewSession:
		return reuseLineageOther, true
	case ContextModeContinueSession, ContextModeCompactAndContinueSession:
		return a.resolveContextSource(state, edge.ContextSource, target, targetBranch)
	default:
		return reuseLineageUnknown, true
	}
}

func (a sessionReuseAnalyzer) resolveContextSource(state reusePathState, source ContextSource, target Node, targetBranch reuseBranch) (reuseLineage, bool) {
	canonical := CanonicalContextSource(source)
	switch canonical.Kind {
	case ContextSourceImmediateSource:
		return state.currentLineage, true
	case ContextSourceSelectedNode:
		nodeID, exists := a.nodeIDsByKey[canonical.NodeKey]
		if !exists {
			return reuseLineageUnknown, true
		}
		lookupBranch, _ := reuseContextLookupBranch(state.branch, targetBranch, canonical)
		return a.associatedLineage(lookupBranch, state.overwritten, nodeID, true)
	case ContextSourcePreviousTarget:
		lookupBranch, _ := reuseContextLookupBranch(state.branch, targetBranch, canonical)
		return a.associatedLineage(lookupBranch, state.overwritten, NodeIDOf(target), false)
	case ContextSourcePreviousTargetOrNew:
		lookupBranch, _ := reuseContextLookupBranch(state.branch, targetBranch, canonical)
		lineage, found := a.associatedLineage(lookupBranch, state.overwritten, NodeIDOf(target), true)
		if !found {
			return reuseLineageOther, true
		}
		return lineage, true
	default:
		return reuseLineageUnknown, true
	}
}

func reuseContextLookupBranch(sourceBranch, targetBranch reuseBranch, source ContextSource) (reuseBranch, bool) {
	switch CanonicalContextSource(source).Kind {
	case ContextSourceSelectedNode:
		return sourceBranch, true
	case ContextSourcePreviousTarget, ContextSourcePreviousTargetOrNew:
		return targetBranch, true
	default:
		return reuseBranch{}, false
	}
}

func (a sessionReuseAnalyzer) associatedLineage(branch reuseBranch, overwritten map[reuseOverwriteKey]reuseOverwriteStatus, nodeID NodeID, optional bool) (reuseLineage, bool) {
	status, overwrittenPath := overwritten[reuseOverwriteKey{nodeID: nodeID, branch: branch}]
	if overwrittenPath && status == reuseOverwriteDefinite {
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
			if overwrittenPath && status == reuseOverwriteMaybe {
				return reuseLineageUnknown, true
			}
			return reuseLineageCompleted, true
		}
		return reuseLineageOther, true
	}
	if overwrittenPath {
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
	return nextReuseBranch(current, edge, target, groupEdgeCount)
}

func nextReuseBranch(current reuseBranch, edge Edge, target Node, groupEdgeCount int) reuseBranch {
	if target.Kind() == NodeKindJoin {
		return reuseBranch{}
	}
	if groupEdgeCount > 1 {
		key := TransitionBranchKey(strings.TrimSpace(string(edge.Key)))
		if key != "" {
			return reuseBranch{key: key, scoped: true}
		}
	}
	return current
}

func transitionBranchPointer(reference CurrentNodeReference) *TransitionBranchKey {
	branch, scoped := reference.TransitionBranchKey()
	if !scoped {
		return nil
	}
	return &branch
}

type reuseGraph struct {
	states        []reusePathState
	groups        [][]reuseGroup
	possibleReuse bool
}

func (a sessionReuseAnalyzer) buildGraph(initial []reuseTransition) reuseGraph {
	graph := reuseGraph{}
	processed := make(map[int]bool)
	// Overwritten provenance is merged by union when structural path states
	// converge. This deliberately trades some possible-reuse precision for a
	// bounded worklist instead of enumerating every overwrite subset.
	addState := func(state reusePathState) (int, bool) {
		if id, exists := graphStateID(state, graph.states); exists {
			merged, changed := mergeOverwritten(graph.states[id].overwritten, state.overwritten)
			if changed {
				graph.states[id].overwritten = merged
				graph.groups[id] = nil
				processed[id] = false
				return id, true
			}
			return id, false
		}
		id := len(graph.states)
		graph.states = append(graph.states, state)
		graph.groups = append(graph.groups, nil)
		processed[id] = false
		return id, true
	}
	queue := make([]int, 0, len(initial))
	queued := make(map[int]struct{}, len(initial))
	enqueue := func(stateID int) {
		if _, exists := queued[stateID]; exists {
			return
		}
		queued[stateID] = struct{}{}
		queue = append(queue, stateID)
	}
	for _, transition := range initial {
		if transition.possibleReuse {
			graph.possibleReuse = true
		}
		stateID, _ := addState(transition.target)
		enqueue(stateID)
	}
	for len(queue) > 0 {
		stateID := queue[0]
		queue = queue[1:]
		delete(queued, stateID)
		if processed[stateID] {
			continue
		}
		processed[stateID] = true
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
				targetID, changed := addState(transition.target)
				if changed {
					enqueue(targetID)
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

func cloneOverwritten(value map[reuseOverwriteKey]reuseOverwriteStatus) map[reuseOverwriteKey]reuseOverwriteStatus {
	if len(value) == 0 {
		return nil
	}
	cloned := make(map[reuseOverwriteKey]reuseOverwriteStatus, len(value))
	for key, status := range value {
		cloned[key] = status
	}
	return cloned
}

func mergeOverwritten(left, right map[reuseOverwriteKey]reuseOverwriteStatus) (map[reuseOverwriteKey]reuseOverwriteStatus, bool) {
	if len(right) == 0 {
		return left, false
	}
	changed := false
	for key, leftStatus := range left {
		rightStatus, exists := right[key]
		mergedStatus := reuseOverwriteMaybe
		if exists && leftStatus == reuseOverwriteDefinite && rightStatus == reuseOverwriteDefinite {
			mergedStatus = reuseOverwriteDefinite
		}
		if leftStatus != mergedStatus {
			changed = true
			break
		}
	}
	if !changed {
		for key := range right {
			if _, exists := left[key]; !exists {
				changed = true
				break
			}
		}
	}
	if !changed {
		return left, false
	}
	merged := cloneOverwritten(left)
	if merged == nil {
		merged = make(map[reuseOverwriteKey]reuseOverwriteStatus, len(right))
	}
	for key, leftStatus := range left {
		rightStatus, exists := right[key]
		if exists && leftStatus == reuseOverwriteDefinite && rightStatus == reuseOverwriteDefinite {
			merged[key] = reuseOverwriteDefinite
			continue
		}
		merged[key] = reuseOverwriteMaybe
	}
	for key := range right {
		if _, exists := left[key]; !exists {
			merged[key] = reuseOverwriteMaybe
		}
	}
	return merged, true
}

func sameReusePathState(left, right reusePathState) bool {
	if left.nodeID != right.nodeID ||
		left.branch != right.branch ||
		left.currentLineage != right.currentLineage ||
		left.completedDormant != right.completedDormant ||
		left.dormancyBlocked != right.dormancyBlocked {
		return false
	}
	return true
}
