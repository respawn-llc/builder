import { describe, expect, it } from "vitest";

import {
  defaultWorkflowExecutionTargetPolicy,
  emptyWorkflowDerivedWiring,
  type WorkflowDefinition,
  type WorkflowValidation,
} from "../../api";
import type { WorkflowGraphEdge, WorkflowGraphNode, WorkflowGraphPoint } from "./workflowGraphLayout";
import { layoutWorkflowGraph } from "./workflowGraphLayout";
import {
  workflowGraphAbsoluteNodeRect,
  workflowGraphEndpointPoint,
} from "../../test-support/workflow-editor/workflowGraphLayoutTestHelpers";

describe("layoutWorkflowGraph node group bounds", () => {
  it("keeps unrelated nodes outside populated node group bounds", async () => {
    const graph = await layoutWorkflowGraph(threeBranchGroupWorkflow, emptyValidation);
    const group = requiredNodeByID(graph.nodes, "workflow-group-group-1");
    const unrelatedNodes = ["start", "done"].map((id) => requiredNodeByID(graph.nodes, id));

    for (const node of unrelatedNodes) {
      expect(rectsOverlap(group, node)).toBe(false);
    }
  });

  it("keeps the group Join outside and vertically centered to the right of the group island", async () => {
    const graph = await layoutWorkflowGraph(threeBranchGroupWorkflow, emptyValidation);
    const group = requiredNodeByID(graph.nodes, "workflow-group-group-1");
    const join = requiredNodeByID(graph.nodes, "join");

    expect(join.parentId).toBeUndefined();
    expect(requiredNodeByID(graph.nodes, "node-a").parentId).toBe(group.id);
    expect(requiredNodeByID(graph.nodes, "node-b").parentId).toBe(group.id);
    expect(requiredNodeByID(graph.nodes, "node-c").parentId).toBe(group.id);
    expect(rectRight(group)).toBeLessThan(join.position.x);
    expect(rectCenterY(join)).toBeCloseTo(rectCenterY(group), 6);
    expect(rectsOverlap(group, join)).toBe(false);
  });

  it("routes group branch endpoints through deterministic Join ports instead of stale in-group coordinates", async () => {
    const graph = await layoutWorkflowGraph(threeBranchGroupJoinWorkflow, emptyValidation);
    const group = requiredNodeByID(graph.nodes, "workflow-group-group-1");
    const branch = requiredNodeByID(graph.nodes, "node-a");
    const join = requiredNodeByID(graph.nodes, "join");
    const edge = requiredEdgeByID(graph.edges, "edge-a-join");
    const outgoingEdge = requiredEdgeByID(graph.edges, "edge-join-done");
    const branchRect = workflowGraphAbsoluteNodeRect(branch, graph.nodes);
    const joinRect = workflowGraphAbsoluteNodeRect(join, graph.nodes);
    const points = requiredRoutePoints(edge);
    const outgoingPoints = requiredRoutePoints(outgoingEdge);

    expectPointCloseTo(points[0], workflowGraphEndpointPoint(branch, edge.sourceHandle, "source", graph.nodes));
    expectPointCloseTo(points[points.length - 1], workflowGraphEndpointPoint(join, edge.targetHandle, "target", graph.nodes));
    expectPointCloseTo(outgoingPoints[0], workflowGraphEndpointPoint(join, outgoingEdge.sourceHandle, "source", graph.nodes));
    expect(outgoingPoints.length).toBeGreaterThan(2);
    expectRouteSegmentsToBeOrthogonal(outgoingPoints);
    expectRouteToHaveCorner(outgoingPoints);
    expect(points.some((point) => point.x > rectRight(group) && point.x < joinRect.x)).toBe(true);
    expect(points.every((point) => point.x >= branchRect.x + branchRect.width)).toBe(true);
  });
});

function requiredNodeByID(nodes: readonly WorkflowGraphNode[], id: string): WorkflowGraphNode {
  const node = nodes.find((item) => item.id === id);
  if (node === undefined) {
    throw new Error(`Node ${id} not found`);
  }
  return node;
}

function requiredEdgeByID(edges: readonly WorkflowGraphEdge[], id: string): WorkflowGraphEdge {
  const edge = edges.find((item) => item.id === id);
  if (edge === undefined) {
    throw new Error(`Edge ${id} not found`);
  }
  return edge;
}

function requiredRoutePoints(edge: WorkflowGraphEdge): readonly WorkflowGraphPoint[] {
  const points = edge.data?.routePoints ?? [];
  if (points.length < 2) {
    throw new Error(`Edge ${edge.id} has no routed points`);
  }
  return points;
}

