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
	SessionID       runtimeids.SessionID
	SourceSessionID runtimeids.SessionID
	CurrentNode     CurrentNodeReference
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
	analyzer.staticFanouts = sessionReuseFanouts(input.Workflow)

	initial := reusePathState{
		nodeID:           input.CompletedCurrentNode.Reference.NodeID,
		branch:           reuseBranchForReference(input.CompletedCurrentNode.Reference),
		currentLineage:   reuseLineageCompleted,
		completedDormant: acceptedBranchesFanout(input.AcceptedBranches),
		joinMode:         reuseJoinPartial,
	}
	if sourceSessionID, exact := input.CompletedCurrentNode.ContinuationSource.ExactSessionID(); exact {
		initial.activeSourceSessionID = sourceSessionID
		initial.activeSourceKnown = true
	} else {
		initial.activeSourceSessionID = completedSessionID
		initial.activeSourceKnown = true
	}
	initialTransitions := make([]reuseTransition, 0, len(input.AcceptedBranches))
	for _, edge := range input.AcceptedBranches {
		transition, ok := analyzer.applyEdge(initial, edge)
		if ok {
			if fanout, exists := analyzer.staticFanouts[edge.TransitionGroupID]; exists {
				transition.target.staticInvocation = staticFanoutInvocation{
					groupID: edge.TransitionGroupID, incomingBranch: initial.branch,
					entrySessionID: initial.activeSourceSessionID, entryKnown: initial.activeSourceKnown,
					sibling: TransitionBranchKey(edge.Key), joinID: fanout.joinID,
				}
				transition.target.joinMode = reuseJoinComplete
			}
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
	nodeID                NodeID
	branch                reuseBranch
	currentLineage        reuseLineage
	activeSourceSessionID runtimeids.SessionID
	activeSourceKnown     bool
	completedDormant      bool
	dormancyBlocked       bool
	overwritten           map[reuseOverwriteKey]reuseOverwriteStatus
	staticSource          staticContinuationSource
	staticInvocation      staticFanoutInvocation
	joinMode              reuseJoinMode
}

type reuseJoinMode uint8

const (
	reuseJoinPartial reuseJoinMode = iota
	reuseJoinComplete
)

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
	staticValidation   bool
	staticInvalidEdges map[EdgeID]struct{}
	staticFanouts      map[TransitionGroupID]staticFanout
}

