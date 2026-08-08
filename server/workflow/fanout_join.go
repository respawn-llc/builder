package workflow

import "strings"

// FanoutJoinResolution is the current-definition topology that maps each
// frozen fan-out branch to the one Join edge through which it arrives.
type FanoutJoinResolution struct {
	Join            Node
	BranchJoinEdges map[TransitionBranchKey]Edge
}

type fanoutTopology struct {
	nodesByID      map[NodeID]Node
	groupsBySource map[NodeID][]TransitionGroup
	edgesByGroup   map[TransitionGroupID][]Edge
	outgoingByNode map[NodeID][]Edge
}

func newFanoutTopology(def Definition) fanoutTopology {
	topology := fanoutTopology{
		nodesByID:      make(map[NodeID]Node, len(def.Nodes)),
		groupsBySource: make(map[NodeID][]TransitionGroup, len(def.TransitionGroups)),
		edgesByGroup:   make(map[TransitionGroupID][]Edge, len(def.TransitionGroups)),
		outgoingByNode: make(map[NodeID][]Edge, len(def.Nodes)),
	}
	for _, node := range def.Nodes {
		topology.nodesByID[NodeIDOf(node)] = node
	}
	for _, group := range def.TransitionGroups {
		topology.groupsBySource[group.SourceNodeID] = append(topology.groupsBySource[group.SourceNodeID], group)
	}
	groupsByID := make(map[TransitionGroupID]TransitionGroup, len(def.TransitionGroups))
	for _, group := range def.TransitionGroups {
		groupsByID[group.ID] = group
	}
	for _, edge := range def.Edges {
		topology.edgesByGroup[edge.TransitionGroupID] = append(topology.edgesByGroup[edge.TransitionGroupID], edge)
		if group, exists := groupsByID[edge.TransitionGroupID]; exists {
			topology.outgoingByNode[group.SourceNodeID] = append(topology.outgoingByNode[group.SourceNodeID], edge)
		}
	}
	return topology
}

