import { describe, expect, it } from "vitest";

import type { BoardColumn } from "../../api";
import type { BoardCardDragPayload } from "./BoardDragTypes";
import { classifyDrop } from "./BoardDropActions";

describe("classifyDrop", () => {
  it("does not force target-union output fields onto allowed manual moves", () => {
    expect(
      classifyDrop(
        {
          ...baseColumn,
          id: "node-review",
          transitionOutputFields: [{ name: "summary", description: "Summary" }],
        },
        { ...baseDragPayload, manualMoveTargetNodeIDs: ["node-review"] },
        undefined,
      ),
    ).toEqual({ kind: "move" });
  });

  it("rejects source-less moves into join columns", () => {
    expect(
      classifyDrop(
        { ...baseColumn, kind: "join", transitionOutputFields: [{ name: "summary", description: "Summary" }] },
        { ...baseDragPayload, activeNodeIDs: [], statusKind: "backlog" },
        undefined,
      ),
    ).toEqual({ kind: "reject" });
  });

  it("allows explicit manual targets for join columns", () => {
    expect(
      classifyDrop(
        { ...baseColumn, id: "node-join", kind: "join" },
        { ...baseDragPayload, activeNodeIDs: [], manualMoveTargetNodeIDs: ["node-join"], statusKind: "backlog" },
        undefined,
      ),
    ).toEqual({ kind: "move" });
  });

  it("moves terminal targets without collecting transition output values", () => {
    expect(
      classifyDrop(
        {
          ...baseColumn,
          id: "node-terminal",
          isDone: true,
          kind: "terminal",
          transitionOutputFields: [{ name: "summary", description: "Summary" }],
        },
        { ...baseDragPayload, manualMoveTargetNodeIDs: [] },
        undefined,
      ),
    ).toEqual({ kind: "move", allowMissingEdge: true });
  });

  it("collects transition output values for non-terminal targets", () => {
    expect(
      classifyDrop(
        {
          ...baseColumn,
          id: "node-review",
          isDone: false,
          kind: "agent",
          transitionOutputFields: [{ name: "summary", description: "Summary" }],
        },
        { ...baseDragPayload, manualMoveTargetNodeIDs: [] },
        undefined,
      ),
    ).toEqual({ kind: "missingInput" });
  });

  it("auto-approves script targets like other executable automation targets", () => {
    expect(
      classifyDrop(
        {
          ...baseColumn,
          kind: "script",
        },
        { ...baseDragPayload, manualMoveTargetNodeIDs: [] },
        undefined,
      ),
    ).toEqual({ kind: "move", allowMissingEdge: true, autoApprove: true });
  });
});

const baseDragPayload: BoardCardDragPayload = {
  activeNodeIDs: ["node-current"],
  canStart: false,
  manualMoveTargetNodeIDs: [],
  statusKind: "active",
  taskID: "task-1",
};

const baseColumn: BoardColumn = {
  assigneeRole: "",
  groupID: "",
  id: "node-target",
  isBacklog: false,
  isDone: false,
  key: "target",
  kind: "agent",
  name: "Target",
  outputFields: [],
  sortOrder: 0,
  taskCount: 0,
  transitionOutputFields: [],
};
