import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent,
  type RefObject,
} from "react";
import { flushSync } from "react-dom";

import type { BoardColumn, SelectedWorkflowBoard } from "../../api";
import { chromeContentPaddingClassName } from "../../ui/chromePadding";
import { type BoardColumnQueryDataSnapshot, type BoardColumnQuerySnapshot } from "./BoardColumnDataOwner";
import { BoardColumnMotionBoundary } from "./BoardColumnMotionBoundary";
import { runBoardCardMotionTransition } from "./BoardCardMotionAnimator";
import { BoardCardMotionContext, type BoardCardMotionContextValue } from "./BoardCardMotionContext";
import { BoardCardVisibilityContext, BoardCardVisibilityStore } from "./BoardCardVisibilityRegistry";
import { KanbanGroup } from "./BoardColumns";
import {
  boardCardColumnCountSnapshot,
  boardCardMotionParticipants,
  boardCardColumnIDsWithCards,
  boardCardSnapshotsEqual,
  boardCardSnapshotFromEntries,
  boardRailLayoutSignature,
  dirtyBoardCardCountColumnIDs,
  dirtyBoardCardColumnIDs,
  type BoardCardColumnCountSnapshot,
  type BoardCardColumnsSnapshot,
  type PendingBoardCardMove,
} from "./BoardCardMotionModel";
import { toKanbanGroupVM, type KanbanCardVM } from "./BoardColumnViewModel";
import type { ActiveBoardCardDrag } from "./BoardDragState";
import type { BoardColumnDropState } from "./BoardDragTypes";
import { boardSections } from "./BoardModel";

type BoardMotionPhase = "idle" | "arming" | "running";

type ArmedTransition = Readonly<{
  attemptID: number;
  layoutSignature: string;
  runtimeGeneration: number;
  namesByCardID: ReadonlyMap<string, string>;
  nextDisplayed: BoardCardColumnsSnapshot;
  revealCardIDs: ReadonlySet<string>;
}>;

type DisplayedSnapshot = Readonly<{
  columnCounts: BoardCardColumnCountSnapshot;
  layoutSignature: string;
  columns: BoardCardColumnsSnapshot;
}>;

export type BoardRailMotionControllerProps = Readonly<{
  activeDrag: ActiveBoardCardDrag | null;
  actionsDisabled: boolean;
  board: SelectedWorkflowBoard;
  columnDropState: (column: BoardColumn) => BoardColumnDropState;
  columnIsCollapsed: (column: BoardColumn) => boolean;
  firstActiveID: string | undefined;
  onCardClick: (taskID: string) => void;
  onCardDragEnd: () => void;
  onCardDragStart: (drag: ActiveBoardCardDrag) => void;
  onCardsLoadError: (error: unknown) => void;
  onDeleteTask: (taskID: string) => void;
  onDropTask: (event: DragEvent<HTMLElement>, column: BoardColumn) => void;
  onExpandColumn: (columnID: string) => void;
  onInterruptedRunObserved: (input: Readonly<{ runID: string; taskID: string }>) => void;
  onInterruptTask: (taskID: string) => void;
  onRegisterColumnScrollport: (columnID: string, element: HTMLElement | null) => void;
  onResumeTask: (taskID: string) => void;
  pendingCardMove: PendingBoardCardMove | null;
  scrollportRef: RefObject<HTMLDivElement | null>;
}>;

const staleSnapshotTimeoutMs = 900;
const emptyColumnsSnapshot: BoardCardColumnsSnapshot = new Map();
const emptyPendingMoveColumnIDs: ReadonlySet<string> = new Set();