function rectsOverlap(left: WorkflowGraphNode, right: WorkflowGraphNode): boolean {
  const leftWidth = Number(left.style?.width ?? 0);
  const leftHeight = Number(left.style?.height ?? 0);
  const rightWidth = Number(right.style?.width ?? 0);
  const rightHeight = Number(right.style?.height ?? 0);
  return (
    left.position.x < right.position.x + rightWidth &&
    left.position.x + leftWidth > right.position.x &&
    left.position.y < right.position.y + rightHeight &&
    left.position.y + leftHeight > right.position.y
  );
}

function rectRight(rect: Readonly<{ position: Readonly<{ x: number }>; style?: WorkflowGraphNode["style"] }>): number {
  return rect.position.x + Number(rect.style?.width ?? 0);
}

function rectCenterY(
  rect: Readonly<{ height?: number; position?: Readonly<{ y: number }>; style?: WorkflowGraphNode["style"]; y?: number }>,
): number {
  return (rect.position?.y ?? rect.y ?? 0) + (rect.height ?? Number(rect.style?.height ?? 0)) / 2;
}

function expectPointCloseTo(actual: WorkflowGraphPoint | undefined, expected: WorkflowGraphPoint): void {
  if (actual === undefined) {
    throw new Error("Expected route point to exist");
  }
  expect(actual.x).toBeCloseTo(expected.x, 6);
  expect(actual.y).toBeCloseTo(expected.y, 6);
}

function expectRouteSegmentsToBeOrthogonal(points: readonly WorkflowGraphPoint[]): void {
  for (const [index, point] of points.entries()) {
    const previous = points[index - 1];
    if (previous !== undefined) {
      expect(point.x === previous.x || point.y === previous.y).toBe(true);
    }
  }
}

function expectRouteToHaveCorner(points: readonly WorkflowGraphPoint[]): void {
  expect(
    points.some((point, index) => {
      const previous = points[index - 1];
      const next = points[index + 1];
      return (
        previous !== undefined &&
        next !== undefined &&
        (previous.x - point.x) * (next.y - point.y) !== (previous.y - point.y) * (next.x - point.x)
      );
    }),
  ).toBe(true);
}

const emptyValidation: WorkflowValidation = { valid: true, errors: [] };

const threeBranchGroupWorkflow: WorkflowDefinition = {
  workflow: {
    id: "workflow-1",
    name: "Group Proof",
    description: "",
    version: 1,
    executionTargetPolicy: defaultWorkflowExecutionTargetPolicy,
  },
  derivedWiring: emptyWorkflowDerivedWiring,
  nodeGroups: [
    {
      id: "group-1",
      workflowID: "workflow-1",
      key: "parallel",
      name: "Parallel",
      sortOrder: 1,
      nodeIDs: ["node-a", "node-b", "node-c", "join"],
    },
  ],
  nodes: [
    workflowNode("start", "Backlog", "start", ""),
    workflowNode("done", "Done", "terminal", ""),
    workflowNode("node-a", "A", "agent", "group-1"),
    workflowNode("node-b", "B", "agent", "group-1"),
    workflowNode("node-c", "C", "agent", "group-1"),
    workflowNode("join", "Join", "join", "group-1"),
  ],
  transitionGroups: [],
  edges: [],
};

const threeBranchGroupJoinWorkflow: WorkflowDefinition = {
  ...threeBranchGroupWorkflow,
  transitionGroups: [
    workflowTransitionGroup("tg-a-join", "node-a", "join", "Join"),
    workflowTransitionGroup("tg-b-join", "node-b", "join", "Join"),
    workflowTransitionGroup("tg-c-join", "node-c", "join", "Join"),
    workflowTransitionGroup("tg-join-done", "join", "done", "Done"),
  ],
  edges: [
    workflowEdge("edge-a-join", "tg-a-join", "join"),
    workflowEdge("edge-b-join", "tg-b-join", "join"),
    workflowEdge("edge-c-join", "tg-c-join", "join"),
    workflowEdge("edge-join-done", "tg-join-done", "done"),
  ],
};

function workflowNode(id: string, name: string, kind: string, groupID: string) {
  return {
    groupID,
    groupKey: groupID.length > 0 ? "parallel" : "",
    id,
    inputFields: [],
    joinInputProviders: [],
    key: id,
    kind,
    name,
    outputFields: [],
    promptTemplate: "",
    subagentRole: kind === "agent" ? "coder" : "",
    workflowID: "workflow-1",
  };
}

function workflowTransitionGroup(id: string, sourceNodeID: string, transitionID: string, name: string) {
  return { description: "", id, workflowID: "workflow-1", sourceNodeID, transitionID, name };
}

function workflowEdge(id: string, transitionGroupID: string, targetNodeID: string) {
  return {
    contextMode: "new_session",
    contextSource: { kind: "immediate_source", nodeKey: "" },
    id,
    inputBindings: [],
    key: id,
    outputRequirements: [],
    parameters: [],
    promptTemplate: "",
    requiresApproval: false,
    targetNodeID,
    transitionGroupID,
    workflowID: "workflow-1",
  };
}
