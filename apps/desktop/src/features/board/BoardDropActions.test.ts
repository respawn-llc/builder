import { describe, expect, it } from "vitest";

import type { BoardColumn } from "@/api";
import type { BoardCardDragPayload } from "./BoardDragTypes";
import { classifyDrop } from "./BoardDropActions";

describe("classifyDrop", () => {
  it("rejects source-less moves into join columns", () => {
    expect(
      classifyDrop(
        {
          ...baseColumn,
          kind: "join",
          transitionOutputFields: [{ name: "summary", description: "Summary" }],
        },
        { ...baseDragPayload, activeNodeIDs: [], statusKind: "backlog" },
        undefined,
      ),
    ).toEqual({ kind: "reject" });
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
