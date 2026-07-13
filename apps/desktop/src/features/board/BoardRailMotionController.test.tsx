import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useEffect, useMemo, useSyncExternalStore } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import type { BoardCard, BoardColumn, WorkflowBoard, WorkflowPickerItem } from "../../api";
import { appI18n, initializeI18n } from "../../i18n/setup";
import type { PendingBoardCardMove } from "./BoardCardMotionModel";
import type { KanbanCardVM } from "./BoardColumnViewModel";
import type { BoardCardDragPayload } from "./BoardDragTypes";

const animateElementMock = vi.fn(() => ({ finished: Promise.resolve() }));

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
  isPending: boolean;
  hasData: boolean;
}>;
const emptyNode: NodeSnapshot = { cards: [], isFetching: false, isPending: false, hasData: true };
const nodeStore = new Map<string, NodeSnapshot>();
const listeners = new Set<() => void>();
const mountedQueryOwners = new Set<string>();
const fetchNextByNode = new Map<string, ReturnType<typeof vi.fn>>();
const fetchPreviousByNode = new Map<string, ReturnType<typeof vi.fn>>();
const refetchByNode = new Map<string, ReturnType<typeof vi.fn>>();
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
    updatedAt: 1,
    ...over,
  };
}

const backlogCard = card({});

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
  lastVirtualIndex: number;
  payload: BoardCardDragPayload;
  snapshot: KanbanCardVM;
}>;

