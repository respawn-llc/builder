import { describe, expect, it } from "vitest";

import type { BoardColumn } from "@/api";
import type { BoardCardDragPayload } from "./BoardDragTypes";
import { classifyDrop } from "./BoardDropActions";

describe("classifyDrop", () => {
  it("routes a same-current destination through authoritative preview", () => {
    expect(
      classifyDrop(
        baseColumn,
        { ...baseDragPayload, activeNodeIDs: ["node-target"] },
        undefined,
      ),
    ).toEqual({ kind: "move" });
  });

  it("rejects source-less moves into join columns", () => {
    expect(
      classifyDrop(
        {
          ...baseColumn,
          kind: "join",
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
};
