import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useEffect, useMemo, useSyncExternalStore } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import type { BoardCard, BoardColumn, SelectedWorkflowBoard, WorkflowPickerItem } from "@/api";
import { appI18n, initializeI18n } from "@/i18n";
import { TestDataTransfer } from "@/test-support/board-drag";
import type { PendingBoardCardMove } from "./BoardCardMotionModel";
import type { BoardColumnDataView } from "./BoardColumnDataOwner";
import type { KanbanCardVM } from "./BoardColumnViewModel";
import type { BoardCardDragPayload } from "./BoardDragTypes";

const animateElementMock = vi.fn(() => ({ finished: Promise.resolve() }));
const emptyProjectLabelCatalog = vi.hoisted(() => ({
  data: {
    labels: [],
    projectID: "p1",
  },
}));

// ---- controllable fake board-node-cards query ----
type NodeSnapshot = Readonly<{
  cards: readonly BoardCard[];
  error?: Error | undefined;
  failureDirection?: "initial" | "newer" | "older" | undefined;
  hasNextPage?: boolean | undefined;
  hasPreviousPage?: boolean | undefined;
  isFetching: boolean;
  isFetchingNextPage?: boolean | undefined;
  isFetchingPreviousPage?: boolean | undefined;
  isPlaceholderData?: boolean | undefined;
  isPending: boolean;
  hasData: boolean;
}>;
type NodeSnapshotOverrides = Partial<Omit<NodeSnapshot, "cards">>;

function nodeSnapshot(cards: readonly BoardCard[] = [], overrides: NodeSnapshotOverrides = {}): NodeSnapshot {
  return { cards, isFetching: false, isPending: false, hasData: true, ...overrides };
}

const emptyNode = nodeSnapshot();
const nodeStore = new Map<string, NodeSnapshot>();
const listeners = new Set<() => void>();
const mountedQueryOwners = new Set<string>();
const fetchNextByNode = new Map<string, ReturnType<typeof vi.fn>>();
const fetchPreviousByNode = new Map<string, ReturnType<typeof vi.fn>>();
const refetchByNode = new Map<string, ReturnType<typeof vi.fn>>();
type FilterGenerationSnapshot = Readonly<{
  active: Readonly<{ generation: number; retiring: boolean }>;
  desiredFilter: Readonly<{ kind: "unlabeled" }> | null;
}>;
const openFilterGenerationSnapshot: FilterGenerationSnapshot = {
  active: { generation: 1, retiring: false },
  desiredFilter: null,
};
let filterGenerationSnapshot = openFilterGenerationSnapshot;
const filterGenerationListeners = new Set<() => void>();
function emit(): void {
  for (const listener of listeners) listener();
}
function setNode(id: string, cards: readonly BoardCard[] = [], overrides: NodeSnapshotOverrides = {}): void {
  nodeStore.set(id, nodeSnapshot(cards, overrides));
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
    useEffect(() => {
      mountedQueryOwners.add(nodeID);
      return () => {
        mountedQueryOwners.delete(nodeID);
      };
    }, [nodeID]);
    const fetchNextPage = fetchNextByNode.get(nodeID) ?? vi.fn(stableFetchNext);
    const fetchPreviousPage = fetchPreviousByNode.get(nodeID) ?? vi.fn(stableFetchNext);
    const refetch = refetchByNode.get(nodeID) ?? vi.fn(stableFetchNext);
    fetchNextByNode.set(nodeID, fetchNextPage);
    fetchPreviousByNode.set(nodeID, fetchPreviousPage);
    refetchByNode.set(nodeID, refetch);
    return useMemo(
      () => ({
        data: snapshot.hasData
          ? {
              pages: [
                {
                  cards: snapshot.cards,
                  nextPageToken: snapshot.hasNextPage === true ? "older" : null,
                  previousPageToken: snapshot.hasPreviousPage === true ? "newer" : null,
                },
              ],
            }
          : undefined,
        isError: snapshot.error !== undefined,
        error: snapshot.error,
        isFetching: snapshot.isFetching,
        isPending: snapshot.isPending,
        isFetchNextPageError: snapshot.error !== undefined && snapshot.failureDirection === "older",
        isFetchPreviousPageError: snapshot.error !== undefined && snapshot.failureDirection === "newer",
        isFetchingNextPage: snapshot.isFetchingNextPage ?? false,
        isFetchingPreviousPage: snapshot.isFetchingPreviousPage ?? false,
        isPlaceholderData: snapshot.isPlaceholderData ?? false,
        hasNextPage: snapshot.hasNextPage ?? false,
        hasPreviousPage: snapshot.hasPreviousPage ?? false,
        fetchNextPage,
        fetchPreviousPage,
        refetch,
      }),
      [fetchNextPage, fetchPreviousPage, refetch, snapshot],
    );
  },
}));

