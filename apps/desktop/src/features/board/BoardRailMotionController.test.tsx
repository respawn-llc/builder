import { act, render } from "@testing-library/react";
import { useMemo, useSyncExternalStore } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import type { BoardCard, BoardColumn, WorkflowBoard, WorkflowPickerItem } from "../../api";
import { appI18n, initializeI18n } from "../../i18n/setup";
import type { PendingBoardCardMove } from "./BoardCardMotionModel";

const animateElementMock = vi.fn(() => ({ finished: Promise.resolve() }));

// ---- controllable fake board-node-cards query ----
type NodeSnapshot = Readonly<{ cards: readonly BoardCard[]; isFetching: boolean; isPending: boolean; hasData: boolean }>;
const emptyNode: NodeSnapshot = { cards: [], isFetching: false, isPending: false, hasData: true };
const nodeStore = new Map<string, NodeSnapshot>();
const listeners = new Set<() => void>();
function emit(): void {
  for (const listener of listeners) listener();
}
function setNode(id: string, snapshot: NodeSnapshot): void {
  nodeStore.set(id, snapshot);
  emit();
}
const stableFetchNext = async () => undefined;

vi.mock("./useBoardData", () => ({
  useBoardNodeCards: (_projectID: string, _workflowID: string, nodeID: string) => {
    const snapshot = useSyncExternalStore(
      (cb) => {
        listeners.add(cb);
        return () => listeners.delete(cb);
      },
      () => nodeStore.get(nodeID) ?? emptyNode,
    );
    return useMemo(
      () => ({
        data: snapshot.hasData ? { pages: [{ cards: snapshot.cards, nextPageToken: "" }] } : undefined,
        isError: false,
        error: undefined,
        isFetching: snapshot.isFetching,
        isPending: snapshot.isPending,
        isFetchingNextPage: false,
        hasNextPage: false,
        fetchNextPage: stableFetchNext,
      }),
      [snapshot],
    );
  },
}));

// Imported after the mocks above are registered.
const { BoardRailMotionController } = await import("./BoardRailMotionController");

const workflow: WorkflowPickerItem = {
  id: "wf1",
  name: "Workflow",
  description: "",
  version: 1,
  isProjectDefault: true,
  validForTaskCreation: true,
  validationErrors: [],
};

function column(over: Partial<BoardColumn>): BoardColumn {
  return {
    id: "",
    key: "",
    kind: "agent",
    name: "",
    assigneeRole: "",
    outputFields: [],
    transitionOutputFields: [],
    groupID: "",
    sortOrder: 0,
    isBacklog: false,
    isDone: false,
    taskCount: 0,
    ...over,
  };
}

function board(backlogCount: number, reconCount: number): WorkflowBoard {
  return {
    projectID: "p1",
    projectKey: "P",
    projectName: "Project",
    selectedWorkflow: workflow,
    workflows: [workflow],
    groups: [],
    columns: [
      column({ id: "backlog", key: "backlog", kind: "backlog", name: "Backlog", isBacklog: true, taskCount: backlogCount }),
      column({ id: "recon", key: "recon", name: "Recon", taskCount: reconCount }),
    ],
    generatedAt: 0,
  };
}

function card(over: Partial<BoardCard>): BoardCard {
  return {
    id: "task-1",
    shortID: "T-1",
    title: "Task",
    bodyPreview: "Body",
    workflowID: "wf1",
    activeNodeIDs: ["backlog"],
    sourceWorkspace: { id: "w", name: "Main", rootPath: "", availability: "available", isPrimary: true, updatedAt: 0 },
    status: { kind: "backlog", nativeState: "", nodeIDs: [], runIDs: [], attentionTypes: [] },
    actions: {
      canStart: true,
      canInterrupt: false,
      canResume: false,
      canCancel: false,
      manualMoveTargetNodeIDs: [],
    },
    updatedAt: 1,
    ...over,
  };
}

const backlogCard = card({});

type HarnessState = Readonly<{ board: WorkflowBoard; pending: PendingBoardCardMove | null }>;
let harnessState: HarnessState = { board: board(1, 0), pending: null };
const harnessListeners = new Set<() => void>();
function applyState(next: HarnessState): void {
  harnessState = next;
  for (const listener of harnessListeners) listener();
}

function Harness() {
  const state = useSyncExternalStore(
    (cb) => {
      harnessListeners.add(cb);
      return () => harnessListeners.delete(cb);
    },
    () => harnessState,
  );
  return (
    <BoardRailMotionController
      actionsDisabled={false}
      board={state.board}
      columnDropState={() => "idle"}
      columnIsCollapsed={() => false}
      firstActiveID="recon"
      onCardClick={() => undefined}
      onCardDragEnd={() => undefined}
      onCardDragStart={() => undefined}
      onCardsLoadError={() => undefined}
      onDeleteTask={() => undefined}
      onDropTask={() => undefined}
      onExpandColumn={() => undefined}
      onInterruptedRunObserved={() => undefined}
      onInterruptTask={() => undefined}
      onResumeTask={() => undefined}
      pendingCardMove={state.pending}
      scrollportRef={{ current: null }}
    />
  );
}

async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function settleStaleTimer(): Promise<void> {
  await act(async () => {
    vi.advanceTimersByTime(1000);
    await Promise.resolve();
    await Promise.resolve();
  });
}

function boardCardMotionCalls(): number {
  return animateElementMock.mock.calls.length;
}

describe("BoardRailMotionController manual-drag animation", () => {
  beforeAll(async () => {
    await initializeI18n();
  });

  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    animateElementMock.mockClear();
    Object.defineProperty(HTMLElement.prototype, "animate", {
      configurable: true,
      value: animateElementMock,
    });
    nodeStore.clear();
    harnessState = { board: board(1, 0), pending: null };
  });

  // Drives the exact data-layer sequence a started/moved card produces:
  // 1. backlog node-cards query refetches first and loses the card,
  // 2. the board read-model task counts lag behind,
  // 3. the stale-snapshot timer elapses while the destination is still empty.
  //
  // The ONLY difference between the two runs is `pendingCardMove`, which is the
  // single signal that distinguishes a manual drag from a server-driven move.
  async function driveBacklogExit(pending: PendingBoardCardMove | null): Promise<void> {
    setNode("backlog", { cards: [backlogCard], isFetching: false, isPending: false, hasData: true });
    setNode("recon", { cards: [], isFetching: false, isPending: false, hasData: true });
    render(
      <I18nextProvider i18n={appI18n}>
        <Harness />
      </I18nextProvider>,
    );
    await flush();
    animateElementMock.mockClear();

    // Drop happens: pending move is registered, backlog query refetches without the card.
    await act(async () => {
      applyState({ board: board(1, 0), pending });
      setNode("backlog", { cards: [], isFetching: false, isPending: false, hasData: true });
      await Promise.resolve();
    });
    await flush();

    // The stale-snapshot timer fires while the destination column is still empty.
    await settleStaleTimer();
    await flush();
  }

  it("server-driven move animates the card leaving the backlog (control)", async () => {
    await driveBacklogExit(null);
    expect(boardCardMotionCalls()).toBeGreaterThan(0);
  });

  it("manual drag animates the card leaving the backlog like a server-driven move", async () => {
    // Regression: a pending manual move must not suppress the departure animation.
    // Previously the stale-snapshot timer kept deferring while the destination
    // column was still empty, so a manual drag played no transition at all.
    await driveBacklogExit({ taskID: "task-1", targetColumnID: "recon" });
    expect(boardCardMotionCalls()).toBeGreaterThan(0);
  });
});