func (a sessionReuseAnalyzer) applyEdge(state reusePathState, edge Edge) (reuseTransition, bool) {
	target, exists := a.nodesByID[edge.TargetNodeID]
	if !exists {
		return reuseTransition{}, false
	}
	groupEdges := a.edgesByGroup[edge.TransitionGroupID]
	targetBranch := a.nextBranch(state.branch, edge, target, len(groupEdges))
	if a.staticValidation {
		if isRetainedTargetContextSource(edge.ContextSource) &&
			state.staticSource.kind != staticContinuationSourceExact {
			a.staticInvalidEdges[edge.ID] = struct{}{}
		}
		return reuseTransition{target: reusePathState{
			nodeID:       edge.TargetNodeID,
			branch:       targetBranch,
			staticSource: a.transferStaticContinuationSource(state.staticSource, edge, target),
		}}, true
	}
	targetLineage, activeSourceSessionID, activeSourceKnown, selected := a.targetContext(state, edge, target, targetBranch)
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
		nodeID:                edge.TargetNodeID,
		branch:                targetBranch,
		currentLineage:        targetLineage,
		activeSourceSessionID: activeSourceSessionID,
		activeSourceKnown:     activeSourceKnown,
		completedDormant:      completedDormant,
		dormancyBlocked:       dormancyBlocked,
		overwritten:           cloneOverwritten(state.overwritten),
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

func (a sessionReuseAnalyzer) targetContext(
	state reusePathState,
	edge Edge,
	target Node,
	targetBranch reuseBranch,
) (reuseLineage, runtimeids.SessionID, bool, bool) {
	if target.Kind() != NodeKindAgent {
		return reuseLineageAbsent, state.activeSourceSessionID, state.activeSourceKnown, true
	}
	switch edge.ContextMode {
	case ContextModeNewSession:
		return reuseLineageOther, runtimeids.SessionID{}, false, true
	case ContextModeContinueSession, ContextModeCompactAndContinueSession:
		return a.resolveContextSource(state, edge.ContextSource, target, targetBranch)
	default:
		return reuseLineageUnknown, runtimeids.SessionID{}, false, true
	}
}

func (a sessionReuseAnalyzer) resolveContextSource(
	state reusePathState,
	source ContextSource,
	target Node,
	targetBranch reuseBranch,
) (reuseLineage, runtimeids.SessionID, bool, bool) {
	canonical := CanonicalContextSource(source)
	switch canonical.Kind {
	case ContextSourceImmediateSource:
		return state.currentLineage, state.activeSourceSessionID, state.activeSourceKnown, true
	case ContextSourceSelectedNode:
		nodeID, exists := a.nodeIDsByKey[canonical.NodeKey]
		if !exists {
			return reuseLineageUnknown, runtimeids.SessionID{}, false, true
		}
		lookupBranch, _ := reuseContextLookupBranch(state.branch, targetBranch, canonical)
		lineage, association, found := a.associatedLineage(state, lookupBranch, nodeID, true, false)
		if !found {
			return lineage, runtimeids.SessionID{}, false, true
		}
		return lineage, association.SessionID, true, true
	case ContextSourcePreviousTarget:
		lookupBranch, _ := reuseContextLookupBranch(state.branch, targetBranch, canonical)
		lineage, _, found := a.associatedLineage(state, lookupBranch, NodeIDOf(target), false, true)
		return lineage, state.activeSourceSessionID, state.activeSourceKnown, found
	case ContextSourcePreviousTargetOrNew:
		lookupBranch, _ := reuseContextLookupBranch(state.branch, targetBranch, canonical)
		lineage, _, found := a.associatedLineage(state, lookupBranch, NodeIDOf(target), true, true)
		if !found {
			return reuseLineageOther, runtimeids.SessionID{}, false, true
		}
		return lineage, state.activeSourceSessionID, state.activeSourceKnown, true
	default:
		return reuseLineageUnknown, runtimeids.SessionID{}, false, true
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

func (a sessionReuseAnalyzer) associatedLineage(
	state reusePathState,
	branch reuseBranch,
	nodeID NodeID,
	optional bool,
	retainedTarget bool,
) (reuseLineage, SessionReuseAssociation, bool) {
	status, overwrittenPath := state.overwritten[reuseOverwriteKey{nodeID: nodeID, branch: branch}]
	if overwrittenPath && status == reuseOverwriteDefinite {
		return reuseLineageOther, SessionReuseAssociation{}, true
	}
	for index := len(a.input.RetainedAssociations) - 1; index >= 0; index-- {
		association := a.input.RetainedAssociations[index]
		if association.CurrentNode.TaskID != a.input.CompletedCurrentNode.Reference.TaskID ||
			association.CurrentNode.NodeID != nodeID ||
			!sameReuseBranch(branch, association.CurrentNode) {
			continue
		}
		if retainedTarget &&
			(!state.activeSourceKnown || association.SourceSessionID.IsZero() ||
				association.SourceSessionID != state.activeSourceSessionID) {
			return reuseLineageOther, association, true
		}
		if association.SessionID == a.completedSessionID {
			if overwrittenPath && status == reuseOverwriteMaybe {
				return reuseLineageUnknown, association, true
			}
			return reuseLineageCompleted, association, true
		}
		return reuseLineageOther, association, true
	}
	if overwrittenPath {
		return reuseLineageOther, SessionReuseAssociation{}, true
	}
	if optional {
		return reuseLineageOther, SessionReuseAssociation{}, false
	}
	return reuseLineageAbsent, SessionReuseAssociation{}, false
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
	joinArrivals := make(map[staticFanoutInvocation]map[TransitionBranchKey]reusePathState)
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
				if transition.target.staticInvocation.joinID == transition.target.nodeID {
					invocation := transition.target.staticInvocation
					arrivals := joinArrivals[invocation.withoutSibling()]
					if arrivals == nil {
						arrivals = make(map[TransitionBranchKey]reusePathState, len(a.staticFanouts[invocation.groupID].siblings))
						joinArrivals[invocation.withoutSibling()] = arrivals
					}
					arrivals[invocation.sibling] = transition.target
					fanout := a.staticFanouts[invocation.groupID]
					if len(arrivals) != len(fanout.siblings) {
						continue
					}
					delete(joinArrivals, invocation.withoutSibling())
					source := staticContinuationSource{}
					var reduced reusePathState
					for index, sibling := range fanout.siblings {
						state := arrivals[sibling]
						arrival := state.staticSource
						if index == 0 {
							source = arrival
							reduced = state
						} else if source != arrival {
							source = staticContinuationSource{kind: staticContinuationSourceDivergent}
						}
						reduced = reduceJoinState(reduced, state)
					}
					transition.target = reduced
					transition.target.nodeID = invocation.joinID
					transition.target.branch = reuseBranch{}
					transition.target.staticSource = source
					transition.target.staticInvocation = staticFanoutInvocation{}
					transition.target.joinMode = reuseJoinPartial
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
				if fanout, exists := a.staticFanouts[group.ID]; exists {
					transition.target.staticInvocation = staticFanoutInvocation{
						groupID: group.ID, incomingBranch: state.branch,
						entry: state.staticSource, sibling: TransitionBranchKey(edge.Key),
						entrySessionID: state.activeSourceSessionID, entryKnown: state.activeSourceKnown,
						joinID: fanout.joinID,
					}
					transition.target.joinMode = reuseJoinComplete
				} else {
					transition.target.staticInvocation = state.staticInvocation
					transition.target.joinMode = state.joinMode
				}
				transitions = append(transitions, transition)
			}
		}
		result = append(result, reuseGroup{transitions: transitions})
	}
	return result
}

func reduceJoinState(left, right reusePathState) reusePathState {
	if left.currentLineage != right.currentLineage {
		left.currentLineage = reuseLineageUnknown
	}
	if !left.activeSourceKnown || !right.activeSourceKnown ||
		left.activeSourceSessionID != right.activeSourceSessionID {
		left.activeSourceSessionID = runtimeids.SessionID{}
		left.activeSourceKnown = false
	}
	left.completedDormant = left.completedDormant || right.completedDormant
	left.dormancyBlocked = left.dormancyBlocked || right.dormancyBlocked
	left.overwritten = mergeCompleteJoinOverwritten(left.overwritten, right.overwritten)
	return left
}

func mergeCompleteJoinOverwritten(left, right map[reuseOverwriteKey]reuseOverwriteStatus) map[reuseOverwriteKey]reuseOverwriteStatus {
	merged := cloneOverwritten(left)
	if merged == nil && len(right) > 0 {
		merged = make(map[reuseOverwriteKey]reuseOverwriteStatus, len(right))
	}
	for key, status := range right {
		if existing, found := merged[key]; !found || status == reuseOverwriteDefinite || existing == reuseOverwriteMaybe {
			merged[key] = status
		}
	}
	return merged
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
		left.activeSourceSessionID != right.activeSourceSessionID ||
		left.activeSourceKnown != right.activeSourceKnown ||
		left.staticSource != right.staticSource ||
		left.staticInvocation != right.staticInvocation ||
		left.joinMode != right.joinMode ||
		left.completedDormant != right.completedDormant ||
		left.dormancyBlocked != right.dormancyBlocked {
		return false
	}
	return true
}

type staticContinuationSourceKind uint8

const (
	staticContinuationSourceAbsent staticContinuationSourceKind = iota
	staticContinuationSourceExact
	staticContinuationSourceDivergent
)

type staticContinuationSource struct {
	kind   staticContinuationSourceKind
	nodeID NodeID
}

type staticFanout struct {
	joinID   NodeID
	siblings []TransitionBranchKey
}

type staticFanoutInvocation struct {
	groupID        TransitionGroupID
	incomingBranch reuseBranch
	entry          staticContinuationSource
	entrySessionID runtimeids.SessionID
	entryKnown     bool
	sibling        TransitionBranchKey
	joinID         NodeID
}

func (i staticFanoutInvocation) withoutSibling() staticFanoutInvocation {
	i.sibling = ""
	return i
}

func validateStaticContinuationSources(def Definition, start NodeID) []EdgeID {
	analyzer := newStaticSessionReuseAnalyzer(def)
	initial := reusePathState{
		nodeID:       start,
		staticSource: staticContinuationSource{kind: staticContinuationSourceAbsent},
	}
	transitions := make([]reuseTransition, 0, len(analyzer.groupsBySource[start]))
	for _, group := range analyzer.stateGroups(initial) {
		transitions = append(transitions, group.transitions...)
	}
	analyzer.buildGraph(transitions)
	result := make([]EdgeID, 0, len(analyzer.staticInvalidEdges))
	for _, edge := range def.Edges {
		if _, invalid := analyzer.staticInvalidEdges[edge.ID]; invalid {
			result = append(result, edge.ID)
		}
	}
	return result
}

func newStaticSessionReuseAnalyzer(def Definition) sessionReuseAnalyzer {
	analyzer := sessionReuseAnalyzer{
		input:              SessionReuseAnalysisInput{Workflow: def},
		nodesByID:          make(map[NodeID]Node, len(def.Nodes)),
		nodeIDsByKey:       make(map[ModelKey]NodeID, len(def.Nodes)),
		groupsBySource:     make(map[NodeID][]TransitionGroup, len(def.TransitionGroups)),
		edgesByGroup:       make(map[TransitionGroupID][]Edge, len(def.Edges)),
		staticValidation:   true,
		staticInvalidEdges: make(map[EdgeID]struct{}),
		staticFanouts:      make(map[TransitionGroupID]staticFanout),
	}
	topology := newFanoutTopology(def)
	for _, node := range def.Nodes {
		analyzer.nodesByID[NodeIDOf(node)] = node
		analyzer.nodeIDsByKey[NodeKey(node)] = NodeIDOf(node)
	}
	for _, group := range def.TransitionGroups {
		analyzer.groupsBySource[group.SourceNodeID] = append(analyzer.groupsBySource[group.SourceNodeID], group)
		analyzer.edgesByGroup[group.ID] = append(analyzer.edgesByGroup[group.ID], topology.edgesByGroup[group.ID]...)
	}
	analyzer.staticFanouts = sessionReuseFanouts(def)
	return analyzer
}

func sessionReuseFanouts(def Definition) map[TransitionGroupID]staticFanout {
	topology := newFanoutTopology(def)
	fanouts := make(map[TransitionGroupID]staticFanout)
	for _, group := range def.TransitionGroups {
		edges := topology.edgesByGroup[group.ID]
		if len(edges) < 2 {
			continue
		}
		distances := make([]map[NodeID]int, 0, len(edges))
		siblings := make([]TransitionBranchKey, 0, len(edges))
		for _, edge := range edges {
			branchDistances, ok := fanoutBranchJoinDistances(
				topology.nodesByID, topology.groupsBySource, topology.edgesByGroup,
				topology.outgoingByNode, edge.TargetNodeID,
			)
			if !ok {
				distances = nil
				break
			}
			distances = append(distances, branchDistances)
			siblings = append(siblings, TransitionBranchKey(edge.Key))
		}
		if joinID, ok := fanoutNearestCommonJoin(distances); ok {
			fanouts[group.ID] = staticFanout{joinID: joinID, siblings: siblings}
		}
	}
	return fanouts
}

func (a sessionReuseAnalyzer) transferStaticContinuationSource(
	source staticContinuationSource,
	edge Edge,
	target Node,
) staticContinuationSource {
	if target.Kind() == NodeKindTerminal {
		return staticContinuationSource{kind: staticContinuationSourceAbsent}
	}
	if target.Kind() != NodeKindAgent {
		return source
	}
	if edge.ContextMode == ContextModeNewSession {
		return staticContinuationSource{kind: staticContinuationSourceExact, nodeID: edge.TargetNodeID}
	}
	if edge.ContextMode != ContextModeContinueSession &&
		edge.ContextMode != ContextModeCompactAndContinueSession {
		return staticContinuationSource{kind: staticContinuationSourceAbsent}
	}
	switch contextSource := CanonicalContextSource(edge.ContextSource); contextSource.Kind {
	case ContextSourceImmediateSource, ContextSourcePreviousTarget, ContextSourcePreviousTargetOrNew:
		return source
	case ContextSourceSelectedNode:
		if nodeID, exists := a.nodeIDsByKey[contextSource.NodeKey]; exists {
			return staticContinuationSource{kind: staticContinuationSourceExact, nodeID: nodeID}
		}
	}
	return staticContinuationSource{kind: staticContinuationSourceAbsent}
}

func isRetainedTargetContextSource(source ContextSource) bool {
	switch CanonicalContextSource(source).Kind {
	case ContextSourcePreviousTarget, ContextSourcePreviousTargetOrNew:
		return true
	default:
		return false
	}
}