type HarnessState = Readonly<{
  activeDrag: ActiveDragState | null;
  board: WorkflowBoard;
  collapsedColumnIDs: ReadonlySet<string>;
  pending: PendingBoardCardMove | null;
}>;
let harnessState: HarnessState = {
  activeDrag: null,
  board: board(1, 0),
  collapsedColumnIDs: new Set(),
  pending: null,
};
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
              lastVirtualIndex: 0,
              payload: drag,
              snapshot: toTestCardVM(backlogCard),
            };
      applyState({ ...harnessState, activeDrag: active });
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
  return (
    <BoardRailMotionController
      {...controllerProps}
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
    mountedQueryOwners.clear();
    fetchNextByNode.clear();
    fetchPreviousByNode.clear();
    refetchByNode.clear();
    harnessState = {
      activeDrag: null,
      board: board(1, 0),
      collapsedColumnIDs: new Set(),
      pending: null,
    };
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
      applyState({ ...harnessState, board: board(1, 0), pending });
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

describe("BoardRailMotionController bounded column lifecycle", () => {
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
    harnessState = {
      activeDrag: null,
      board: board(1, 0),
      collapsedColumnIDs: new Set(),
      pending: null,
    };
    ControlledIntersectionObserver.reset();
    vi.stubGlobal("IntersectionObserver", ControlledIntersectionObserver);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("mounts card data only inside the active margin and releases it immediately on exit", async () => {
    setNode("backlog", { cards: [backlogCard], isFetching: false, isPending: false, hasData: true });
    renderHarness();
    await flush();

    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    expect(mountedQueryOwners).not.toContain("backlog");

    intersect(backlog, true);
    await flush();
    expect(mountedQueryOwners).toContain("backlog");

    intersect(backlog, false);
    await flush();
    expect(mountedQueryOwners).not.toContain("backlog");
  });

  it("reactivates at the newest page and top inside the existing column shell", async () => {
    setNode("backlog", { cards: [backlogCard], isFetching: false, isPending: false, hasData: true });
    renderHarness();
    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    intersect(backlog, true);
    await flush();

    const scrollport = screen.getByTestId("kanban-column-scroll-backlog");
    scrollport.scrollTop = 420;
    intersect(backlog, false);
    await flush();
    expect(screen.getByRole("listitem", { name: "Backlog" })).toBe(backlog);

    intersect(backlog, true);
    await flush();

    expect(screen.getByRole("listitem", { name: "Backlog" })).toBe(backlog);
    expect(scrollport.scrollTop).toBe(0);
  });

  it("keeps expanded and collapsed shell state stable through initial loading and failure", async () => {
    setNode("backlog", { cards: [], isFetching: true, isPending: true, hasData: false });
    setNode("recon", { cards: [], isFetching: true, isPending: true, hasData: false });
    harnessState = {
      ...harnessState,
      board: board(1, 1),
      collapsedColumnIDs: new Set(["recon"]),
    };
    renderHarness();

    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    const recon = screen.getByRole("listitem", { name: "Recon" });
    intersect(backlog, true);
    intersect(recon, true);
    await flush();

    expect(backlog).toHaveAttribute("data-collapsed", "false");
    expect(recon).toHaveAttribute("data-collapsed", "true");
    expect(within(backlog).getByRole("status")).toBeInTheDocument();

    await updateNode("backlog", {
      cards: [],
      error: new Error("offline"),
      failureDirection: "initial",
      isFetching: false,
      isPending: false,
      hasData: false,
    });

    expect(screen.getByRole("listitem", { name: "Backlog" })).toBe(backlog);
    expect(backlog).toHaveAttribute("data-collapsed", "false");
    expect(screen.getByRole("listitem", { name: "Recon" })).toBe(recon);
    expect(recon).toHaveAttribute("data-collapsed", "true");
  });

  it("suppresses enter and move motion during hydration and bidirectional page-window changes", async () => {
    setNode("backlog", { cards: [], isFetching: true, isPending: true, hasData: false });
    harnessState = { ...harnessState, board: board(4, 0) };
    renderHarness();
    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    intersect(backlog, true);
    await flush();

    const taskTwo = card({ id: "task-2", shortID: "T-2", title: "Task 2", updatedAt: 2 });
    const taskOne = card({ id: "task-1", shortID: "T-1", title: "Task 1", updatedAt: 1 });
    await updateNode("backlog", {
      cards: [taskTwo, taskOne],
      isFetching: false,
      isPending: false,
      hasData: true,
      hasPreviousPage: true,
    });

    expect(screen.getByRole("article", { name: "Task 2" })).not.toHaveClass("board-card-enter-reveal");
    expect(boardCardMotionCalls()).toBe(0);
    animateElementMock.mockClear();

    await updateNode("backlog", {
      cards: [taskTwo, taskOne],
      isFetching: true,
      isFetchingPreviousPage: true,
      isPending: false,
      hasData: true,
      hasPreviousPage: true,
    });
    await updateNode("backlog", {
      cards: [
        card({ id: "task-4", shortID: "T-4", title: "Task 4", updatedAt: 4 }),
        card({ id: "task-3", shortID: "T-3", title: "Task 3", updatedAt: 3 }),
        taskTwo,
        taskOne,
      ],
      isFetching: false,
      isPending: false,
      hasData: true,
      hasPreviousPage: false,
      hasNextPage: true,
    });

    expect(boardCardMotionCalls()).toBe(0);
  });

  it("renders an initial in-column Retry without collapsing or resizing the shell", async () => {
    setNode("backlog", {
      cards: [],
      error: new Error("initial page failed"),
      failureDirection: "initial",
      isFetching: false,
      isPending: false,
      hasData: false,
    });
    renderHarness();
    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    intersect(backlog, true);
    await flush();

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
      const cards = Array.from({ length: 8 }, (_, index) =>
        card({
          id: `task-${index.toString()}`,
          shortID: `T-${index.toString()}`,
          title: `Task ${index.toString()}`,
          updatedAt: 20 - index,
        }),
      );
      setNode("backlog", {
        cards,
        hasNextPage: true,
        hasPreviousPage: true,
        isFetching: false,
        isPending: false,
        hasData: true,
      });
      harnessState = { ...harnessState, board: board(cards.length, 0) };
      renderHarness();
      const backlog = screen.getByRole("listitem", { name: "Backlog" });
      intersect(backlog, true);
      await flush();

      const scrollport = screen.getByTestId("kanban-column-scroll-backlog");
      scrollport.scrollTop = 346;
      const leadingCard = screen.getByRole("article", { name: "Task 0" });
      await updateNode("backlog", {
        cards,
        error: new Error(`${failureDirection} page failed`),
        failureDirection,
        hasNextPage: true,
        hasPreviousPage: true,
        isFetching: false,
        isPending: false,
        hasData: true,
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

  it("tracks fan-out card instances independently and parses Markdown only after each instance intersects", async () => {
    const sharedCard = card({
      activeNodeIDs: ["backlog", "recon"],
      preview: { markdown: "Visible **preview**", truncated: true },
      title: "Shared task",
    });
    setNode("backlog", { cards: [sharedCard], isFetching: false, isPending: false, hasData: true });
    setNode("recon", { cards: [sharedCard], isFetching: false, isPending: false, hasData: true });
    harnessState = { ...harnessState, board: board(1, 1) };
    renderHarness();
    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    const recon = screen.getByRole("listitem", { name: "Recon" });
    intersect(backlog, true);
    intersect(recon, true);
    await flush();

    const [backlogCardElement, reconCardElement] = screen.getAllByRole("article", {
      name: "Shared task",
    });
    if (backlogCardElement === undefined || reconCardElement === undefined) {
      throw new Error("Expected one Shared task instance in each active column");
    }
    expect(observedCards()).toEqual(new Set([backlogCardElement, reconCardElement]));
    expect(within(backlogCardElement).getByTestId("task-card-body")).toBeEmptyDOMElement();
    expect(within(reconCardElement).getByTestId("task-card-body")).toBeEmptyDOMElement();

    intersect(backlogCardElement, true);
    await flush();
    expect(within(backlogCardElement).getByTestId("task-card-body")).toHaveTextContent(
      "Visible preview…",
    );
    expect(within(reconCardElement).getByTestId("task-card-body")).toBeEmptyDOMElement();

    await updateNode("recon", { cards: [], isFetching: false, isPending: false, hasData: true });
    expect(screen.getByRole("article", { name: "Shared task" })).toBe(backlogCardElement);
    expect(within(backlogCardElement).getByTestId("task-card-body")).toHaveTextContent(
      "Visible preview…",
    );
  });

  it("keeps the same native drag-source instance mounted beyond overscan and source-page eviction", async () => {
    const cards = Array.from({ length: 100 }, (_, index) =>
      card({
        id: `task-${index.toString()}`,
        shortID: `T-${index.toString()}`,
        title: `Task ${index.toString()}`,
        updatedAt: 100 - index,
      }),
    );
    setNode("backlog", {
      cards,
      hasNextPage: true,
      isFetching: false,
      isPending: false,
      hasData: true,
    });
    harnessState = { ...harnessState, board: board(100, 0) };
    renderHarness();
    const backlog = screen.getByRole("listitem", { name: "Backlog" });
    intersect(backlog, true);
    await flush();

    const source = screen.getByRole("article", { name: "Task 0" });
    fireEvent.dragStart(source, { dataTransfer: new TestDataTransfer() });
    await flush();

    const scrollport = screen.getByTestId("kanban-column-scroll-backlog");
    scrollport.scrollTop = 12_000;
    fireEvent.scroll(scrollport);
    await flush();
    expect(source).toBeInTheDocument();

    await updateNode("backlog", {
      cards: cards.slice(25),
      hasPreviousPage: true,
      isFetching: false,
      isPending: false,
      hasData: true,
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

async function updateNode(id: string, snapshot: NodeSnapshot): Promise<void> {
  await act(async () => {
    setNode(id, snapshot);
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

function intersect(target: Element, isIntersecting: boolean): void {
  const observer = ControlledIntersectionObserver.instances.find((candidate) =>
    candidate.targets.has(target),
  );
  if (observer === undefined) {
    throw new Error(`No IntersectionObserver owns ${target.tagName}`);
  }
  act(() => {
    observer.emit(target, isIntersecting);
  });
}

function observedCards(): ReadonlySet<Element> {
  return new Set(
    ControlledIntersectionObserver.instances.flatMap((observer) =>
      Array.from(observer.targets).filter((target) => target.getAttribute("data-testid") === "task-card"),
    ),
  );
}

class TestDataTransfer {
  readonly #values = new Map<string, string>();
  effectAllowed = "all";
  dropEffect = "none";
  readonly setDragImage = vi.fn();

  get types(): readonly string[] {
    return [...this.#values.keys()];
  }

  setData(type: string, value: string): void {
    this.#values.set(type, value);
  }

  getData(type: string): string {
    return this.#values.get(type) ?? "";
  }
}
