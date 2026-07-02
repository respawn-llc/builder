import { describe, expect, it } from "vitest";
import { z } from "zod";

import { emptyWorkflowDerivedWiring, type WorkflowDefinition, type WorkflowValidation } from "../../api";
import type { WorkflowGraphEdge, WorkflowGraphNode } from "./workflowGraphLayout";
import { layoutWorkflowGraph, workflowGraphLayoutWithDraftProjection } from "./workflowGraphLayout";
import {
  workflowGraphAbsoluteNodeRect,
  workflowGraphEndpointPoint,
} from "../../test-support/workflow-editor/workflowGraphLayoutTestHelpers";

const nodeHeightSchema = z.number();

describe("layoutWorkflowGraph", () => {
  it("builds grouped workflow graph nodes and labeled edges", async () => {
    const graph = await layoutWorkflowGraph(groupedWorkflow, emptyValidation);

    expect(nodeByID(graph.nodes, "workflow-group-group-1")?.type).toBe("workflowGroup");
    expect(nodeByID(graph.nodes, "node-1")).toMatchObject({
      parentId: "workflow-group-group-1",
      type: "workflowNode",
    });
    expect(nodeByID(graph.nodes, "done")?.type).toBe("workflowNode");
    expect(edgeByID(graph.edges, "edge-1")).toMatchObject({
      source: "node-1",
      target: "done",
      data: { label: "Done", hasError: false },
      markerEnd: { type: "arrowclosed" },
    });
  });

  it("lays out grouped cross-boundary edges as node-to-node edges", async () => {
    const graph = await layoutWorkflowGraph(crossBoundaryWorkflow, emptyValidation);

    expect(nodeByID(graph.nodes, "node-source")).toMatchObject({
      parentId: "workflow-group-group-source",
      type: "workflowNode",
    });
    expect(nodeByID(graph.nodes, "node-target")).toMatchObject({
      parentId: "workflow-group-group-target",
      type: "workflowNode",
    });
    expect(edgeByID(graph.edges, "edge-cross")).toMatchObject({
      source: "node-source",
      target: "node-target",
      type: "workflow",
    });
    expect(edgeByID(graph.edges, "edge-exit")).toMatchObject({
      source: "node-target",
      target: "done",
      type: "workflow",
    });
  });

  it("marks validation errors and disambiguates fan-out labels", async () => {
    const graph = await layoutWorkflowGraph(fanoutWorkflow, {
      valid: false,
      errors: [
        {
          code: "workflow.validation.invalid",
          details: { fieldName: "", inputName: "", placeholder: "", providerEdgeID: "" },
          message: "Invalid",
          workflowID: "workflow-1",
          nodeID: "node-1",
          transitionGroupID: "tg-split",
          edgeID: "edge-a",
          relatedIDs: ["node-a"],
          blocksContext: true,
        },
      ],
    });

    expect(graph.nodes.find((node) => node.id === "node-1")?.data.hasError).toBe(true);
    expect(graph.nodes.find((node) => node.id === "node-a")?.data.hasError).toBe(true);
    expect(edgeByID(graph.edges, "edge-a")?.data).toMatchObject({ label: "Split / a", hasError: true });
    expect(edgeByID(graph.edges, "edge-b")?.data).toMatchObject({ label: "Split / b", hasError: true });
  });

  it("marks provider edges from structured validation details", async () => {
    const graph = await layoutWorkflowGraph(joinWorkflow, {
      valid: false,
      errors: [
        {
          blocksContext: true,
          code: "workflow.validation.invalid_join_input_provider",
          details: { fieldName: "", inputName: "summary", placeholder: "", providerEdgeID: "edge-join-b" },
          edgeID: "",
          message: "join input provider must reference an incoming edge into the join",
          nodeID: "join",
          relatedIDs: [],
          transitionGroupID: "",
          workflowID: "workflow-1",
        },
      ],
    });

    expect(edgeByID(graph.edges, "edge-join-b")?.data).toMatchObject({ hasError: true });
    expect(edgeByID(graph.edges, "edge-join-a")?.data).toMatchObject({ hasError: false });
  });

  it("renders join nodes as inspectable merge diamonds with raw join edges", async () => {
    const graph = await layoutWorkflowGraph(joinWorkflow, emptyValidation);
    const joinOutgoingRoutePoints = edgeByID(graph.edges, "edge-join-synth")?.data?.routePoints ?? [];

    expect(nodeByID(graph.nodes, "join")).toMatchObject({
      type: "workflowJoin",
      data: { entityID: "join", entityKind: "node", kind: "join" },
      style: { width: 56, height: 56 },
    });
    expect(edgeByID(graph.edges, "edge-join-a")).toMatchObject({
      markerEnd: { type: "arrowclosed" },
      source: "node-a",
      target: "join",
      type: "workflow",
    });
    expect(edgeByID(graph.edges, "edge-join-b")).toMatchObject({
      markerEnd: { type: "arrowclosed" },
    });
    expect(edgeByID(graph.edges, "edge-join-synth")).toMatchObject({
      markerEnd: { type: "arrowclosed" },
      source: "join",
      target: "synth",
      type: "workflow",
    });
    expect(joinOutgoingRoutePoints.length).toBeGreaterThan(2);
    expectRouteSegmentsToBeOrthogonal(joinOutgoingRoutePoints);
    expectRouteToHaveCorner(joinOutgoingRoutePoints);
  });

  it("keeps chained join hops inspectable instead of synthesizing edge chains", async () => {
    const graph = await layoutWorkflowGraph(joinChainWorkflow, emptyValidation);

    expect(nodeByID(graph.nodes, "join-a")).toMatchObject({ type: "workflowJoin" });
    expect(nodeByID(graph.nodes, "join-b")).toMatchObject({ type: "workflowJoin" });
    expect(graph.edges.map((edge) => edge.target)).toEqual(["join-a", "join-b", "synth"]);
    expect(edgeByID(graph.edges, "edge-to-join-a")).toMatchObject({
      source: "node-a",
      target: "join-a",
      type: "workflow",
    });
  });

  it("keeps the model-order mainline layered when workflow transitions loop back", async () => {
    const graph = await layoutWorkflowGraph(loopedWorkflow, emptyValidation);
    const mainlineNodeIDs = ["start", "plan", "implement", "review", "approval", "done"];
    const loopToPlanning = requireEdge(graph.edges, "edge-approval-plan");
    const loopToImplementation = requireEdge(graph.edges, "edge-review-implement");

    expectNodeXPositionsToIncrease(graph.nodes, mainlineNodeIDs);
    expect(requireNode(graph.nodes, loopToPlanning.source).position.x).toBeGreaterThan(
      requireNode(graph.nodes, loopToPlanning.target).position.x,
    );
    expect(requireNode(graph.nodes, loopToImplementation.source).position.x).toBeGreaterThan(
      requireNode(graph.nodes, loopToImplementation.target).position.x,
    );
  });

  it("keeps Main SWE-shaped branch and loop workflows wide instead of stacked into floors", async () => {
    const graph = await layoutWorkflowGraph(mainSWEWorkflow, emptyValidation);
    const mainlineNodeIDs = [
      "start",
      "architecture",
      "planning",
      "plan-review",
      "implementation",
      "workflow-group-code-review-parallel",
      "approval-gate",
      "next-agent",
    ];
    const planRejected = requireEdge(graph.edges, "edge-plan-review-planning");
    const approvalRejected = requireEdge(graph.edges, "edge-approval-planning");

    expectAbsoluteNodeXPositionsToIncrease(graph.nodes, mainlineNodeIDs);
    expect(absoluteNodeX(graph.nodes, "code-review-join")).toBeGreaterThan(
      absoluteNodeX(graph.nodes, "workflow-group-code-review-parallel"),
    );
    expect(absoluteNodeX(graph.nodes, planRejected.source)).toBeGreaterThan(absoluteNodeX(graph.nodes, planRejected.target));
    expect(absoluteNodeX(graph.nodes, approvalRejected.source)).toBeGreaterThan(absoluteNodeX(graph.nodes, approvalRejected.target));
  });

  it("routes transition endpoints away from the reserved creation handle slot", async () => {
    const graph = await layoutWorkflowGraph(singleTransitionWorkflow, emptyValidation);
    const edge = edgeByID(graph.edges, "edge-source-target");
    const source = nodeByID(graph.nodes, "node-source");
    const target = nodeByID(graph.nodes, "node-target");

    expect(edge?.data?.routePoints.at(0)?.y).not.toBe(nodeCenterY(source));
    expect(edge?.data?.routePoints.at(-1)?.y).not.toBe(nodeCenterY(target));
  });

  it("uses deterministic separate endpoint slots for multiple outgoing transitions", async () => {
    const graph = await layoutWorkflowGraph(twoOutgoingTransitionWorkflow, emptyValidation);
    const source = nodeByID(graph.nodes, "node-source");
    const firstStart = edgeByID(graph.edges, "edge-source-a")?.data?.routePoints.at(0);
    const secondStart = edgeByID(graph.edges, "edge-source-b")?.data?.routePoints.at(0);

    expect(firstStart?.y).not.toBe(nodeCenterY(source));
    expect(secondStart?.y).not.toBe(nodeCenterY(source));
    expect(firstStart?.y).not.toBe(secondStart?.y);
  });

  it("exposes matching React Flow handles for routed transition endpoint slots", async () => {
    const graph = await layoutWorkflowGraph(singleTransitionWorkflow, emptyValidation);
    const edge = requireEdge(graph.edges, "edge-source-target");
    const source = requireNode(graph.nodes, "node-source");
    const target = requireNode(graph.nodes, "node-target");

    assertEndpointHandle(edge, source, "source", graph.nodes);
    assertEndpointHandle(edge, target, "target", graph.nodes);
  });

  it("preserves endpoint slots for aligned join routes", async () => {
    const graph = await layoutWorkflowGraph(alignedJoinWorkflow, emptyValidation);
    const internalBranch = requireEdge(graph.edges, "edge-internal-join");
    const externalBranch = requireEdge(graph.edges, "edge-external-join");
    const joinBranch = requireEdge(graph.edges, "edge-join-synth");
    const internal = requireNode(graph.nodes, "node-a");
    const join = requireNode(graph.nodes, "join");

    assertEndpointHandle(internalBranch, internal, "source", graph.nodes);
    assertEndpointHandle(internalBranch, join, "target", graph.nodes);
    assertEndpointHandle(externalBranch, join, "target", graph.nodes);
    assertEndpointHandle(joinBranch, join, "source", graph.nodes);
  });
});

