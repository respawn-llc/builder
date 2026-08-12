package workflow

import (
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

// SessionReuseAssociation is bounded current retained authority supplied by
// the Workflow store, including the exact source proof used for freshness.
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
	topology := newWorkflowTraversalTopology(input.Workflow)

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

	appendContextReference := func(sourceBranch, targetBranch reuseBranch, edge Edge, target Node) {
		if target == nil {
			return
		}
		switch edge.ContextMode {
		case ContextModeContinueSession, ContextModeCompactAndContinueSession:
		default:
			return
		}
		source := CanonicalContextSource(edge.ContextSource)
		lookupBranch, usesAssociation := reuseContextLookupBranch(sourceBranch, targetBranch, source)
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
			if nodeID, ok := topology.nodeIDsByKey[source.NodeKey]; ok {
				appendReference(nodeID, branch)
			}
		case ContextSourcePreviousTarget, ContextSourcePreviousTargetOrNew:
			appendReference(NodeIDOf(target), branch)
		}
	}

	rootMode := workflowTraversalCompleteInvocation
	if input.CompletedCurrentNode.Reference.IsBranchScoped() {
		rootMode = workflowTraversalPartialInvocation
	}
	runWorkflowTraversal(
		topology,
		workflowTraversalState[struct{}]{
			nodeID:   input.CompletedCurrentNode.Reference.NodeID,
			branch:   reuseBranchForReference(input.CompletedCurrentNode.Reference),
			analysis: struct{}{},
		},
		input.AcceptedBranches,
		rootMode,
		workflowTraversalAdapter[struct{}, struct{}]{
			applyEdge: func(
				state workflowTraversalState[struct{}],
				edge Edge,
				target Node,
				targetBranch reuseBranch,
			) (workflowTraversalTransition[struct{}, struct{}], bool) {
				appendContextReference(state.branch, targetBranch, edge, target)
				return workflowTraversalTransition[struct{}, struct{}]{
					target: workflowTraversalState[struct{}]{analysis: struct{}{}},
				}, true
			},
			reduceJoin: func(
				arrivals []workflowTraversalJoinArrival[struct{}, struct{}],
			) []workflowTraversalJoinReduction[struct{}, struct{}] {
				if len(arrivals) == 0 {
					return nil
				}
				return []workflowTraversalJoinReduction[struct{}, struct{}]{{analysis: struct{}{}}}
			},
			sameAnalysis: func(struct{}, struct{}) bool { return true },
		},
	)
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

	topology := newWorkflowTraversalTopology(input.Workflow)
	analyzer := sessionReuseAnalyzer{
		input:              input,
		completedSessionID: completedSessionID,
		topology:           topology,
	}

	initial := reusePathState{
		nodeID:           input.CompletedCurrentNode.Reference.NodeID,
		branch:           reuseBranchForReference(input.CompletedCurrentNode.Reference),
		currentLineage:   reuseLineageCompleted,
		completedDormant: acceptedBranchesFanout(input.AcceptedBranches),
	}
	if sourceSessionID, exact := input.CompletedCurrentNode.ContinuationSource.ExactSessionID(); exact {
		initial.activeSourceSessionID = sourceSessionID
		initial.activeSourceKnown = true
	} else {
		initial.activeSourceSessionID = completedSessionID
		initial.activeSourceKnown = true
	}
	rootMode := workflowTraversalCompleteInvocation
	if input.CompletedCurrentNode.Reference.IsBranchScoped() {
		rootMode = workflowTraversalPartialInvocation
	}
	traversed := runWorkflowTraversal(
		topology,
		workflowTraversalState[reusePathState]{
			nodeID:   input.CompletedCurrentNode.Reference.NodeID,
			branch:   reuseBranchForReference(input.CompletedCurrentNode.Reference),
			analysis: initial,
		},
		input.AcceptedBranches,
		rootMode,
		workflowTraversalAdapter[reusePathState, reuseTransitionMetadata]{
			applyEdge: func(
				state workflowTraversalState[reusePathState],
				edge Edge,
				_ Node,
				_ reuseBranch,
			) (workflowTraversalTransition[reusePathState, reuseTransitionMetadata], bool) {
				transition, ok := analyzer.applyEdge(state.analysis, edge)
				if !ok {
					return workflowTraversalTransition[reusePathState, reuseTransitionMetadata]{}, false
				}
				return workflowTraversalTransition[reusePathState, reuseTransitionMetadata]{
					target: workflowTraversalState[reusePathState]{
						nodeID:   transition.target.nodeID,
						branch:   transition.target.branch,
						analysis: transition.target,
					},
					metadata: reuseTransitionMetadata{
						possibleReuse: transition.possibleReuse,
						guaranteedCAC: transition.guaranteedCAC,
					},
				}, true
			},
			reduceJoin: func(
				arrivals []workflowTraversalJoinArrival[reusePathState, reuseTransitionMetadata],
			) []workflowTraversalJoinReduction[reusePathState, reuseTransitionMetadata] {
				reductions := make(
					[]workflowTraversalJoinReduction[reusePathState, reuseTransitionMetadata],
					0,
					len(arrivals),
				)
				for _, arrival := range arrivals {
					reductions = append(reductions, workflowTraversalJoinReduction[reusePathState, reuseTransitionMetadata]{
						analysis: arrival.state.analysis,
						metadata: arrival.metadata,
					})
				}
				return reductions
			},
			sameAnalysis: sameReusePathState,
			mergeAnalysis: func(left *reusePathState, right reusePathState) bool {
				merged, changed := mergeOverwritten(left.overwritten, right.overwritten)
				if changed {
					left.overwritten = merged
				}
				return changed
			},
		},
	)
	initialTransitions, graph := reuseGraphFromTraversal(traversed)
	if len(initialTransitions) == 0 {
		return SessionReuseNone
	}

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