vi.mock("./BoardFilterGenerationRuntime", () => ({
  useBoardFilterGeneration: () => {
    const snapshot = useSyncExternalStore(
      (listener) => {
        filterGenerationListeners.add(listener);
        return () => {
          filterGenerationListeners.delete(listener);
        };
      },
      () => filterGenerationSnapshot,
    );
    return { snapshot };
  },
}));

vi.mock("@/shared/labels", () => ({
  useProjectLabelCatalog: () => emptyProjectLabelCatalog,
}));

// Imported after the mocks above are registered.
const { BoardColumnDataOwner } = await import("./BoardColumnDataOwner");
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

function board(backlogCount: number, reconCount: number): SelectedWorkflowBoard {
  return {
    projectID: "p1",
    projectKey: "P",
    projectName: "Project",
    defaultWorkspaceID: "w",
    attachedWorkspaceCount: 1,
    selectedWorkflow: workflow,
    workflows: [workflow],
    groups: [],
    columns: [
      column({
        id: "backlog",
        key: "backlog",
        kind: "backlog",
        name: "Backlog",
        isBacklog: true,
        taskCount: backlogCount,
      }),
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
    preview: { markdown: "Body", truncated: false },
    workflowID: "wf1",
    activeNodeIDs: ["backlog"],
    sourceWorkspace: {
      id: "w",
      name: "Main",
      rootPath: "",
      availability: "available",
      isPrimary: true,
      updatedAt: 0,
    },
    status: { kind: "backlog", nativeState: "", nodeIDs: [], runIDs: [], attentionTypes: [] },
    actions: {
      canStart: true,
      canInterrupt: false,
      canResume: false,
      canCancel: false,
      manualMoveTargetNodeIDs: [],
    },
    labelIDs: [],
    updatedAt: 1,
    ...over,
  };
}

const backlogCard = card({});

function numberedCards(count: number, latestUpdatedAt = count): BoardCard[] {
  return Array.from({ length: count }, (_, index) =>
    card({
      id: `task-${index.toString()}`,
      shortID: `T-${index.toString()}`,
      title: `Task ${index.toString()}`,
      updatedAt: latestUpdatedAt - index,
    }),
  );
}

function toTestCardVM(value: BoardCard): KanbanCardVM {
  return {
    activeNodeIDs: value.activeNodeIDs,
    actions: {
      canInterrupt: value.actions.canInterrupt,
      canResume: value.actions.canResume,
      canStart: value.actions.canStart,
      manualMoveTargetNodeIDs: value.actions.manualMoveTargetNodeIDs,
    },
    borderTone: "default",
    id: value.id,
    labels: [],
    preview: value.preview,
    shortID: value.shortID,
    statusKind: value.status.kind,
    statusRunIDs: value.status.runIDs,
    title: value.title,
    updatedAt: value.updatedAt,
    workspaceChipLabel: null,
  };
}

type ActiveDragState = Readonly<{
  instance: Readonly<{ columnID: string; taskID: string }>;
  lastCardIndex: number;
  payload: BoardCardDragPayload;
  snapshot: KanbanCardVM;
}>;

type HarnessState = Readonly<{
  activeDrag: ActiveDragState | null;
  board: SelectedWorkflowBoard;
  collapsedColumnIDs: ReadonlySet<string>;
  pending: PendingBoardCardMove | null;
}>;
function initialHarnessState(): HarnessState {
  return {
    activeDrag: null,
    board: board(1, 0),
    collapsedColumnIDs: new Set(),
    pending: null,
  };
}

let harnessState = initialHarnessState();
const harnessListeners = new Set<() => void>();
function updateHarness(overrides: Partial<HarnessState>): void {
  harnessState = { ...harnessState, ...overrides };
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
  const controllerProps = {
    activeDrag: state.activeDrag,
    actionsDisabled: false,
    board: state.board,
    columnDropState: () => "idle" as const,
    columnIsCollapsed: (value: BoardColumn) => state.collapsedColumnIDs.has(value.id),
    firstActiveID: "recon",
    onCardClick: () => undefined,
    onCardDragEnd: () => undefined,
    onCardDragStart: (drag: ActiveDragState | BoardCardDragPayload) => {
      const active =
        "instance" in drag
          ? drag
          : {
              instance: { columnID: "backlog", taskID: drag.taskID },
              lastCardIndex: 0,
              payload: drag,
              snapshot: toTestCardVM(backlogCard),
            };
      updateHarness({ activeDrag: active });
    },
    onCardsLoadError: () => undefined,
    onDeleteTask: () => undefined,
    onDropTask: () => undefined,
    onExpandColumn: () => undefined,
    onInterruptedRunObserved: () => undefined,
    onInterruptTask: () => undefined,
    onRegisterColumnScrollport: () => undefined,
    onResumeTask: () => undefined,
    pendingCardMove: state.pending,
    scrollportRef: { current: null },
  };
  return <BoardRailMotionController {...controllerProps} />;
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
  mountedQueryOwners.clear();
  fetchNextByNode.clear();
  fetchPreviousByNode.clear();
  refetchByNode.clear();
  filterGenerationSnapshot = openFilterGenerationSnapshot;
  filterGenerationListeners.clear();
  harnessState = initialHarnessState();
});