describe("workflowGraphLayoutWithDraftProjection", () => {
  it("keeps a node whose draft group membership is unchanged", async () => {
    const layout = await layoutWorkflowGraph(groupedWorkflow, emptyValidation);

    const projected = workflowGraphLayoutWithDraftProjection(layout, groupedWorkflow, emptyValidation);

    expect(nodeByID(projected.nodes, "node-1")).toBeDefined();
  });

  it("drops a node whose draft group membership changed until the next layout", async () => {
    const layout = await layoutWorkflowGraph(groupedWorkflow, emptyValidation);
    expect(nodeByID(layout.nodes, "node-1")?.parentId).toBe("workflow-group-group-1");
    const draft: WorkflowDefinition = {
      ...groupedWorkflow,
      nodeGroups: groupedWorkflow.nodeGroups.map((group) => ({ ...group, nodeIDs: [] })),
      nodes: [workflowNode("node-1", "Implement", "agent", ""), workflowNode("done", "Done", "terminal", "")],
    };

    const projected = workflowGraphLayoutWithDraftProjection(layout, draft, emptyValidation);

    expect(nodeByID(projected.nodes, "node-1")).toBeUndefined();
  });

  it("reconnects a re-targeted edge to the draft endpoints before the next layout", async () => {
    const layout = await layoutWorkflowGraph(reconnectWorkflow, emptyValidation);
    expect(edgeByID(layout.edges, "edge-x")).toMatchObject({ source: "node-source", target: "node-a" });
    const draft: WorkflowDefinition = {
      ...reconnectWorkflow,
      edges: [
        workflowEdge({ id: "edge-x", key: "a", targetNodeID: "node-b", transitionGroupID: "tg-source" }),
      ],
    };

    const projected = workflowGraphLayoutWithDraftProjection(layout, draft, emptyValidation);

    expect(edgeByID(projected.edges, "edge-x")).toMatchObject({ source: "node-source", target: "node-b" });
  });

  it("keeps a grouped join node whose draft group membership is unchanged", async () => {
    const layout = await layoutWorkflowGraph(alignedJoinWorkflow, emptyValidation);

    const projected = workflowGraphLayoutWithDraftProjection(layout, alignedJoinWorkflow, emptyValidation);

    expect(nodeByID(projected.nodes, "join")).toBeDefined();
  });
});