export function BoardRailMotionController({
  activeDrag,
  actionsDisabled,
  board,
  columnDropState,
  columnIsCollapsed,
  firstActiveID,
  onCardClick,
  onCardDragEnd,
  onCardDragStart,
  onCardsLoadError,
  onDeleteTask,
  onDropTask,
  onExpandColumn,
  onInterruptedRunObserved,
  onInterruptTask,
  onRegisterColumnScrollport,
  onResumeTask,
  pendingCardMove,
  scrollportRef,
}: BoardRailMotionControllerProps) {
  const sections = useMemo(() => boardSections(board), [board]);
  const layoutSignature = useMemo(
    () => boardRailLayoutSignature(board, sections, firstActiveID),
    [board, firstActiveID, sections],
  );
  const boardColumnCounts = useMemo(() => boardCardColumnCountSnapshot(board), [board]);
  const [displayedSnapshot, setDisplayedSnapshot] = useState<DisplayedSnapshot>(() => ({
    columnCounts: boardColumnCounts,
    columns: new Map(),
    layoutSignature,
  }));
  const [activeNamesByCardID, setActiveNamesByCardID] = useState<ReadonlyMap<string, string>>(
    () => new Map(),
  );
  const [revealCardIDs, setRevealCardIDs] = useState<ReadonlySet<string>>(() => new Set());
  const [armedTransition, setArmedTransition] = useState<ArmedTransition | null>(null);
  const [heldExpandedColumnIDs, setHeldExpandedColumnIDs] = useState<ReadonlySet<string>>(() => new Set());
  const [columnVersion, setColumnVersion] = useState(0);
  const displayedColumns =
    displayedSnapshot.layoutSignature === layoutSignature ? displayedSnapshot.columns : emptyColumnsSnapshot;
  const displayedColumnCounts =
    displayedSnapshot.layoutSignature === layoutSignature
      ? displayedSnapshot.columnCounts
      : boardColumnCounts;
  const pendingMoveColumnIDs = useMemo(() => {
    if (pendingCardMove === null) {
      return emptyPendingMoveColumnIDs;
    }
    return new Set([...boardCardColumnIDsWithCards(displayedColumns), pendingCardMove.targetColumnID]);
  }, [displayedColumns, pendingCardMove]);
  const latestColumnsRef = useRef<ReadonlyMap<string, BoardColumnQueryDataSnapshot>>(new Map());
  const displayedColumnsRef = useRef(displayedColumns);
  const displayedColumnCountsRef = useRef(displayedColumnCounts);
  const boardColumnCountsRef = useRef(boardColumnCounts);
  const columnElementsRef = useRef<ReadonlyMap<string, HTMLElement>>(new Map());
  const [cardVisibilityStore] = useState(() => new BoardCardVisibilityStore());
  const phaseRef = useRef<BoardMotionPhase>("idle");
  const followUpPendingRef = useRef(false);
  const attemptIDRef = useRef(0);
  const runtimeGenerationRef = useRef(0);
  const startedAttemptIDsRef = useRef<ReadonlySet<number>>(new Set());
  const timeoutRef = useRef<number | null>(null);
  const revealTimeoutsRef = useRef<ReadonlySet<number>>(new Set());
  const staleTimeoutDueRef = useRef(false);
  const layoutSignatureRef = useRef(layoutSignature);

  const latestSnapshot = useCallback(
    (): BoardCardColumnsSnapshot =>
      boardCardSnapshotFromEntries(
        Array.from(latestColumnsRef.current, ([columnID, snapshot]) => [columnID, snapshot.cards]),
      ),
    [],
  );

  const scheduleNextTransition = useCallback(
    (fromTimeout: boolean): void => {
      const nextDisplayed = latestSnapshot();
      const currentDisplayed = displayedColumnsRef.current;
      const currentCounts = displayedColumnCountsRef.current;
      const nextCounts = boardColumnCountsRef.current;
      const dirtyCountColumns = dirtyBoardCardCountColumnIDs(currentCounts, nextCounts);
      if (boardCardSnapshotsEqual(currentDisplayed, nextDisplayed) && dirtyCountColumns.length === 0) {
        clearStaleSnapshotTimer(timeoutRef);
        return;
      }
      const dirtyCountColumnSet = new Set(dirtyCountColumns);
      const dirtyColumns = unionColumnIDs(
        dirtyBoardCardColumnIDs(currentDisplayed, nextDisplayed),
        dirtyCountColumns,
      );
      const dirtySettled = dirtyColumns.every((columnID) =>
        columnSnapshotSettledForBoardCount(
          latestColumnsRef.current.get(columnID),
          nextCounts.get(columnID) ?? 0,
          dirtyCountColumnSet.has(columnID),
        ),
      );
      if (!dirtySettled && !fromTimeout) {
        clearStaleSnapshotTimer(timeoutRef);
        timeoutRef.current = window.setTimeout(() => {
          timeoutRef.current = null;
          staleTimeoutDueRef.current = true;
          setColumnVersion((version) => version + 1);
        }, staleSnapshotTimeoutMs);
        return;
      }
      clearStaleSnapshotTimer(timeoutRef);
      const participants = boardCardMotionParticipants(
        currentDisplayed,
        nextDisplayed,
        cardVisibilityStore.visibleTaskIDs(),
      );
      if (participants.namesByCardID.size === 0) {
        displayedColumnsRef.current = nextDisplayed;
        displayedColumnCountsRef.current = nextCounts;
        setDisplayedSnapshot({ columnCounts: nextCounts, columns: nextDisplayed, layoutSignature });
        setRevealCardIDs(participants.revealCardIDs);
        scheduleRevealClear(revealTimeoutsRef, participants.revealCardIDs, setRevealCardIDs);
        return;
      }
      const attemptID = attemptIDRef.current + 1;
      attemptIDRef.current = attemptID;
      phaseRef.current = "arming";
      setHeldExpandedColumnIDs(boardCardColumnIDsWithCards(currentDisplayed));
      setActiveNamesByCardID(participants.namesByCardID);
      setArmedTransition({
        attemptID,
        layoutSignature,
        runtimeGeneration: runtimeGenerationRef.current,
        namesByCardID: participants.namesByCardID,
        nextDisplayed,
        revealCardIDs: participants.revealCardIDs,
      });
    },
    [cardVisibilityStore, latestSnapshot, layoutSignature],
  );

  useLayoutEffect(() => {
    boardColumnCountsRef.current = boardColumnCounts;
  }, [boardColumnCounts, layoutSignature]);

  useEffect(() => {
    queueMicrotask(() => {
      if (layoutSignatureRef.current === layoutSignature) {
        setColumnVersion((version) => version + 1);
      }
    });
  }, [boardColumnCounts, layoutSignature]);

  useLayoutEffect(() => {
    if (layoutSignatureRef.current !== layoutSignature) {
      layoutSignatureRef.current = layoutSignature;
      runtimeGenerationRef.current += 1;
      attemptIDRef.current += 1;
      clearStaleSnapshotTimer(timeoutRef);
      clearRevealTimers(revealTimeoutsRef);
      staleTimeoutDueRef.current = false;
      phaseRef.current = "idle";
      followUpPendingRef.current = false;
      latestColumnsRef.current = new Map();
      setActiveNamesByCardID(new Map());
      setRevealCardIDs(new Set());
      setHeldExpandedColumnIDs(new Set());
      setArmedTransition(null);
      const nextDisplayed = new Map<string, readonly KanbanCardVM[]>();
      displayedColumnsRef.current = nextDisplayed;
      displayedColumnCountsRef.current = boardColumnCounts;
      setDisplayedSnapshot({ columnCounts: boardColumnCounts, columns: nextDisplayed, layoutSignature });
    }
  }, [boardColumnCounts, latestSnapshot, layoutSignature]);

  useEffect(() => {
    if (displayedSnapshot.layoutSignature === layoutSignature) {
      displayedColumnsRef.current = displayedSnapshot.columns;
      displayedColumnCountsRef.current = displayedSnapshot.columnCounts;
    }
  }, [displayedSnapshot, layoutSignature]);

  useLayoutEffect(() => {
    return () => {
      runtimeGenerationRef.current += 1;
      attemptIDRef.current += 1;
      clearStaleSnapshotTimer(timeoutRef);
      clearRevealTimers(revealTimeoutsRef);
      staleTimeoutDueRef.current = false;
      cardVisibilityStore.destroy();
    };
  }, [cardVisibilityStore]);

  const reportColumnSnapshot = useCallback(
    (columnID: string, snapshot: BoardColumnQuerySnapshot): void => {
      const current = latestColumnsRef.current;
      if (snapshot.cause === "deactivation") {
        const nextLatest = new Map(current);
        nextLatest.delete(columnID);
        latestColumnsRef.current = nextLatest;
        const nextDisplayed = new Map(displayedColumnsRef.current);
        nextDisplayed.delete(columnID);
        replaceDisplayedColumnsWithoutMotion(nextDisplayed);
        return;
      }
      const data = snapshot.data;
      const previous = current.get(columnID);
      if (previous !== undefined && previous.generation > data.generation) {
        return;
      }
      latestColumnsRef.current = new Map(current).set(columnID, data);
      if (snapshot.cause !== "domain" || !displayedColumnsRef.current.has(columnID)) {
        replaceDisplayedColumnsWithoutMotion(new Map(displayedColumnsRef.current).set(columnID, data.cards));
        return;
      }
      setColumnVersion((version) => version + 1);

      function replaceDisplayedColumnsWithoutMotion(nextDisplayed: BoardCardColumnsSnapshot): void {
        if (phaseRef.current !== "idle") {
          runtimeGenerationRef.current += 1;
          attemptIDRef.current += 1;
          phaseRef.current = "idle";
          followUpPendingRef.current = false;
          setActiveNamesByCardID(new Map());
          setHeldExpandedColumnIDs(new Set());
          setArmedTransition(null);
        }
        displayedColumnsRef.current = nextDisplayed;
        setDisplayedSnapshot({
          columnCounts: displayedColumnCountsRef.current,
          columns: nextDisplayed,
          layoutSignature,
        });
      }
    },
    [layoutSignature],
  );

  useEffect(() => {
    if (columnVersion === 0 || phaseRef.current !== "idle") {
      if (phaseRef.current === "arming" || phaseRef.current === "running") {
        followUpPendingRef.current = true;
      }
      return;
    }
    const fromTimeout = staleTimeoutDueRef.current;
    staleTimeoutDueRef.current = false;
    scheduleNextTransition(fromTimeout);
  }, [columnVersion, scheduleNextTransition]);

  useLayoutEffect(() => {
    if (armedTransition === null) {
      return;
    }
    const startedAttemptIDs = startedAttemptIDsRef.current;
    if (startedAttemptIDs.has(armedTransition.attemptID)) {
      return;
    }
    startedAttemptIDsRef.current = new Set(startedAttemptIDs).add(armedTransition.attemptID);
    queueMicrotask(() => {
      if (
        armedTransition.attemptID !== attemptIDRef.current ||
        armedTransition.layoutSignature !== layoutSignatureRef.current ||
        armedTransition.runtimeGeneration !== runtimeGenerationRef.current
      ) {
        return;
      }
      phaseRef.current = "running";
      void runBoardCardMotionTransition({
        cardElementForTaskID: (taskID) => cardVisibilityStore.elementForUniqueTask(taskID),
        columnElementsRef,
        namesByCardID: armedTransition.namesByCardID,
        pendingCardMove,
        update: () => {
          flushSync(() => {
            displayedColumnsRef.current = armedTransition.nextDisplayed;
            displayedColumnCountsRef.current = boardColumnCountsRef.current;
            setDisplayedSnapshot({
              columnCounts: boardColumnCountsRef.current,
              columns: armedTransition.nextDisplayed,
              layoutSignature: armedTransition.layoutSignature,
            });
            setRevealCardIDs(armedTransition.revealCardIDs);
          });
        },
      }).finally(() => {
        if (
          armedTransition.attemptID !== attemptIDRef.current ||
          armedTransition.layoutSignature !== layoutSignatureRef.current ||
          armedTransition.runtimeGeneration !== runtimeGenerationRef.current
        ) {
          return;
        }
        phaseRef.current = "idle";
        setArmedTransition(null);
        setActiveNamesByCardID(new Map());
        setHeldExpandedColumnIDs(new Set());
        scheduleRevealClear(revealTimeoutsRef, armedTransition.revealCardIDs, setRevealCardIDs);
        if (followUpPendingRef.current) {
          followUpPendingRef.current = false;
          scheduleNextTransition(false);
        }
      });
    });
  }, [armedTransition, cardVisibilityStore, pendingCardMove, scheduleNextTransition]);

  const registerCard = useCallback(
    (instance: Readonly<{ columnID: string; taskID: string }>, element: HTMLElement | null) => {
      cardVisibilityStore.register(instance, element);
    },
    [cardVisibilityStore],
  );

  const registerColumn = useCallback((columnID: string, element: HTMLElement | null) => {
    const current = columnElementsRef.current;
    const next = new Map(current);
    if (element === null) {
      next.delete(columnID);
    } else {
      next.set(columnID, element);
    }
    columnElementsRef.current = next;
  }, []);

  const motionContext = useMemo<BoardCardMotionContextValue>(
    () => ({
      cardClassName(cardID) {
        return revealCardIDs.has(cardID) ? "board-card-enter-reveal" : undefined;
      },
      cardStyle(cardID) {
        const transitionName = activeNamesByCardID.get(cardID);
        return transitionName === undefined ? undefined : { viewTransitionName: transitionName };
      },
      registerCard,
    }),
    [activeNamesByCardID, registerCard, revealCardIDs],
  );

  function effectiveColumnIsCollapsed(column: BoardColumn): boolean {
    return (
      columnIsCollapsed(column) &&
      !heldExpandedColumnIDs.has(column.id) &&
      !pendingMoveColumnIDs.has(column.id)
    );
  }

  return (
    <BoardCardVisibilityContext.Provider value={cardVisibilityStore}>
      <BoardCardMotionContext.Provider value={motionContext}>
        <div
          className={`flex h-full min-h-0 w-max min-w-full gap-[var(--space-2)] ${chromeContentPaddingClassName}`}
          data-testid="board-column-rail"
        >
          {sections.map((section) =>
            section.kind === "group" ? (
              <KanbanGroup
                group={toKanbanGroupVM(section.group)}
                hideHeader={section.columns.every(effectiveColumnIsCollapsed)}
                key={section.id}
              >
                {section.columns.map((column) => (
                  <BoardColumnMotionBoundary
                    activeDrag={activeDrag}
                    actionsDisabled={actionsDisabled}
                    board={board}
                    displayedCards={displayedColumns.get(column.id)}
                    column={column}
                    dropState={columnDropState(column)}
                    isCollapsed={effectiveColumnIsCollapsed(column)}
                    isFirstActive={column.id === firstActiveID}
                    key={`${board.projectID}:${board.selectedWorkflow.id}:${column.id}`}
                    latestIsCollapsed={effectiveColumnIsCollapsed(column)}
                    onCardClick={onCardClick}
                    onCardDragEnd={onCardDragEnd}
                    onCardDragStart={onCardDragStart}
                    onCardsLoadError={onCardsLoadError}
                    onDeleteTask={onDeleteTask}
                    onDropTask={onDropTask}
                    onExpandColumn={onExpandColumn}
                    onInterruptedRunObserved={onInterruptedRunObserved}
                    onInterruptTask={onInterruptTask}
                    onReportColumnSnapshot={reportColumnSnapshot}
                    onRegisterColumn={registerColumn}
                    onRegisterColumnScrollport={onRegisterColumnScrollport}
                    onResumeTask={onResumeTask}
                    scrollportRef={scrollportRef}
                  />
                ))}
              </KanbanGroup>
            ) : (
              <BoardColumnMotionBoundary
                activeDrag={activeDrag}
                actionsDisabled={actionsDisabled}
                board={board}
                displayedCards={displayedColumns.get(section.column.id)}
                column={section.column}
                dropState={columnDropState(section.column)}
                isCollapsed={effectiveColumnIsCollapsed(section.column)}
                isFirstActive={section.column.id === firstActiveID}
                key={`${board.projectID}:${board.selectedWorkflow.id}:${section.id}`}
                latestIsCollapsed={effectiveColumnIsCollapsed(section.column)}
                onCardClick={onCardClick}
                onCardDragEnd={onCardDragEnd}
                onCardDragStart={onCardDragStart}
                onCardsLoadError={onCardsLoadError}
                onDeleteTask={onDeleteTask}
                onDropTask={onDropTask}
                onExpandColumn={onExpandColumn}
                onInterruptedRunObserved={onInterruptedRunObserved}
                onInterruptTask={onInterruptTask}
                onReportColumnSnapshot={reportColumnSnapshot}
                onRegisterColumn={registerColumn}
                onRegisterColumnScrollport={onRegisterColumnScrollport}
                onResumeTask={onResumeTask}
                scrollportRef={scrollportRef}
              />
            ),
          )}
        </div>
      </BoardCardMotionContext.Provider>
    </BoardCardVisibilityContext.Provider>
  );
}

