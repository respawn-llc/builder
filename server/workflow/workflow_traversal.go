package workflow

import "strings"

type workflowTraversalRootMode uint8

const (
	workflowTraversalCompleteInvocation workflowTraversalRootMode = iota
	workflowTraversalPartialInvocation
)

type workflowTraversalState[S any] struct {
	nodeID     NodeID
	branch     reuseBranch
	analysis   S
	invocation *workflowTraversalInvocation[S]
}

type workflowTraversalInvocation[S any] struct {
	groupID        TransitionGroupID
	incomingBranch reuseBranch
	entry          S
	sibling        TransitionBranchKey
	expected       []TransitionBranchKey
	joinID         NodeID
}

type workflowTraversalTransition[S any, M any] struct {
	target   workflowTraversalState[S]
	metadata M
}

type workflowTraversalGroup[S any, M any] struct {
	transitions []workflowTraversalTransition[S, M]
}

type workflowTraversalGraph[S any, M any] struct {
	states  []workflowTraversalState[S]
	groups  [][]workflowTraversalGroup[S, M]
	initial []workflowTraversalTransition[S, M]
}

type workflowTraversalJoinArrival[S any, M any] struct {
	stateID    int
	groupIndex int
	state      workflowTraversalState[S]
	metadata   M
}

type workflowTraversalJoinReduction[S any, M any] struct {
	analysis S
	metadata M
}

type workflowTraversalAdapter[S any, M any] struct {
	applyEdge func(
		workflowTraversalState[S],
		Edge,
		Node,
		reuseBranch,
	) (workflowTraversalTransition[S, M], bool)
	reduceJoin func(
		[]workflowTraversalJoinArrival[S, M],
	) []workflowTraversalJoinReduction[S, M]
	sameAnalysis  func(S, S) bool
	mergeAnalysis func(*S, S) bool
}

type workflowTraversalFanout struct {
	groupID  TransitionGroupID
	joinID   NodeID
	siblings []TransitionBranchKey
}

type workflowTraversalTopology struct {
	fanoutTopology
	nodeIDsByKey map[ModelKey]NodeID
	fanouts      map[TransitionGroupID]workflowTraversalFanout
}

func newWorkflowTraversalTopology(def Definition) workflowTraversalTopology {
	base := newFanoutTopology(def)
	topology := workflowTraversalTopology{
		fanoutTopology: base,
		nodeIDsByKey:   make(map[ModelKey]NodeID, len(def.Nodes)),
		fanouts:        make(map[TransitionGroupID]workflowTraversalFanout),
	}
	for _, node := range def.Nodes {
		topology.nodeIDsByKey[NodeKey(node)] = NodeIDOf(node)
	}
	for _, group := range def.TransitionGroups {
		edges := topology.edgesByGroup[group.ID]
		if len(edges) < 2 {
			continue
		}
		distances := make([]map[NodeID]int, 0, len(edges))
		siblings := make([]TransitionBranchKey, 0, len(edges))
		valid := true
		for _, edge := range edges {
			key := TransitionBranchKey(strings.TrimSpace(string(edge.Key)))
			if key == "" {
				valid = false
				break
			}
			branchDistances, ok := fanoutBranchJoinDistances(
				topology.nodesByID,
				topology.groupsBySource,
				topology.edgesByGroup,
				topology.outgoingByNode,
				edge.TargetNodeID,
			)
			if !ok || len(branchDistances) == 0 {
				valid = false
				break
			}
			siblings = append(siblings, key)
			distances = append(distances, branchDistances)
		}
		if !valid {
			continue
		}
		joinID, ok := fanoutNearestCommonJoin(distances)
		if !ok {
			continue
		}
		for _, edge := range edges {
			if _, ok := fanoutBranchJoinEdge(
				topology.nodesByID,
				topology.groupsBySource,
				topology.edgesByGroup,
				topology.outgoingByNode,
				edge.TargetNodeID,
				joinID,
			); !ok {
				valid = false
				break
			}
		}
		if valid {
			topology.fanouts[group.ID] = workflowTraversalFanout{
				groupID: group.ID, joinID: joinID, siblings: siblings,
			}
		}
	}
	return topology
}

