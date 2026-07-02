import type { WorkflowEdge } from "../../api";
import type { DraftWorkflowDefinition, DraftWorkflowNode } from "./workflowEditorDraft";
import { uniqueWorkflowModelKey } from "./workflowEditorGraphKeys";
import {
  edgesForTransitionGroup,
  incidentEdges,
  transitionIDsForSource,
  workflowEdge,
  workflowTransitionGroup,
} from "./workflowEditorGraphMutationHelpers";
import type { InferredNodeGroupTopologyIDs } from "./workflowEditorGraphMutationTypes";

type InferredNodeGroupTopology = Readonly<{
  addedBranch: DraftWorkflowNode;
  fanoutGroupID: string;
  fanoutEdgeKeys: readonly string[];
  join: DraftWorkflowNode;
}> &
  (
    | Readonly<{
        kind: "initial";
        downstreamGroup: DraftWorkflowDefinition["transitionGroups"][number];
        existingBranch: DraftWorkflowNode;
      }>
    | Readonly<{ kind: "additional" }>
  );

export function inferNodeGroupV1Topology(
  draft: DraftWorkflowDefinition,
  addedBranchID: string,
  ids: InferredNodeGroupTopologyIDs,
): DraftWorkflowDefinition | null {
  const topology = inferNodeGroupV1TopologyFacts(draft, addedBranchID);
  return topology === null ? null : applyNodeGroupV1Topology(draft, ids, topology);
}

function inferNodeGroupV1TopologyFacts(
  draft: DraftWorkflowDefinition,
  addedBranchID: string,
): InferredNodeGroupTopology | null {
  const addedBranch = draft.nodes.find((node) => node.id === addedBranchID);
  if (addedBranch === undefined || addedBranch.groupID.length === 0) {
    return null;
  }
  if (incidentEdges(draft, addedBranch.id).length > 0) {
    return null;
  }
  const topology = inferNodeGroupMembers(draft, addedBranch);
  if (topology === null) {
    return null;
  }
  if (topology.existingBranches.length === 1) {
    const existingBranch = topology.existingBranches[0];
    if (existingBranch === undefined) {
      return null;
    }
    const fanout = inferInitialFanoutTopology(draft, existingBranch.id);
    const downstreamGroup = inferDownstreamGroup(draft, existingBranch.id, topology.join.id);
    return fanout === null || downstreamGroup === null
      ? null
      : { ...topology, ...fanout, downstreamGroup, existingBranch, kind: "initial" };
  }
  const fanout = inferExistingGroupFanoutTopology(draft, topology.existingBranches);
  return fanout === null || !hasExistingBranchJoinTopology(draft, topology.existingBranches, topology.join.id)
    ? null
    : { addedBranch, fanoutEdgeKeys: fanout.fanoutEdgeKeys, fanoutGroupID: fanout.fanoutGroupID, join: topology.join, kind: "additional" };
}

function inferNodeGroupMembers(
  draft: DraftWorkflowDefinition,
  addedBranch: DraftWorkflowNode,
): Readonly<{
  addedBranch: DraftWorkflowNode;
  existingBranches: readonly DraftWorkflowNode[];
  join: DraftWorkflowNode;
}> | null {
  const members = draft.nodes.filter((node) => node.groupID === addedBranch.groupID);
  const branches = members.filter((node) => workflowBranchNodeKind(node.kind));
  const joins = members.filter((node) => node.kind === "join");
  const existingBranches = branches.filter((node) => node.id !== addedBranch.id);
  const join = joins[0];
  return branches.length >= 2 && joins.length === 1 && existingBranches.length > 0 && join !== undefined
    ? { addedBranch, existingBranches, join }
    : null;
}

function workflowBranchNodeKind(kind: DraftWorkflowNode["kind"]): boolean {
  return kind === "agent" || kind === "script";
}

function inferInitialFanoutTopology(
  draft: DraftWorkflowDefinition,
  existingBranchID: string,
): Pick<InferredNodeGroupTopology, "fanoutGroupID" | "fanoutEdgeKeys"> | null {
  const incomingToExistingBranch = draft.edges.filter((edge) => edge.targetNodeID === existingBranchID);
  if (incomingToExistingBranch.length !== 1) {
    return null;
  }
  const fanoutGroup = draft.transitionGroups.find((group) => group.id === incomingToExistingBranch[0]?.transitionGroupID);
  const fanoutSource = draft.nodes.find((node) => node.id === fanoutGroup?.sourceNodeID);
  if (fanoutGroup === undefined || fanoutSource === undefined || fanoutSource.kind === "start") {
    return null;
  }
  const fanoutEdges = edgesForTransitionGroup(draft, fanoutGroup.id);
  return fanoutEdges.length === 1
    ? { fanoutEdgeKeys: fanoutEdges.map((edge) => edge.key), fanoutGroupID: fanoutGroup.id }
    : null;
}

function inferExistingGroupFanoutTopology(
  draft: DraftWorkflowDefinition,
  existingBranches: readonly DraftWorkflowNode[],
): Pick<InferredNodeGroupTopology, "fanoutGroupID" | "fanoutEdgeKeys"> | null {
  const incomingByBranch = existingBranches.map((branch) =>
    draft.edges.filter((edge) => edge.targetNodeID === branch.id),
  );
  if (incomingByBranch.some((incoming) => incoming.length !== 1)) {
    return null;
  }
  const fanoutGroupID = incomingByBranch[0]?.[0]?.transitionGroupID;
  if (fanoutGroupID === undefined || incomingByBranch.some((incoming) => incoming[0]?.transitionGroupID !== fanoutGroupID)) {
    return null;
  }
  const fanoutEdges = edgesForTransitionGroup(draft, fanoutGroupID);
  const existingBranchIDs = new Set(existingBranches.map((branch) => branch.id));
  const fanoutTargets = new Set(fanoutEdges.map((edge) => edge.targetNodeID));
  if (fanoutEdges.length !== existingBranchIDs.size || ![...existingBranchIDs].every((id) => fanoutTargets.has(id))) {
    return null;
  }
  return { fanoutEdgeKeys: fanoutEdges.map((edge) => edge.key), fanoutGroupID };
}