function clearStaleSnapshotTimer(timeoutRef: { current: number | null }): void {
  if (timeoutRef.current === null) {
    return;
  }
  window.clearTimeout(timeoutRef.current);
  timeoutRef.current = null;
}

function scheduleRevealClear(
  revealTimeoutsRef: { current: ReadonlySet<number> },
  revealCardIDs: ReadonlySet<string>,
  setRevealCardIDs: (update: (current: ReadonlySet<string>) => ReadonlySet<string>) => void,
): void {
  const timeout = window.setTimeout(() => {
    revealTimeoutsRef.current = withoutTimeout(revealTimeoutsRef.current, timeout);
    setRevealCardIDs((current) => (current === revealCardIDs ? new Set() : current));
  }, 420);
  revealTimeoutsRef.current = new Set(revealTimeoutsRef.current).add(timeout);
}

function clearRevealTimers(revealTimeoutsRef: { current: ReadonlySet<number> }): void {
  for (const timeout of revealTimeoutsRef.current) {
    window.clearTimeout(timeout);
  }
  revealTimeoutsRef.current = new Set();
}

function withoutTimeout(timeouts: ReadonlySet<number>, timeout: number): ReadonlySet<number> {
  const next = new Set(timeouts);
  next.delete(timeout);
  return next;
}

function unionColumnIDs(left: readonly string[], right: readonly string[]): readonly string[] {
  return Array.from(new Set([...left, ...right]));
}

function columnSnapshotSettledForBoardCount(
  snapshot: BoardColumnQueryDataSnapshot | undefined,
  taskCount: number,
  countDirty: boolean,
): boolean {
  if (snapshot === undefined) {
    return !countDirty || taskCount === 0;
  }
  if (taskCount > 0 && snapshot.cards.length === 0) {
    return false;
  }
  return (
    snapshot.isSettled &&
    (!countDirty || snapshot.taskCount === taskCount) &&
    (!countDirty || taskCount === 0 || snapshot.hasData)
  );
}