type workflowTraversalAccumulator[S any, M any] struct {
	invocation *workflowTraversalInvocation[S]
	arrivals   map[TransitionBranchKey]workflowTraversalJoinArrival[S, M]
}

func runWorkflowTraversal[S any, M any](
	topology workflowTraversalTopology,
	root workflowTraversalState[S],
	initialEdges []Edge,
	rootMode workflowTraversalRootMode,
	adapter workflowTraversalAdapter[S, M],
) workflowTraversalGraph[S, M] {
	graph := workflowTraversalGraph[S, M]{}
	processed := map[int]bool{}
	queue := []int{}
	queued := map[int]struct{}{}
	accumulators := []workflowTraversalAccumulator[S, M]{}

	sameInvocation := func(left, right *workflowTraversalInvocation[S]) bool {
		if left == nil || right == nil {
			return left == nil && right == nil
		}
		return left.groupID == right.groupID &&
			left.incomingBranch == right.incomingBranch &&
			left.sibling == right.sibling &&
			left.joinID == right.joinID &&
			adapter.sameAnalysis(left.entry, right.entry)
	}
	addState := func(state workflowTraversalState[S]) (int, bool) {
		for id := range graph.states {
			candidate := &graph.states[id]
			if candidate.nodeID != state.nodeID ||
				candidate.branch != state.branch ||
				!sameInvocation(candidate.invocation, state.invocation) {
				continue
			}
			if adapter.sameAnalysis(candidate.analysis, state.analysis) {
				if adapter.mergeAnalysis != nil &&
					adapter.mergeAnalysis(&candidate.analysis, state.analysis) {
					graph.groups[id] = nil
					processed[id] = false
					return id, true
				}
				return id, false
			}
		}
		id := len(graph.states)
		graph.states = append(graph.states, state)
		graph.groups = append(graph.groups, nil)
		return id, true
	}
	enqueue := func(stateID int) {
		if _, exists := queued[stateID]; exists {
			return
		}
		queued[stateID] = struct{}{}
		queue = append(queue, stateID)
	}
	appendTransition := func(
		stateID int,
		groupIndex int,
		transition workflowTraversalTransition[S, M],
	) {
		graph.groups[stateID][groupIndex].transitions = append(
			graph.groups[stateID][groupIndex].transitions,
			transition,
		)
		targetID, changed := addState(transition.target)
		if changed {
			enqueue(targetID)
		}
	}
	invocationMatches := func(
		left, right *workflowTraversalInvocation[S],
	) bool {
		if left == nil || right == nil {
			return false
		}
		return left.groupID == right.groupID &&
			left.incomingBranch == right.incomingBranch &&
			left.joinID == right.joinID &&
			adapter.sameAnalysis(left.entry, right.entry)
	}

	var traverseEdges func(
		state workflowTraversalState[S],
		stateID int,
		groupIndex int,
		edges []Edge,
		initial bool,
	)
	traverseEdges = func(
		state workflowTraversalState[S],
		stateID int,
		groupIndex int,
		edges []Edge,
		initial bool,
	) {
		fanout, fanoutActive := topology.fanouts[firstTraversalGroupID(edges)]
		for _, edge := range edges {
			target, exists := topology.nodesByID[edge.TargetNodeID]
			if !exists {
				continue
			}
			targetBranch := nextReuseBranch(
				state.branch,
				edge,
				target,
				len(topology.edgesByGroup[edge.TransitionGroupID]),
			)
			transition, ok := adapter.applyEdge(state, edge, target, targetBranch)
			if !ok {
				continue
			}
			transition.target.nodeID = edge.TargetNodeID
			transition.target.branch = targetBranch
			if fanoutActive && rootMode == workflowTraversalCompleteInvocation {
				transition.target.invocation = &workflowTraversalInvocation[S]{
					groupID:        fanout.groupID,
					incomingBranch: state.branch,
					entry:          state.analysis,
					sibling:        TransitionBranchKey(strings.TrimSpace(string(edge.Key))),
					expected:       append([]TransitionBranchKey(nil), fanout.siblings...),
					joinID:         fanout.joinID,
				}
			} else {
				transition.target.invocation = state.invocation
			}

			if target.Kind() != NodeKindJoin ||
				transition.target.invocation == nil ||
				transition.target.invocation.joinID != edge.TargetNodeID {
				if initial {
					graph.initial = append(graph.initial, transition)
					targetID, changed := addState(transition.target)
					if changed {
						enqueue(targetID)
					}
				} else {
					appendTransition(stateID, groupIndex, transition)
				}
				continue
			}

			arrival := workflowTraversalJoinArrival[S, M]{
				stateID: stateID, groupIndex: groupIndex,
				state: transition.target, metadata: transition.metadata,
			}
			accumulatorIndex := -1
			for index := range accumulators {
				if invocationMatches(
					accumulators[index].invocation,
					transition.target.invocation,
				) {
					accumulatorIndex = index
					break
				}
			}
			if accumulatorIndex < 0 {
				accumulators = append(accumulators, workflowTraversalAccumulator[S, M]{
					invocation: transition.target.invocation,
					arrivals:   map[TransitionBranchKey]workflowTraversalJoinArrival[S, M]{},
				})
				accumulatorIndex = len(accumulators) - 1
			}
			accumulator := &accumulators[accumulatorIndex]
			accumulator.arrivals[transition.target.invocation.sibling] = arrival
			if len(accumulator.arrivals) != len(accumulator.invocation.expected) {
				continue
			}
			ordered := make([]workflowTraversalJoinArrival[S, M], 0, len(accumulator.invocation.expected))
			complete := true
			for _, sibling := range accumulator.invocation.expected {
				siblingArrival, exists := accumulator.arrivals[sibling]
				if !exists {
					complete = false
					break
				}
				ordered = append(ordered, siblingArrival)
			}
			if !complete {
				continue
			}
			reductions := adapter.reduceJoin(ordered)
			accumulators = append(accumulators[:accumulatorIndex], accumulators[accumulatorIndex+1:]...)
			for reductionIndex, reduction := range reductions {
				reduced := transition
				reduced.target = workflowTraversalState[S]{
					nodeID:   edge.TargetNodeID,
					branch:   reuseBranch{},
					analysis: reduction.analysis,
				}
				reduced.metadata = reduction.metadata
				if len(reductions) == len(ordered) {
					selected := ordered[reductionIndex]
					if initial {
						graph.initial = append(graph.initial, reduced)
						targetID, changed := addState(reduced.target)
						if changed {
							enqueue(targetID)
						}
					} else {
						appendTransition(selected.stateID, selected.groupIndex, reduced)
					}
					continue
				}
				for _, selected := range ordered {
					if initial {
						graph.initial = append(graph.initial, reduced)
						targetID, changed := addState(reduced.target)
						if changed {
							enqueue(targetID)
						}
					} else {
						appendTransition(selected.stateID, selected.groupIndex, reduced)
					}
				}
			}
		}
	}

	traverseEdges(root, 0, 0, initialEdges, true)
	for len(queue) > 0 {
		stateID := queue[0]
		queue = queue[1:]
		delete(queued, stateID)
		if processed[stateID] {
			continue
		}
		processed[stateID] = true
		state := graph.states[stateID]
		groups := topology.groupsBySource[state.nodeID]
		graph.groups[stateID] = make([]workflowTraversalGroup[S, M], len(groups))
		for groupIndex, group := range groups {
			traverseEdges(
				state,
				stateID,
				groupIndex,
				topology.edgesByGroup[group.ID],
				false,
			)
		}
	}
	return graph
}

