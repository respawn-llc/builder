import { describe, expect, it } from "vitest";

import type { KanbanCardVM } from "./BoardColumnViewModel";
import {
  boardCardColumnCountSnapshot,
  boardCardMotionParticipants,
  boardCardSnapshotFromEntries,
  boardCardSnapshotsEqual,
  boardCardViewTransitionName,
  dirtyBoardCardCountColumnIDs,
} from "./BoardCardMotionModel";

describe("BoardCardMotionModel", () => {
  it("uses CSS-safe deterministic view transition names", () => {
    expect(boardCardViewTransitionName("Task 1/α")).toMatch(/^board-card-[0-9a-f-]+$/u);
    expect(boardCardViewTransitionName("Task 1/α")).toBe(boardCardViewTransitionName("Task 1/α"));
  });

  it("names shared and exiting visible cards while excluding duplicate rendered task IDs", () => {
    const oldSnapshot = boardCardSnapshotFromEntries([
      ["backlog", [card("task-1"), card("task-duplicate")]],
      ["review", [card("task-duplicate")]],
      ["done", [card("task-exit")]],
    ]);
    const newSnapshot = boardCardSnapshotFromEntries([
      ["backlog", []],
      ["review", [card("task-1"), card("task-duplicate")]],
      ["done", []],
    ]);

    const participants = boardCardMotionParticipants(
      oldSnapshot,
      newSnapshot,
      new Set(["task-1", "task-duplicate", "task-exit"]),
    );

    expect(participants.namesByCardID.get("task-1")).toBe(boardCardViewTransitionName("task-1"));
    expect(participants.namesByCardID.get("task-exit")).toBe(boardCardViewTransitionName("task-exit"));
    expect(participants.namesByCardID.has("task-duplicate")).toBe(false);
  });

  it("names moved cards even when native drag temporarily reports them non-visible", () => {
    const participants = boardCardMotionParticipants(
      boardCardSnapshotFromEntries([["backlog", [card("task-1")]]]),
      boardCardSnapshotFromEntries([["recon", [card("task-1")]]]),
      new Set(),
    );

    expect(participants.namesByCardID.get("task-1")).toBe(boardCardViewTransitionName("task-1"));
  });

  it("does not name same-position cards when only card content changes", () => {
    const participants = boardCardMotionParticipants(
      boardCardSnapshotFromEntries([["backlog", [card("task-1")]]]),
      boardCardSnapshotFromEntries([["backlog", [{ ...card("task-1"), title: "Renamed" }]]]),
      new Set(["task-1"]),
    );

    expect(participants.namesByCardID.size).toBe(0);
    expect(participants.revealCardIDs.size).toBe(0);
  });

  it("reveals new-only cards without naming them as shared elements", () => {
    const participants = boardCardMotionParticipants(
      boardCardSnapshotFromEntries([["backlog", []]]),
      boardCardSnapshotFromEntries([["backlog", [card("task-new")]]]),
      new Set(),
    );

    expect(participants.namesByCardID.size).toBe(0);
    expect(participants.revealCardIDs.has("task-new")).toBe(true);
  });

  it("compares card snapshots by card content and order", () => {
    const snapshot = boardCardSnapshotFromEntries([["backlog", [card("task-1")]]]);

    expect(boardCardSnapshotsEqual(snapshot, boardCardSnapshotFromEntries([["backlog", [card("task-1")]]]))).toBe(
      true,
    );
    expect(
      boardCardSnapshotsEqual(snapshot, boardCardSnapshotFromEntries([["backlog", [{ ...card("task-1"), title: "Changed" }]]])),
    ).toBe(false);
  });

  it("detects dirty columns from board read-model task count changes", () => {
    const board = {
      columns: [
        { id: "backlog", taskCount: 1 },
        { id: "recon", taskCount: 0 },
      ],
    };
    const nextBoard = {
      columns: [
        { id: "backlog", taskCount: 0 },
        { id: "recon", taskCount: 1 },
      ],
    };

    expect(dirtyBoardCardCountColumnIDs(boardCardColumnCountSnapshot(board), boardCardColumnCountSnapshot(nextBoard))).toEqual([
      "backlog",
      "recon",
    ]);
  });
});

function card(id: string): KanbanCardVM {
  return {
    activeNodeIDs: id === "task-1" ? ["backlog"] : [],
    actions: {
      canInterrupt: false,
      canResume: false,
      canStart: true,
      manualMoveTargetNodeIDs: [],
    },
    bodyPreview: "Body",
    id,
    shortID: id,
    sourceWorkspaceName: "Main",
    statusKind: "backlog",
    statusRunIDs: [],
    title: "Task",
    updatedAt: 1,
  };
}