function nodeByID(nodes: readonly WorkflowGraphNode[], id: string): WorkflowGraphNode | undefined {
  return nodes.find((node) => node.id === id);
}

function edgeByID(edges: readonly WorkflowGraphEdge[], id: string): WorkflowGraphEdge | undefined {
  return edges.find((edge) => edge.id === id);
}

function requireNode(nodes: readonly WorkflowGraphNode[], id: string): WorkflowGraphNode {
  const node = nodeByID(nodes, id);
  if (node === undefined) {
    throw new Error(`Expected graph node ${id}.`);
  }
  return node;
}

function requireEdge(edges: readonly WorkflowGraphEdge[], id: string): WorkflowGraphEdge {
  const edge = edgeByID(edges, id);
  if (edge === undefined) {
    throw new Error(`Expected graph edge ${id}.`);
  }
  return edge;
}

function nodeCenterY(node: WorkflowGraphNode | undefined): number | undefined {
  if (node === undefined) {
    return undefined;
  }
  const parsedHeight = nodeHeightSchema.safeParse(node.style?.height);
  const height = parsedHeight.success ? parsedHeight.data : Number(node.style?.height);
  return node.position.y + height / 2;
}

function assertEndpointHandle(
  edge: WorkflowGraphEdge,
  node: WorkflowGraphNode,
  side: "source" | "target",
  nodes: readonly WorkflowGraphNode[],
): void {
  const handle = side === "source" ? edge.sourceHandle : edge.targetHandle;
  const point = edge.data?.routePoints.at(side === "source" ? 0 : -1);
  expect(point?.y).toBe(workflowGraphEndpointPoint(node, handle, side, nodes).y);
}