func firstTraversalGroupID(edges []Edge) TransitionGroupID {
	if len(edges) == 0 {
		return ""
	}
	groupID := edges[0].TransitionGroupID
	for _, edge := range edges[1:] {
		if edge.TransitionGroupID != groupID {
			return ""
		}
	}
	return groupID
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

func validateStaticContinuationSources(
	topology workflowTraversalTopology,
	start NodeID,
) []EdgeID {
	invalid := map[EdgeID]struct{}{}
	root := workflowTraversalState[staticContinuationSource]{
		nodeID:   start,
		analysis: staticContinuationSource{kind: staticContinuationSourceAbsent},
	}
	initialEdges := topology.outgoingByNode[start]
	runWorkflowTraversal(
		topology,
		root,
		initialEdges,
		workflowTraversalCompleteInvocation,
		workflowTraversalAdapter[staticContinuationSource, struct{}]{
			applyEdge: func(
				state workflowTraversalState[staticContinuationSource],
				edge Edge,
				target Node,
				_ reuseBranch,
			) (workflowTraversalTransition[staticContinuationSource, struct{}], bool) {
				targetSource := transferStaticContinuationSource(
					topology,
					state.analysis,
					edge,
					target,
				)
				if isRetainedTargetContextSource(edge.ContextSource) &&
					state.analysis.kind != staticContinuationSourceExact {
					invalid[edge.ID] = struct{}{}
				}
				return workflowTraversalTransition[staticContinuationSource, struct{}]{
					target: workflowTraversalState[staticContinuationSource]{
						analysis: targetSource,
					},
				}, true
			},
			reduceJoin: func(
				arrivals []workflowTraversalJoinArrival[staticContinuationSource, struct{}],
			) []workflowTraversalJoinReduction[staticContinuationSource, struct{}] {
				reduced := staticContinuationSource{kind: staticContinuationSourceAbsent}
				if len(arrivals) > 0 {
					reduced = arrivals[0].state.analysis
					for _, arrival := range arrivals[1:] {
						if arrival.state.analysis != reduced {
							reduced = staticContinuationSource{kind: staticContinuationSourceDivergent}
							break
						}
					}
				}
				return []workflowTraversalJoinReduction[staticContinuationSource, struct{}]{{
					analysis: reduced,
				}}
			},
			sameAnalysis: func(left, right staticContinuationSource) bool {
				return left == right
			},
		},
	)
	edgeIDs := make([]EdgeID, 0, len(invalid))
	for _, edge := range topology.edgesByGroup {
		for _, candidate := range edge {
			if _, exists := invalid[candidate.ID]; exists {
				edgeIDs = append(edgeIDs, candidate.ID)
			}
		}
	}
	return edgeIDs
}

func transferStaticContinuationSource(
	topology workflowTraversalTopology,
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
	switch edge.ContextMode {
	case ContextModeNewSession:
		return staticContinuationSource{
			kind:   staticContinuationSourceExact,
			nodeID: edge.TargetNodeID,
		}
	case ContextModeContinueSession, ContextModeCompactAndContinueSession:
	default:
		return staticContinuationSource{kind: staticContinuationSourceAbsent}
	}
	contextSource := CanonicalContextSource(edge.ContextSource)
	switch contextSource.Kind {
	case ContextSourceImmediateSource,
		ContextSourcePreviousTarget,
		ContextSourcePreviousTargetOrNew:
		return source
	case ContextSourceSelectedNode:
		selectedNodeID, exists := topology.nodeIDsByKey[contextSource.NodeKey]
		if exists {
			return staticContinuationSource{
				kind:   staticContinuationSourceExact,
				nodeID: selectedNodeID,
			}
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