describe("BoardRailMotionController manual-drag animation", () => {
  // Drives the exact data-layer sequence a started/moved card produces:
  // 1. backlog node-cards query refetches first and loses the card,
  // 2. the board read-model task counts lag behind,
  // 3. the stale-snapshot timer elapses while the destination is still empty.
  //
  // The ONLY difference between the two runs is `pendingCardMove`, which is the
  // single signal that distinguishes a manual drag from a server-driven move.
  async function driveBacklogExit(pending: PendingBoardCardMove | null): Promise<void> {
    setNode("backlog", [backlogCard]);
    setNode("recon");
    renderHarness();
    await flush();
    animateElementMock.mockClear();

    // Drop happens: pending move is registered, backlog query refetches without the card.
    await act(async () => {
      updateHarness({ board: board(1, 0), pending });
      setNode("backlog");
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

describe("BoardRailMotionController filter replacement", () => {
  it("replaces an accepted empty filter generation without leaving departing card clones", async () => {
    const neverFinishes = new Promise<never>(() => undefined);
    animateElementMock.mockReturnValue({ finished: neverFinishes });
    setNode("backlog", [backlogCard]);
    renderHarness();
    await flush();
    animateElementMock.mockClear();

    await act(async () => {
      setNode("backlog", [backlogCard], {
        isFetching: true,
        isPlaceholderData: true,
      });
      filterGenerationSnapshot = {
        active: { generation: 2, retiring: false },
        desiredFilter: null,
      };
      for (const listener of filterGenerationListeners) {
        listener();
      }
      await Promise.resolve();
    });
    await act(async () => {
      updateHarness({ board: board(0, 0) });
      setNode("backlog");
      await Promise.resolve();
    });
    await flush();

    expect(animateElementMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("article", { name: "Task" })).not.toBeInTheDocument();
  });
});

describe("BoardRailMotionController bounded column lifecycle", () => {
  beforeEach(() => {
    ControlledIntersectionObserver.reset();
    vi.stubGlobal("IntersectionObserver", ControlledIntersectionObserver);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("mounts card data only inside the active margin and releases it immediately on exit", async () => {
    setNode("backlog", [backlogCard]);
    renderHarness();
    await flush();

    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    expect(mountedQueryOwners).not.toContain("backlog");

    await setIntersecting(true, backlog);
    expect(mountedQueryOwners).toContain("backlog");

    await setIntersecting(false, backlog);
    expect(mountedQueryOwners).not.toContain("backlog");
  });

  it("reactivates at the newest page and top inside the existing column shell", async () => {
    setNode("backlog", [backlogCard]);
    renderHarness();
    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    await setIntersecting(true, backlog);

    const scrollport = screen.getByTestId("kanban-column-scroll-backlog");
    scrollport.scrollTop = 420;
    await setIntersecting(false, backlog);
    expect(screen.getByRole("listitem", { name: "Backlog" })).toBe(backlog);

    await setIntersecting(true, backlog);

    expect(screen.getByRole("listitem", { name: "Backlog" })).toBe(backlog);
    expect(scrollport.scrollTop).toBe(0);
  });

  it("keeps expanded and collapsed shell state stable through initial loading and failure", async () => {
    const loading = { isFetching: true, isPending: true, hasData: false };
    setNode("backlog", [], loading);
    setNode("recon", [], loading);
    updateHarness({
      board: board(1, 1),
      collapsedColumnIDs: new Set(["recon"]),
    });
    renderHarness();

    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    const recon = screen.getByRole("listitem", { name: "Recon" });
    await setIntersecting(true, backlog, recon);

    expect(backlog).toHaveAttribute("data-collapsed", "false");
    expect(recon).toHaveAttribute("data-collapsed", "true");
    expect(within(backlog).getByRole("status")).toBeInTheDocument();

    await updateNode("backlog", [], {
      error: new Error("offline"),
      failureDirection: "initial",
      hasData: false,
    });

    expect(screen.getByRole("listitem", { name: "Backlog" })).toBe(backlog);
    expect(backlog).toHaveAttribute("data-collapsed", "false");
    expect(screen.getByRole("listitem", { name: "Recon" })).toBe(recon);
    expect(recon).toHaveAttribute("data-collapsed", "true");
  });

  it("suppresses enter and move motion during hydration and bidirectional page-window changes", async () => {
    setNode("backlog", [], { isFetching: true, isPending: true, hasData: false });
    updateHarness({ board: board(4, 0) });
    renderHarness();
    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    await setIntersecting(true, backlog);

    const taskTwo = card({ id: "task-2", shortID: "T-2", title: "Task 2", updatedAt: 2 });
    const taskOne = card({ id: "task-1", shortID: "T-1", title: "Task 1", updatedAt: 1 });
    await updateNode("backlog", [taskTwo, taskOne], {
      hasPreviousPage: true,
    });

    expect(boardCardMotionCalls()).toBe(0);
    animateElementMock.mockClear();

    await updateNode("backlog", [taskTwo, taskOne], {
      isFetching: true,
      isFetchingPreviousPage: true,
      hasPreviousPage: true,
    });
    await updateNode(
      "backlog",
      [
        card({ id: "task-4", shortID: "T-4", title: "Task 4", updatedAt: 4 }),
        card({ id: "task-3", shortID: "T-3", title: "Task 3", updatedAt: 3 }),
        taskTwo,
        taskOne,
      ],
      { hasPreviousPage: false, hasNextPage: true },
    );

    expect(boardCardMotionCalls()).toBe(0);
  });

  it("renders an initial in-column Retry without collapsing or resizing the shell", async () => {
    setNode("backlog", [], {
      error: new Error("initial page failed"),
      failureDirection: "initial",
      hasData: false,
    });
    renderHarness();
    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    await setIntersecting(true, backlog);

    fireEvent.click(within(backlog).getByRole("button", { name: "Try again" }));

    expect(refetchByNode.get("backlog")).toHaveBeenCalledTimes(1);
    expect(backlog).toHaveAttribute("data-collapsed", "false");
  });

  it.each([
    ["newer", "previous", fetchPreviousByNode],
    ["older", "next", fetchNextByNode],
  ] as const)(
    "keeps retained rows and the anchor while retrying only the failed %s direction",
    async (failureDirection, boundaryDirection, expectedFetches) => {
      const cards = numberedCards(8, 20);
      setNode("backlog", cards, {
        hasNextPage: true,
        hasPreviousPage: true,
      });
      updateHarness({ board: board(cards.length, 0) });
      renderHarness();
      const backlog = screen.getByRole("listitem", { name: "Backlog" });
      await setIntersecting(true, backlog);
      fetchNextByNode.get("backlog")?.mockClear();
      fetchPreviousByNode.get("backlog")?.mockClear();

      const scrollport = screen.getByTestId("kanban-column-scroll-backlog");
      scrollport.scrollTop = 346;
      const leadingCard = screen.getByRole("article", { name: "Task 0" });
      await updateNode("backlog", cards, {
        error: new Error(`${failureDirection} page failed`),
        failureDirection,
        hasNextPage: true,
        hasPreviousPage: true,
      });

      expect(screen.getByRole("article", { name: "Task 0" })).toBe(leadingCard);
      expect(scrollport.scrollTop).toBe(346);
      const boundary = screen.getByTestId(`virtual-boundary-${boundaryDirection}`);
      fireEvent.click(within(boundary).getByRole("button", { name: "Try again" }));

      expect(expectedFetches.get("backlog")).toHaveBeenCalledTimes(1);
      const otherFetches =
        failureDirection === "newer" ? fetchNextByNode.get("backlog") : fetchPreviousByNode.get("backlog");
      expect(otherFetches).not.toHaveBeenCalled();
      expect(scrollport.scrollTop).toBe(346);
    },
  );

  it("disables every pagination trigger while a desired filter is pending", async () => {
    const cards = numberedCards(12);
    setNode("backlog", cards, {
      hasNextPage: true,
      hasPreviousPage: true,
    });
    updateHarness({ board: board(cards.length, 0) });
    renderHarness();
    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    await setIntersecting(true, backlog);
    fetchNextByNode.get("backlog")?.mockClear();
    fetchPreviousByNode.get("backlog")?.mockClear();

    await act(async () => {
      filterGenerationSnapshot = {
        active: { generation: 1, retiring: true },
        desiredFilter: { kind: "unlabeled" },
      };
      for (const listener of filterGenerationListeners) {
        listener();
      }
      await Promise.resolve();
    });
    const scrollport = screen.getByTestId("kanban-column-scroll-backlog");
    scrollport.scrollTop = 0;
    fireEvent.scroll(scrollport);
    scrollport.scrollTop = 10_000;
    fireEvent.scroll(scrollport);
    await flush();

    expect(fetchNextByNode.get("backlog")).not.toHaveBeenCalled();
    expect(fetchPreviousByNode.get("backlog")).not.toHaveBeenCalled();
    expect(screen.queryByTestId("virtual-boundary-next")).not.toBeInTheDocument();
    expect(screen.queryByTestId("virtual-boundary-previous")).not.toBeInTheDocument();
  });

  it("keeps retained placeholder pages non-pageable until the promoted first page is accepted", async () => {
    const cards = numberedCards(12);
    let dataView: BoardColumnDataView | undefined;
    setNode("backlog", cards, {
      hasNextPage: true,
      hasPreviousPage: true,
      isPlaceholderData: true,
    });
    const selectedBoard = board(cards.length, 0);
    const backlog = selectedBoard.columns[0];
    if (backlog === undefined) {
      throw new Error("Expected the test board to contain a backlog column.");
    }
    render(
      <I18nextProvider i18n={appI18n}>
        <BoardColumnDataOwner
          board={selectedBoard}
          column={backlog}
          onCardsLoadError={() => undefined}
          onDataViewChange={(value) => {
            dataView = value;
          }}
          onDataViewRelease={() => undefined}
          onInterruptedRunObserved={() => undefined}
          onReportColumnSnapshot={() => undefined}
        />
      </I18nextProvider>,
    );

    expect(dataView).toMatchObject({
      hasNextPage: false,
      hasPreviousPage: false,
      nextBoundary: undefined,
      previousBoundary: undefined,
    });

    await updateNode("backlog", cards, {
      error: new Error("promoted first page failed"),
      failureDirection: "initial",
      hasNextPage: true,
      hasPreviousPage: true,
      isPlaceholderData: true,
    });
    expect(dataView).toMatchObject({
      hasNextPage: false,
      hasPreviousPage: false,
      nextBoundary: undefined,
      previousBoundary: undefined,
    });

    await updateNode("backlog", cards, {
      hasNextPage: true,
      hasPreviousPage: true,
      isPlaceholderData: false,
    });
    fetchNextByNode.get("backlog")?.mockClear();
    fetchPreviousByNode.get("backlog")?.mockClear();
    expect(dataView).toMatchObject({
      hasNextPage: true,
      hasPreviousPage: true,
    });
    dataView?.onLoadMore();
    dataView?.onLoadPrevious();
    expect(fetchNextByNode.get("backlog")).toHaveBeenCalledTimes(1);
    expect(fetchPreviousByNode.get("backlog")).toHaveBeenCalledTimes(1);
  });

  it("tracks fan-out card instances independently and parses Markdown only after each instance intersects", async () => {
    const sharedCard = card({
      activeNodeIDs: ["backlog", "recon"],
      preview: { markdown: "Visible **preview**", truncated: true },
      title: "Shared task",
    });
    setNode("backlog", [sharedCard]);
    setNode("recon", [sharedCard]);
    updateHarness({ board: board(1, 1) });
    renderHarness();
    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    const recon = screen.getByRole("listitem", { name: "Recon" });
    await setIntersecting(true, backlog, recon);

    const [backlogCardElement, reconCardElement] = screen.getAllByRole("article", {
      name: "Shared task",
    });
    if (backlogCardElement === undefined || reconCardElement === undefined) {
      throw new Error("Expected one Shared task instance in each active column");
    }
    expect(observedCards()).toEqual(new Set([backlogCardElement, reconCardElement]));
    expect(within(backlogCardElement).getByTestId("task-card-body")).not.toHaveTextContent("Visible preview");
    expect(within(reconCardElement).getByTestId("task-card-body")).not.toHaveTextContent("Visible preview");

    await setIntersecting(true, backlogCardElement);
    expect(within(backlogCardElement).getByTestId("task-card-body")).toHaveTextContent("Visible preview");
    expect(within(backlogCardElement).getByTestId("task-card-preview-ellipsis")).toBeInTheDocument();
    expect(within(reconCardElement).getByTestId("task-card-body")).not.toHaveTextContent("Visible preview");

    await setIntersecting(false, recon);
    expect(screen.getByRole("article", { name: "Shared task" })).toBe(backlogCardElement);
    expect(within(backlogCardElement).getByTestId("task-card-body")).toHaveTextContent("Visible preview");
  });

  it("keeps the same native drag-source instance mounted beyond overscan and source-page eviction", async () => {
    const cards = numberedCards(100);
    setNode("backlog", cards, {
      hasNextPage: true,
    });
    updateHarness({ board: board(100, 0) });
    renderHarness();
    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    await setIntersecting(true, backlog);

    const source = screen.getByRole("article", { name: "Task 0" });
    fireEvent.dragStart(source, { dataTransfer: new TestDataTransfer() });
    await flush();

    const scrollport = screen.getByTestId("kanban-column-scroll-backlog");
    scrollport.scrollTop = 12_000;
    fireEvent.scroll(scrollport);
    await flush();
    expect(source).toBeInTheDocument();

    await updateNode("backlog", cards.slice(25), {
      hasPreviousPage: true,
    });

    expect(screen.getByRole("article", { name: "Task 0" })).toBe(source);
  });
});

function renderHarness() {
  return render(
    <I18nextProvider i18n={appI18n}>
      <Harness />
    </I18nextProvider>,
  );
}

async function updateNode(
  id: string,
  cards: readonly BoardCard[] = [],
  overrides: NodeSnapshotOverrides = {},
): Promise<void> {
  await act(async () => {
    setNode(id, cards, overrides);
    await Promise.resolve();
  });
  await flush();
}

class ControlledIntersectionObserver implements IntersectionObserver {
  static instances: ControlledIntersectionObserver[] = [];

  static reset(): void {
    ControlledIntersectionObserver.instances = [];
  }

  readonly root: Element | Document | null;
  readonly rootMargin: string;
  readonly scrollMargin = "0px";
  readonly thresholds: readonly number[] = [0];
  readonly targets = new Set<Element>();
  readonly #callback: IntersectionObserverCallback;

  constructor(callback: IntersectionObserverCallback, options: IntersectionObserverInit = {}) {
    this.#callback = callback;
    this.root = options.root ?? null;
    this.rootMargin = options.rootMargin ?? "0px";
    ControlledIntersectionObserver.instances.push(this);
  }

  disconnect(): void {
    this.targets.clear();
  }

  observe(target: Element): void {
    this.targets.add(target);
  }

  takeRecords(): IntersectionObserverEntry[] {
    return [];
  }

  unobserve(target: Element): void {
    this.targets.delete(target);
  }

  emit(target: Element, isIntersecting: boolean): void {
    this.#callback(
      [
        {
          boundingClientRect: target.getBoundingClientRect(),
          intersectionRatio: isIntersecting ? 1 : 0,
          intersectionRect: target.getBoundingClientRect(),
          isIntersecting,
          rootBounds: null,
          target,
          time: performance.now(),
        },
      ],
      this,
    );
  }
}

async function setIntersecting(isIntersecting: boolean, ...targets: readonly Element[]): Promise<void> {
  const observations = targets.map((target) => {
    const observer = ControlledIntersectionObserver.instances.find((candidate) =>
      candidate.targets.has(target),
    );
    if (observer === undefined) {
      throw new Error(`No IntersectionObserver owns ${target.tagName}`);
    }
    return { observer, target };
  });
  act(() => {
    for (const { observer, target } of observations) {
      observer.emit(target, isIntersecting);
    }
  });
  await flush();
}

function observedCards(): ReadonlySet<Element> {
  return new Set(
    ControlledIntersectionObserver.instances.flatMap((observer) =>
      Array.from(observer.targets).filter((target) => target.getAttribute("data-testid") === "task-card"),
    ),
  );
}