function expectNodeXPositionsToIncrease(nodes: readonly WorkflowGraphNode[], nodeIDs: readonly string[]): void {
  for (const [index, nodeID] of nodeIDs.entries()) {
    const previousID = nodeIDs[index - 1];
    if (previousID !== undefined) {
      expect(requireNode(nodes, nodeID).position.x).toBeGreaterThan(requireNode(nodes, previousID).position.x);
    }
  }
}

function expectAbsoluteNodeXPositionsToIncrease(nodes: readonly WorkflowGraphNode[], nodeIDs: readonly string[]): void {
  for (const [index, nodeID] of nodeIDs.entries()) {
    const previousID = nodeIDs[index - 1];
    if (previousID !== undefined) {
      expect(absoluteNodeX(nodes, nodeID), `${previousID} -> ${nodeID}`).toBeGreaterThan(
        absoluteNodeX(nodes, previousID),
      );
    }
  }
}

function absoluteNodeX(nodes: readonly WorkflowGraphNode[], nodeID: string): number {
  return workflowGraphAbsoluteNodeRect(requireNode(nodes, nodeID), nodes).x;
}

function expectRouteSegmentsToBeOrthogonal(
  points: readonly Readonly<{ x: number; y: number }>[],
): void {
  for (const [index, point] of points.entries()) {
    const previous = points[index - 1];
    if (previous !== undefined) {
      expect(point.x === previous.x || point.y === previous.y).toBe(true);
    }
  }
}

