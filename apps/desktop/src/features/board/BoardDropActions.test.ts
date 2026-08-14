import { describe, expect, it } from "vitest";

import type { BoardCard, BoardColumn, ProjectLabelCatalog } from "@/api";
import { orderedAssignedLabels } from "@/shared/labels";
import type { BoardCardDragPayload } from "./BoardDragTypes";
import { classifyDrop } from "./BoardDropActions";
import { toKanbanCardVM } from "./BoardColumnViewModel";

describe("classifyDrop", () => {
  it("routes a same-current destination through authoritative preview", () => {
    expect(
      classifyDrop(baseColumn, { ...baseDragPayload, activeNodeIDs: ["node-target"] }, undefined),
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
  groupID: null,
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

describe("toKanbanCardVM", () => {
  it("renders cached card labels in refreshed Project catalog order without refetching the card", () => {
    const alphaID = "38bf0da7-a3f7-4c15-bc5f-c8fca538e667";
    const betaID = "942495c2-5958-4959-8445-94046ad74fbd";
    const card: BoardCard = {
      actions: {
        canDelete: true,
        canInterrupt: false,
        canResume: false,
        canStart: true,
      },
      activeNodeIDs: [],
      dependencyProgress: null,
      id: "task-1",
      labelIDs: [betaID, alphaID],
      preview: { markdown: "", truncated: false },
      shortID: "KNT-1",
      sourceWorkspace: {
        availability: "available",
        id: "workspace-1",
        isPrimary: true,
        name: "Workspace",
        rootPath: "/workspace",
        updatedAt: 1,
      },
      status: {
        attentionTypes: [],
        kind: "backlog",
        nativeState: "active",
        nodeIDs: [],
      },
      title: "Task",
      updatedAt: 1,
      workflowID: "11111111-1111-4111-8111-111111111111",
    };
    const catalog: ProjectLabelCatalog = {
      projectID: "project-1",
      labels: [
        { id: alphaID, name: "Alpha" },
        { id: betaID, name: "Beta" },
      ],
    };

    expect(
      toKanbanCardVM(card, { attachedWorkspaceCount: 1, defaultWorkspaceID: "workspace-1" }, catalog).labels,
    ).toEqual([
      { id: alphaID, name: "Alpha" },
      { id: betaID, name: "Beta" },
    ]);
  });
});

describe("orderedAssignedLabels", () => {
  it("projects assigned labels in the Project catalog sequence", () => {
    const alphaID = "38bf0da7-a3f7-4c15-bc5f-c8fca538e667";
    const betaID = "942495c2-5958-4959-8445-94046ad74fbd";
    const catalog: ProjectLabelCatalog = {
      projectID: "project-1",
      labels: [
        { id: alphaID, name: "Alpha" },
        { id: betaID, name: "Beta" },
      ],
    };

    expect(orderedAssignedLabels(catalog, [betaID, alphaID])).toEqual([
      { id: alphaID, name: "Alpha" },
      { id: betaID, name: "Beta" },
    ]);
  });
});