function hasExistingBranchJoinTopology(
  draft: DraftWorkflowDefinition,
  existingBranches: readonly DraftWorkflowNode[],
  joinID: string,
): boolean {
  return existingBranches.every((branch) => {
    const joinGroups = draft.transitionGroups.filter((group) => {
      if (group.sourceNodeID !== branch.id) {
        return false;
      }
      const edges = edgesForTransitionGroup(draft, group.id);
      return edges.length === 1 && edges[0]?.targetNodeID === joinID;
    });
    return joinGroups.length === 1;
  });
}

function inferDownstreamGroup(
  draft: DraftWorkflowDefinition,
  existingBranchID: string,
  joinID: string,
): DraftWorkflowDefinition["transitionGroups"][number] | null {
  const existingOutgoingGroups = draft.transitionGroups.filter((group) => group.sourceNodeID === existingBranchID);
  if (existingOutgoingGroups.length !== 1 || draft.transitionGroups.some((group) => group.sourceNodeID === joinID)) {
    return null;
  }
  const downstreamGroup = existingOutgoingGroups[0];
  if (downstreamGroup === undefined) {
    return null;
  }
  const downstreamEdges = edgesForTransitionGroup(draft, downstreamGroup.id);
  return downstreamEdges.length === 1 && downstreamEdges[0]?.targetNodeID !== joinID ? downstreamGroup : null;
}

function applyNodeGroupV1Topology(
  draft: DraftWorkflowDefinition,
  ids: InferredNodeGroupTopologyIDs,
  topology: InferredNodeGroupTopology,
): DraftWorkflowDefinition {
  return {
    ...draft,
    edges: [...nodeGroupV1TopologyExistingEdges(draft, topology), ...nodeGroupV1TopologyEdges(draft, ids, topology)],
    transitionGroups: [
      ...draft.transitionGroups.map((group) =>
        topology.kind === "initial" && group.id === topology.downstreamGroup.id
          ? { ...group, sourceNodeID: topology.join.id }
          : group,
      ),
      ...nodeGroupV1TopologyTransitionGroups(draft, ids, topology),
    ],
  };
}

function nodeGroupV1TopologyExistingEdges(
  draft: DraftWorkflowDefinition,
  topology: InferredNodeGroupTopology,
): readonly WorkflowEdge[] {
  return topology.kind === "initial"
    ? draft.edges.map((edge) =>
        edge.transitionGroupID === topology.downstreamGroup.id ? resetEdgeToNewSession(edge) : edge,
      )
    : draft.edges;
}

function resetEdgeToNewSession(edge: WorkflowEdge): WorkflowEdge {
  return {
    ...edge,
    contextMode: "new_session",
    contextSource: { kind: "immediate_source", nodeKey: "" },
  };
}

function nodeGroupV1TopologyEdges(
  draft: DraftWorkflowDefinition,
  ids: InferredNodeGroupTopologyIDs,
  topology: InferredNodeGroupTopology,
) {
  const edges = [
    workflowEdge({
      id: ids.fanoutEdgeID,
      key: uniqueWorkflowModelKey(topology.addedBranch.key, topology.fanoutEdgeKeys),
      targetNodeID: topology.addedBranch.id,
      transitionGroupID: topology.fanoutGroupID,
      workflowID: draft.workflow.id,
    }),
    workflowEdge({
      id: ids.addedBranchJoinEdgeID,
      key: topology.join.key,
      targetNodeID: topology.join.id,
      transitionGroupID: ids.addedBranchJoinTransitionGroupID,
      workflowID: draft.workflow.id,
    }),
  ];
  return topology.kind === "initial"
    ? [
        ...edges,
        workflowEdge({
          id: ids.existingBranchJoinEdgeID,
          key: topology.join.key,
          targetNodeID: topology.join.id,
          transitionGroupID: ids.existingBranchJoinTransitionGroupID,
          workflowID: draft.workflow.id,
        }),
      ]
    : edges;
}

function nodeGroupV1TopologyTransitionGroups(
  draft: DraftWorkflowDefinition,
  ids: InferredNodeGroupTopologyIDs,
  topology: InferredNodeGroupTopology,
) {
  const groups = [
    workflowTransitionGroup({
      id: ids.addedBranchJoinTransitionGroupID,
      name: topology.join.name,
      sourceNodeID: topology.addedBranch.id,
      transitionID: uniqueWorkflowModelKey(topology.join.key, transitionIDsForSource(draft, topology.addedBranch.id)),
      workflowID: draft.workflow.id,
    }),
  ];
  return topology.kind === "initial"
    ? [
        workflowTransitionGroup({
          id: ids.existingBranchJoinTransitionGroupID,
          name: topology.join.name,
          sourceNodeID: topology.existingBranch.id,
          transitionID: uniqueWorkflowModelKey(topology.join.key, transitionIDsForSource(draft, topology.existingBranch.id)),
          workflowID: draft.workflow.id,
        }),
        ...groups,
      ]
    : groups;
}