// SerialTransitionRequiresFanoutSiblings reports whether a one-target
// Transition enters a valid Fan-Out branch before its Join. Such a Transition
// cannot recreate the sibling Current Nodes required by the Join.
func SerialTransitionRequiresFanoutSiblings(def Definition, transitionGroupID TransitionGroupID) bool {
	topology := newFanoutTopology(def)
	var targetNodeID NodeID
	found := false
	for _, group := range def.TransitionGroups {
		if group.ID != transitionGroupID {
			continue
		}
		edges := topology.edgesByGroup[group.ID]
		if len(edges) != 1 {
			return false
		}
		targetNodeID = edges[0].TargetNodeID
		found = true
		break
	}
	if !found {
		return false
	}

	for _, fanoutGroup := range def.TransitionGroups {
		fanoutEdges := topology.edgesByGroup[fanoutGroup.ID]
		if len(fanoutEdges) < 2 {
			continue
		}
		branchJoinDistances := make([]map[NodeID]int, 0, len(fanoutEdges))
		valid := true
		for _, edge := range fanoutEdges {
			traversal, ok := fanoutBranchTraversal(
				topology.nodesByID,
				topology.groupsBySource,
				topology.edgesByGroup,
				topology.outgoingByNode,
				edge.TargetNodeID,
			)
			if !ok || len(traversal.joinDistances) == 0 {
				valid = false
				break
			}
			branchJoinDistances = append(branchJoinDistances, traversal.joinDistances)
		}
		if !valid {
			continue
		}
		joinID, ok := fanoutNearestCommonJoin(branchJoinDistances)
		if !ok {
			continue
		}
		join, exists := topology.nodesByID[joinID]
		if !exists || join.Kind() != NodeKindJoin {
			continue
		}
		for _, edge := range fanoutEdges {
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
		if !valid {
			continue
		}
		for _, edge := range fanoutEdges {
			traversal, ok := fanoutBranchTraversal(
				topology.nodesByID,
				topology.groupsBySource,
				topology.edgesByGroup,
				topology.outgoingByNode,
				edge.TargetNodeID,
			)
			if ok {
				if _, exists := traversal.pathNodes[targetNodeID]; exists {
					return true
				}
			}
		}
	}
	return false
}

// ResolveFanoutJoin resolves one current-definition Join for the frozen
// fan-out branch keys. It returns false when those keys no longer identify one
// valid fan-out topology with an unambiguous common Join.
func ResolveFanoutJoin(def Definition, branchKeys []TransitionBranchKey) (FanoutJoinResolution, bool) {
	expected := make(map[ModelKey]struct{}, len(branchKeys))
	for _, branchKey := range branchKeys {
		key := ModelKey(strings.TrimSpace(string(branchKey)))
		if key == "" {
			return FanoutJoinResolution{}, false
		}
		if _, exists := expected[key]; exists {
			return FanoutJoinResolution{}, false
		}
		expected[key] = struct{}{}
	}
	if len(expected) < 2 {
		return FanoutJoinResolution{}, false
	}

	topology := newFanoutTopology(def)

	initialEdges := map[TransitionBranchKey]Edge{}
	foundFanout := false
	for _, group := range def.TransitionGroups {
		edges := topology.edgesByGroup[group.ID]
		if len(edges) != len(expected) {
			continue
		}
		candidate := make(map[TransitionBranchKey]Edge, len(edges))
		for _, edge := range edges {
			key := ModelKey(strings.TrimSpace(string(edge.Key)))
			if _, exists := expected[key]; !exists {
				continue
			}
			branchKey := TransitionBranchKey(key)
			if _, exists := candidate[branchKey]; exists {
				continue
			}
			candidate[branchKey] = edge
		}
		if len(candidate) != len(expected) {
			continue
		}
		if foundFanout {
			return FanoutJoinResolution{}, false
		}
		initialEdges = candidate
		foundFanout = true
	}
	if !foundFanout {
		return FanoutJoinResolution{}, false
	}

	distancesByBranch := make([]map[NodeID]int, 0, len(branchKeys))
	for _, branchKey := range branchKeys {
		edge := initialEdges[branchKey]
		distances, ok := fanoutBranchJoinDistances(topology.nodesByID, topology.groupsBySource, topology.edgesByGroup, topology.outgoingByNode, edge.TargetNodeID)
		if !ok || len(distances) == 0 {
			return FanoutJoinResolution{}, false
		}
		distancesByBranch = append(distancesByBranch, distances)
	}
	joinID, ok := fanoutNearestCommonJoin(distancesByBranch)
	if !ok {
		return FanoutJoinResolution{}, false
	}
	join, exists := topology.nodesByID[joinID]
	if !exists || join.Kind() != NodeKindJoin {
		return FanoutJoinResolution{}, false
	}

	branchJoinEdges := make(map[TransitionBranchKey]Edge, len(branchKeys))
	for _, branchKey := range branchKeys {
		edge, ok := fanoutBranchJoinEdge(topology.nodesByID, topology.groupsBySource, topology.edgesByGroup, topology.outgoingByNode, initialEdges[branchKey].TargetNodeID, joinID)
		if !ok {
			return FanoutJoinResolution{}, false
		}
		branchJoinEdges[branchKey] = edge
	}
	return FanoutJoinResolution{Join: join, BranchJoinEdges: branchJoinEdges}, true
}

func fanoutNearestCommonJoin(branchJoinDistances []map[NodeID]int) (NodeID, bool) {
	if len(branchJoinDistances) == 0 {
		return "", false
	}
	common := make(map[NodeID]int, len(branchJoinDistances[0]))
	for joinID, distance := range branchJoinDistances[0] {
		common[joinID] = distance
	}
	for _, distances := range branchJoinDistances[1:] {
		for joinID := range common {
			distance, exists := distances[joinID]
			if !exists {
				delete(common, joinID)
				continue
			}
			common[joinID] += distance
		}
	}
	if len(common) == 0 {
		return "", false
	}
	var nearestJoinID NodeID
	nearestDistance := 0
	nearestCount := 0
	for joinID, distance := range common {
		if nearestCount == 0 || distance < nearestDistance {
			nearestJoinID = joinID
			nearestDistance = distance
			nearestCount = 1
			continue
		}
		if distance == nearestDistance {
			nearestCount++
		}
	}
	return nearestJoinID, nearestCount == 1
}

func fanoutBranchJoinDistances(
	nodesByID map[NodeID]Node,
	groupsBySource map[NodeID][]TransitionGroup,
	edgesByGroup map[TransitionGroupID][]Edge,
	outgoingByNode map[NodeID][]Edge,
	start NodeID,
) (map[NodeID]int, bool) {
	traversal, ok := fanoutBranchTraversal(nodesByID, groupsBySource, edgesByGroup, outgoingByNode, start)
	if !ok {
		return nil, false
	}
	return traversal.joinDistances, true
}

type fanoutBranchTraversalResult struct {
	pathNodes     map[NodeID]int
	joinDistances map[NodeID]int
}

func fanoutBranchTraversal(
	nodesByID map[NodeID]Node,
	groupsBySource map[NodeID][]TransitionGroup,
	edgesByGroup map[TransitionGroupID][]Edge,
	outgoingByNode map[NodeID][]Edge,
	start NodeID,
) (fanoutBranchTraversalResult, bool) {
	type frame struct {
		nodeID   NodeID
		distance int
		path     map[NodeID]bool
	}
	result := fanoutBranchTraversalResult{
		pathNodes:     map[NodeID]int{},
		joinDistances: map[NodeID]int{},
	}
	stack := []frame{{nodeID: start, distance: 0, path: map[NodeID]bool{}}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.path[current.nodeID] {
			return fanoutBranchTraversalResult{}, false
		}
		node, exists := nodesByID[current.nodeID]
		if !exists {
			return fanoutBranchTraversalResult{}, false
		}
		if node.Kind() == NodeKindJoin {
			previous, exists := result.joinDistances[current.nodeID]
			if !exists || current.distance < previous {
				result.joinDistances[current.nodeID] = current.distance
			}
			continue
		}
		if node.Kind() == NodeKindTerminal {
			return fanoutBranchTraversalResult{}, false
		}
		previous, exists := result.pathNodes[current.nodeID]
		if !exists || current.distance < previous {
			result.pathNodes[current.nodeID] = current.distance
		}
		for _, group := range groupsBySource[current.nodeID] {
			if len(edgesByGroup[group.ID]) > 1 {
				return fanoutBranchTraversalResult{}, false
			}
		}
		nextPath := make(map[NodeID]bool, len(current.path)+1)
		for nodeID, visited := range current.path {
			nextPath[nodeID] = visited
		}
		nextPath[current.nodeID] = true
		for _, edge := range outgoingByNode[current.nodeID] {
			stack = append(stack, frame{nodeID: edge.TargetNodeID, distance: current.distance + 1, path: nextPath})
		}
	}
	return result, true
}

func fanoutBranchJoinEdge(
	nodesByID map[NodeID]Node,
	groupsBySource map[NodeID][]TransitionGroup,
	edgesByGroup map[TransitionGroupID][]Edge,
	outgoingByNode map[NodeID][]Edge,
	start NodeID,
	joinID NodeID,
) (Edge, bool) {
	type frame struct {
		nodeID NodeID
		path   map[NodeID]bool
	}
	candidates := []Edge{}
	stack := []frame{{nodeID: start, path: map[NodeID]bool{}}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.path[current.nodeID] {
			return Edge{}, false
		}
		node, exists := nodesByID[current.nodeID]
		if !exists || node.Kind() == NodeKindTerminal || node.Kind() == NodeKindJoin {
			return Edge{}, false
		}
		for _, group := range groupsBySource[current.nodeID] {
			if len(edgesByGroup[group.ID]) > 1 {
				return Edge{}, false
			}
		}
		nextPath := make(map[NodeID]bool, len(current.path)+1)
		for nodeID, visited := range current.path {
			nextPath[nodeID] = visited
		}
		nextPath[current.nodeID] = true
		for _, edge := range outgoingByNode[current.nodeID] {
			target, exists := nodesByID[edge.TargetNodeID]
			if !exists {
				return Edge{}, false
			}
			if target.Kind() == NodeKindJoin {
				if edge.TargetNodeID != joinID {
					return Edge{}, false
				}
				candidates = append(candidates, edge)
				continue
			}
			stack = append(stack, frame{nodeID: edge.TargetNodeID, path: nextPath})
		}
	}
	if len(candidates) != 1 {
		return Edge{}, false
	}
	return candidates[0], true
}