function expectRouteToHaveCorner(points: readonly Readonly<{ x: number; y: number }>[]): void {
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

const groupedWorkflow: WorkflowDefinition = {
  workflow: { id: "workflow-1", name: "Delivery", description: "", version: 1 },
  derivedWiring: emptyWorkflowDerivedWiring,
  nodeGroups: [
    {
      id: "group-1",
      workflowID: "workflow-1",
      key: "core",
      name: "Core",
      sortOrder: 1,
      nodeIDs: ["node-1"],
    },
  ],
  nodes: [
    workflowNode("node-1", "Implement", "agent", "group-1"),
    workflowNode("done", "Done", "terminal", ""),
  ],
  transitionGroups: [workflowTransitionGroup("tg-1", "node-1", "done", "Done")],
  edges: [
    {
      id: "edge-1",
      workflowID: "workflow-1",
      transitionGroupID: "tg-1",
      key: "done",
      targetNodeID: "done",
      requiresApproval: false,
      contextMode: "new_session",
      contextSource: { kind: "immediate_source", nodeKey: "" },
      inputBindings: [],
      outputRequirements: [],
      parameters: [],
      promptTemplate: "",
    },
  ],
};

const fanoutWorkflow: WorkflowDefinition = {
  workflow: { id: "workflow-1", name: "Delivery", description: "", version: 1 },
  derivedWiring: emptyWorkflowDerivedWiring,
  nodeGroups: [],
  nodes: [
    workflowNode("node-1", "Plan", "agent", ""),
    workflowNode("node-a", "A", "agent", ""),
    workflowNode("node-b", "B", "agent", ""),
  ],
  transitionGroups: [workflowTransitionGroup("tg-split", "node-1", "split", "Split")],
  edges: [
    workflowEdge({ id: "edge-a", key: "a", targetNodeID: "node-a", transitionGroupID: "tg-split" }),
    workflowEdge({ id: "edge-b", key: "b", targetNodeID: "node-b", transitionGroupID: "tg-split" }),
  ],
};

const singleTransitionWorkflow: WorkflowDefinition = {
  workflow: { id: "workflow-1", name: "Delivery", description: "", version: 1 },
  derivedWiring: emptyWorkflowDerivedWiring,
  nodeGroups: [],
  nodes: [
    workflowNode("node-source", "Source", "agent", ""),
    workflowNode("node-target", "Target", "agent", ""),
  ],
  transitionGroups: [workflowTransitionGroup("tg-source-target", "node-source", "target", "Target")],
  edges: [
    workflowEdge({
      id: "edge-source-target",
      key: "target",
      targetNodeID: "node-target",
      transitionGroupID: "tg-source-target",
    }),
  ],
};

const twoOutgoingTransitionWorkflow: WorkflowDefinition = {
  workflow: { id: "workflow-1", name: "Delivery", description: "", version: 1 },
  derivedWiring: emptyWorkflowDerivedWiring,
  nodeGroups: [],
  nodes: [
    workflowNode("node-source", "Source", "agent", ""),
    workflowNode("node-a", "A", "agent", ""),
    workflowNode("node-b", "B", "agent", ""),
  ],
  transitionGroups: [
    workflowTransitionGroup("tg-source-a", "node-source", "a", "A"),
    workflowTransitionGroup("tg-source-b", "node-source", "b", "B"),
  ],
  edges: [
    workflowEdge({
      id: "edge-source-a",
      key: "a",
      targetNodeID: "node-a",
      transitionGroupID: "tg-source-a",
    }),
    workflowEdge({
      id: "edge-source-b",
      key: "b",
      targetNodeID: "node-b",
      transitionGroupID: "tg-source-b",
    }),
  ],
};

const reconnectWorkflow: WorkflowDefinition = {
  workflow: { id: "workflow-1", name: "Delivery", description: "", version: 1 },
  derivedWiring: emptyWorkflowDerivedWiring,
  nodeGroups: [],
  nodes: [
    workflowNode("node-source", "Source", "agent", ""),
    workflowNode("node-a", "A", "agent", ""),
    workflowNode("node-b", "B", "agent", ""),
  ],
  transitionGroups: [workflowTransitionGroup("tg-source", "node-source", "a", "A")],
  edges: [workflowEdge({ id: "edge-x", key: "a", targetNodeID: "node-a", transitionGroupID: "tg-source" })],
};

const crossBoundaryWorkflow: WorkflowDefinition = {
  workflow: { id: "workflow-1", name: "Delivery", description: "", version: 1 },
  derivedWiring: emptyWorkflowDerivedWiring,
  nodeGroups: [
    {
      id: "group-source",
      workflowID: "workflow-1",
      key: "source",
      name: "Source",
      sortOrder: 1,
      nodeIDs: ["node-source"],
    },
    {
      id: "group-target",
      workflowID: "workflow-1",
      key: "target",
      name: "Target",
      sortOrder: 2,
      nodeIDs: ["node-target"],
    },
  ],
  nodes: [
    workflowNode("node-source", "Source", "agent", "group-source"),
    workflowNode("node-target", "Target", "agent", "group-target"),
    workflowNode("done", "Done", "terminal", ""),
  ],
  transitionGroups: [
    workflowTransitionGroup("tg-cross", "node-source", "cross", "Cross"),
    workflowTransitionGroup("tg-exit", "node-target", "exit", "Exit"),
  ],
  edges: [
    workflowEdge({
      id: "edge-cross",
      key: "cross",
      targetNodeID: "node-target",
      transitionGroupID: "tg-cross",
    }),
    workflowEdge({ id: "edge-exit", key: "exit", targetNodeID: "done", transitionGroupID: "tg-exit" }),
  ],
};

const alignedJoinWorkflow: WorkflowDefinition = {
  workflow: { id: "workflow-1", name: "Delivery", description: "", version: 1 },
  derivedWiring: emptyWorkflowDerivedWiring,
  nodeGroups: [
    {
      id: "group-join",
      workflowID: "workflow-1",
      key: "core",
      name: "Core",
      sortOrder: 1,
      nodeIDs: ["node-a", "join"],
    },
  ],
  nodes: [
    workflowNode("node-a", "A", "agent", "group-join"),
    workflowNode("external", "External", "agent", ""),
    workflowNode("join", "Join", "join", "group-join"),
    workflowNode("synth", "Synthesize", "agent", ""),
  ],
  transitionGroups: [
    workflowTransitionGroup("tg-internal-join", "node-a", "join", "Join"),
    workflowTransitionGroup("tg-external-join", "external", "join", "Join"),
    workflowTransitionGroup("tg-join-synth", "join", "synth", "Synthesize"),
  ],
  edges: [
    workflowEdge({
      id: "edge-internal-join",
      key: "internal",
      targetNodeID: "join",
      transitionGroupID: "tg-internal-join",
    }),
    workflowEdge({
      id: "edge-external-join",
      key: "external",
      targetNodeID: "join",
      transitionGroupID: "tg-external-join",
    }),
    workflowEdge({
      id: "edge-join-synth",
      key: "synth",
      targetNodeID: "synth",
      transitionGroupID: "tg-join-synth",
    }),
  ],
};

const joinWorkflow: WorkflowDefinition = {
  workflow: { id: "workflow-1", name: "Delivery", description: "", version: 1 },
  derivedWiring: emptyWorkflowDerivedWiring,
  nodeGroups: [],
  nodes: [
    workflowNode("node-a", "A", "agent", ""),
    workflowNode("node-b", "B", "agent", ""),
    workflowNode("join", "Join", "join", ""),
    workflowNode("synth", "Synthesize", "agent", ""),
  ],
  transitionGroups: [
    workflowTransitionGroup("tg-join-a", "node-a", "join", "Join"),
    workflowTransitionGroup("tg-join-b", "node-b", "join", "Join"),
    workflowTransitionGroup("tg-join-synth", "join", "done", "Done"),
  ],
  edges: [
    workflowEdge({
      contextMode: "continue_session",
      id: "edge-join-a",
      key: "join_a",
      targetNodeID: "join",
      transitionGroupID: "tg-join-a",
    }),
    workflowEdge({
      contextMode: "compact_and_continue_session",
      id: "edge-join-b",
      key: "join_b",
      targetNodeID: "join",
      transitionGroupID: "tg-join-b",
    }),
    workflowEdge({
      contextMode: "new_session",
      id: "edge-join-synth",
      key: "synth",
      targetNodeID: "synth",
      transitionGroupID: "tg-join-synth",
    }),
  ],
};

const joinChainWorkflow: WorkflowDefinition = {
  workflow: { id: "workflow-1", name: "Delivery", description: "", version: 1 },
  derivedWiring: emptyWorkflowDerivedWiring,
  nodeGroups: [],
  nodes: [
    workflowNode("node-a", "A", "agent", ""),
    workflowNode("join-a", "Join A", "join", ""),
    workflowNode("join-b", "Join B", "join", ""),
    workflowNode("synth", "Synthesize", "agent", ""),
  ],
  transitionGroups: [
    workflowTransitionGroup("tg-to-join-a", "node-a", "join", "Join"),
    workflowTransitionGroup("tg-join-a-join-b", "join-a", "join", "Join"),
    workflowTransitionGroup("tg-join-b-synth", "join-b", "done", "Done"),
  ],
  edges: [
    workflowEdge({
      id: "edge-to-join-a",
      key: "join_a",
      targetNodeID: "join-a",
      transitionGroupID: "tg-to-join-a",
    }),
    workflowEdge({
      id: "edge-join-a-join-b",
      key: "join_b",
      targetNodeID: "join-b",
      transitionGroupID: "tg-join-a-join-b",
    }),
    workflowEdge({
      id: "edge-join-b-synth",
      key: "synth",
      targetNodeID: "synth",
      transitionGroupID: "tg-join-b-synth",
    }),
  ],
};

const loopedWorkflow: WorkflowDefinition = {
  workflow: { id: "workflow-1", name: "Looped Delivery", description: "", version: 1 },
  derivedWiring: emptyWorkflowDerivedWiring,
  nodeGroups: [],
  nodes: [
    workflowNode("start", "Start", "start", ""),
    workflowNode("plan", "Plan", "agent", ""),
    workflowNode("implement", "Implement", "agent", ""),
    workflowNode("review", "Review", "agent", ""),
    workflowNode("approval", "Approval", "agent", ""),
    workflowNode("done", "Done", "terminal", ""),
  ],
  transitionGroups: [
    workflowTransitionGroup("tg-start-plan", "start", "plan", "Plan"),
    workflowTransitionGroup("tg-plan-implement", "plan", "implement", "Implement"),
    workflowTransitionGroup("tg-implement-review", "implement", "review", "Review"),
    workflowTransitionGroup("tg-review-approval", "review", "approval", "Approval"),
    workflowTransitionGroup("tg-approval-done", "approval", "done", "Done"),
    workflowTransitionGroup("tg-approval-plan", "approval", "rejected", "Rejected"),
    workflowTransitionGroup("tg-review-implement", "review", "rework", "Rework"),
  ],
  edges: [
    workflowEdge({ id: "edge-start-plan", key: "plan", targetNodeID: "plan", transitionGroupID: "tg-start-plan" }),
    workflowEdge({
      id: "edge-plan-implement",
      key: "implement",
      targetNodeID: "implement",
      transitionGroupID: "tg-plan-implement",
    }),
    workflowEdge({
      id: "edge-implement-review",
      key: "review",
      targetNodeID: "review",
      transitionGroupID: "tg-implement-review",
    }),
    workflowEdge({
      id: "edge-review-approval",
      key: "approval",
      targetNodeID: "approval",
      transitionGroupID: "tg-review-approval",
    }),
    workflowEdge({ id: "edge-approval-done", key: "done", targetNodeID: "done", transitionGroupID: "tg-approval-done" }),
    workflowEdge({
      id: "edge-approval-plan",
      key: "rejected",
      targetNodeID: "plan",
      transitionGroupID: "tg-approval-plan",
    }),
    workflowEdge({
      id: "edge-review-implement",
      key: "rework",
      targetNodeID: "implement",
      transitionGroupID: "tg-review-implement",
    }),
  ],
};

const mainSWEWorkflow: WorkflowDefinition = {
  workflow: { id: "workflow-main-swe", name: "Main SWE", description: "", version: 1 },
  derivedWiring: emptyWorkflowDerivedWiring,
  nodeGroups: [
    {
      id: "code-review-parallel",
      workflowID: "workflow-main-swe",
      key: "code_review_parallel",
      name: "Code review parallel",
      sortOrder: 1,
      nodeIDs: ["code-review", "qa", "code-review-join"],
    },
  ],
  nodes: [
    workflowNode("start", "Start", "start", ""),
    workflowNode("architecture", "Architecture", "agent", ""),
    workflowNode("planning", "Planning", "agent", ""),
    workflowNode("plan-review", "Plan Review", "agent", ""),
    workflowNode("implementation", "Implementation", "agent", ""),
    workflowNode("code-review", "Code review", "agent", "code-review-parallel"),
    workflowNode("qa", "QA", "agent", "code-review-parallel"),
    workflowNode("code-review-join", "Code review join", "join", "code-review-parallel"),
    workflowNode("approval-gate", "Approval gate", "agent", ""),
    workflowNode("next-agent", "New agent", "agent", ""),
  ],
  transitionGroups: [
    workflowTransitionGroup("tg-start-architecture", "start", "architect", "Architect"),
    workflowTransitionGroup("tg-architecture-planning", "architecture", "plan", "Plan"),
    workflowTransitionGroup("tg-planning-plan-review", "planning", "review_plan", "Plan Review"),
    workflowTransitionGroup("tg-plan-review-planning", "plan-review", "plan_rejected", "Plan Rejected"),
    workflowTransitionGroup("tg-plan-review-implementation", "plan-review", "plan_approved", "Plan Approved"),
    workflowTransitionGroup("tg-implementation-code-review", "implementation", "code_review", "Code review"),
    workflowTransitionGroup("tg-code-review-join", "code-review", "join", "Code review join"),
    workflowTransitionGroup("tg-qa-join", "qa", "join", "Code review join"),
    workflowTransitionGroup("tg-join-approval", "code-review-join", "new_agent", "New agent"),
    workflowTransitionGroup("tg-approval-planning", "approval-gate", "rejected", "Rejected"),
    workflowTransitionGroup("tg-approval-next-agent", "approval-gate", "approved", "Approved"),
  ],
  edges: [
    workflowEdge({
      id: "edge-start-architecture",
      key: "architect",
      targetNodeID: "architecture",
      transitionGroupID: "tg-start-architecture",
    }),
    workflowEdge({
      id: "edge-architecture-planning",
      key: "plan",
      targetNodeID: "planning",
      transitionGroupID: "tg-architecture-planning",
    }),
    workflowEdge({
      id: "edge-planning-plan-review",
      key: "plan_review",
      targetNodeID: "plan-review",
      transitionGroupID: "tg-planning-plan-review",
    }),
    workflowEdge({
      id: "edge-plan-review-planning",
      key: "plan_rejected",
      targetNodeID: "planning",
      transitionGroupID: "tg-plan-review-planning",
    }),
    workflowEdge({
      id: "edge-plan-review-implementation",
      key: "plan_approved",
      targetNodeID: "implementation",
      transitionGroupID: "tg-plan-review-implementation",
    }),
    workflowEdge({
      id: "edge-implementation-code-review",
      key: "code_review",
      targetNodeID: "code-review",
      transitionGroupID: "tg-implementation-code-review",
    }),
    workflowEdge({
      id: "edge-implementation-qa",
      key: "qa",
      targetNodeID: "qa",
      transitionGroupID: "tg-implementation-code-review",
    }),
    workflowEdge({
      id: "edge-code-review-join",
      key: "code_review",
      targetNodeID: "code-review-join",
      transitionGroupID: "tg-code-review-join",
    }),
    workflowEdge({
      id: "edge-qa-join",
      key: "qa",
      targetNodeID: "code-review-join",
      transitionGroupID: "tg-qa-join",
    }),
    workflowEdge({
      id: "edge-join-approval",
      key: "new_agent",
      targetNodeID: "approval-gate",
      transitionGroupID: "tg-join-approval",
    }),
    workflowEdge({
      id: "edge-approval-planning",
      key: "rejected",
      targetNodeID: "planning",
      transitionGroupID: "tg-approval-planning",
    }),
    workflowEdge({
      id: "edge-approval-next-agent",
      key: "approved",
      targetNodeID: "next-agent",
      transitionGroupID: "tg-approval-next-agent",
    }),
  ],
};

function workflowNode(id: string, name: string, kind: string, groupID: string) {
  return {
    id,
    workflowID: "workflow-1",
    key: id,
    kind,
    name,
    groupID,
    groupKey: groupID.length > 0 ? "core" : "",
    subagentRole: kind === "agent" ? "coder" : "",
    promptTemplate: "",
    inputFields: [],
    joinInputProviders: [],
    outputFields: [],
  };
}

function workflowTransitionGroup(id: string, sourceNodeID: string, transitionID: string, name: string) {
  return { description: "", id, workflowID: "workflow-1", sourceNodeID, transitionID, name };
}

function workflowEdge({
  contextMode = "new_session",
  id,
  key,
  targetNodeID,
  transitionGroupID,
}: Readonly<{
  contextMode?: string;
  id: string;
  key: string;
  targetNodeID: string;
  transitionGroupID: string;
}>) {
  return {
    id,
    workflowID: "workflow-1",
    transitionGroupID,
    key,
    targetNodeID,
    requiresApproval: false,
    contextMode,
    contextSource: { kind: "immediate_source", nodeKey: "" },
    inputBindings: [],
    outputRequirements: [],
    parameters: [],
    promptTemplate: "",
  };
}