type reuseTransitionMetadata struct {
	possibleReuse bool
	guaranteedCAC bool
}

type reuseGroup struct {
	transitions []reuseTransition
}

type sessionReuseAnalyzer struct {
	input              SessionReuseAnalysisInput
	completedSessionID runtimeids.SessionID
	topology           workflowTraversalTopology
}

func (a sessionReuseAnalyzer) applyEdge(state reusePathState, edge Edge) (reuseTransition, bool) {
	target, exists := a.topology.nodesByID[edge.TargetNodeID]
	if !exists {
		return reuseTransition{}, false
	}
	groupEdges := a.topology.edgesByGroup[edge.TransitionGroupID]
	targetBranch := a.nextBranch(state.branch, edge, target, len(groupEdges))
	targetLineage, activeSourceSessionID, activeSourceKnown, selected := a.targetContext(
		state,
		edge,
		target,
		targetBranch,
	)
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
		nodeID, exists := a.topology.nodeIDsByKey[canonical.NodeKey]
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
		if retainedTarget && !a.retainedTargetSourceMatches(state, association.SourceSessionID) {
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

func (a sessionReuseAnalyzer) retainedTargetSourceMatches(
	state reusePathState,
	sourceSessionID runtimeids.SessionID,
) bool {
	if sourceSessionID.IsZero() {
		return false
	}
	return state.activeSourceKnown && sourceSessionID == state.activeSourceSessionID
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

func reuseGraphFromTraversal(
	traversed workflowTraversalGraph[reusePathState, reuseTransitionMetadata],
) ([]reuseTransition, reuseGraph) {
	initial := make([]reuseTransition, 0, len(traversed.initial))
	graph := reuseGraph{
		states: make([]reusePathState, len(traversed.states)),
		groups: make([][]reuseGroup, len(traversed.groups)),
	}
	for index, state := range traversed.states {
		graph.states[index] = state.analysis
	}
	for _, transition := range traversed.initial {
		reuseTransition := reuseTransition{
			target:        transition.target.analysis,
			possibleReuse: transition.metadata.possibleReuse,
			guaranteedCAC: transition.metadata.guaranteedCAC,
		}
		initial = append(initial, reuseTransition)
		if reuseTransition.possibleReuse {
			graph.possibleReuse = true
		}
	}
	for stateID, groups := range traversed.groups {
		graph.groups[stateID] = make([]reuseGroup, len(groups))
		for groupID, group := range groups {
			transitions := make([]reuseTransition, 0, len(group.transitions))
			for _, transition := range group.transitions {
				reuseTransition := reuseTransition{
					target:        transition.target.analysis,
					possibleReuse: transition.metadata.possibleReuse,
					guaranteedCAC: transition.metadata.guaranteedCAC,
				}
				transitions = append(transitions, reuseTransition)
				if reuseTransition.possibleReuse {
					graph.possibleReuse = true
				}
			}
			graph.groups[stateID][groupID] = reuseGroup{transitions: transitions}
		}
	}
	return initial, graph
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
		left.completedDormant != right.completedDormant ||
		left.dormancyBlocked != right.dormancyBlocked {
		return false
	}
	return true
}
