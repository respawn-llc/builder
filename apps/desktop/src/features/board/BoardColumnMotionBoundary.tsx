import {
  useCallback,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent,
  type RefObject,
} from "react";
import { useTranslation } from "react-i18next";

import type { BoardColumn, SelectedWorkflowBoard } from "../../api";
import { useStableCallback, type VirtualizedInfiniteListBoundaryState } from "../../ui";
import {
  BoardColumnDataOwner,
  type BoardColumnDataView,
  type BoardColumnQuerySnapshot,
} from "./BoardColumnDataOwner";
import { boardCardInstanceKey } from "./BoardCardInstance";
import type { KanbanCardVM } from "./BoardColumnViewModel";
import { toKanbanColumnVM } from "./BoardColumnViewModel";
import { KanbanColumn } from "./BoardColumns";
import type { ActiveBoardCardDrag } from "./BoardDragState";
import type { BoardColumnDropState } from "./BoardDragTypes";
import { useColumnVisibility } from "./useColumnVisibility";

export type BoardColumnMotionBoundaryProps = Readonly<{
  activeDrag: ActiveBoardCardDrag | null;
  actionsDisabled: boolean;
  board: SelectedWorkflowBoard;
  displayedCards: readonly KanbanCardVM[] | undefined;
  column: BoardColumn;
  dropState: BoardColumnDropState;
  isCollapsed: boolean;
  isFirstActive: boolean;
  latestIsCollapsed: boolean;
  onCardClick: (taskID: string) => void;
  onCardDragEnd: () => void;
  onCardDragStart: (drag: ActiveBoardCardDrag) => void;
  onCardsLoadError: (error: unknown) => void;
  onDeleteTask: (taskID: string) => void;
  onDropTask: (event: DragEvent<HTMLElement>, column: BoardColumn) => void;
  onExpandColumn: (columnID: string) => void;
  onInterruptedRunObserved: (input: Readonly<{ runID: string; taskID: string }>) => void;
  onInterruptTask: (taskID: string) => void;
  onReportColumnSnapshot: (columnID: string, snapshot: BoardColumnQuerySnapshot) => void;
  onRegisterColumn: (columnID: string, element: HTMLElement | null) => void;
  onRegisterColumnScrollport: (columnID: string, element: HTMLElement | null) => void;
  onResumeTask: (taskID: string) => void;
  scrollportRef: RefObject<HTMLDivElement | null>;
}>;

type BoardColumnPresentation = Readonly<{
  hasMoreCards: boolean;
  hasPreviousCards: boolean;
  initialBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  isLoadingMoreCards: boolean;
  isLoadingPreviousCards: boolean;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
}>;

const inactivePresentation: BoardColumnPresentation = {
  hasMoreCards: false,
  hasPreviousCards: false,
  initialBoundary: undefined,
  isLoadingMoreCards: false,
  isLoadingPreviousCards: false,
  nextBoundary: undefined,
  previousBoundary: undefined,
};

export function BoardColumnMotionBoundary({
  activeDrag,
  actionsDisabled,
  board,
  displayedCards,
  column,
  dropState,
  isCollapsed,
  isFirstActive,
  latestIsCollapsed,
  onCardClick,
  onCardDragEnd,
  onCardDragStart,
  onCardsLoadError,
  onDeleteTask,
  onDropTask,
  onExpandColumn,
  onInterruptedRunObserved,
  onInterruptTask,
  onReportColumnSnapshot,
  onRegisterColumn,
  onRegisterColumnScrollport,
  onResumeTask,
  scrollportRef,
}: BoardColumnMotionBoundaryProps) {
  const { t } = useTranslation();
  const [columnElement, setColumnElement] = useState<HTMLElement | null>(null);
  const [dataView, setDataView] = useState<BoardColumnDataView | null>(null);
  const columnScrollElementRef = useRef<HTMLElement | null>(null);
  const wasDataOwnerActiveRef = useRef(false);
  const setRegisteredColumnElement = useCallback(
    (element: HTMLElement | null): void => {
      setColumnElement(element);
      onRegisterColumn(column.id, element);
    },
    [column.id, onRegisterColumn],
  );
  const setRegisteredScrollElement = useCallback(
    (element: HTMLElement | null): void => {
      columnScrollElementRef.current = element;
      onRegisterColumnScrollport(column.id, element);
    },
    [column.id, onRegisterColumnScrollport],
  );
  const isVisible = useColumnVisibility(scrollportRef, columnElement);
  const sourceDrag = activeDragForColumn(activeDrag, column.id);
  const dataOwnerActive = columnDataOwnerActive(isVisible, sourceDrag, latestIsCollapsed);
  const columnVM = useMemo(() => toKanbanColumnVM(column), [column]);
  const loadingDataView = useMemo<BoardColumnDataView>(
    () => ({
      cards: [],
      hasNextPage: false,
      hasPreviousPage: false,
      initialBoundary: { state: "loading", label: t("states.loading") },
      isFetchingNextPage: false,
      isFetchingPreviousPage: false,
      nextBoundary: undefined,
      onLoadMore: () => undefined,
      onLoadPrevious: () => undefined,
      previousBoundary: undefined,
    }),
    [t],
  );
  const activeDataView = dataView ?? loadingDataView;
  const renderedCards = useMemo(
    () =>
      presentedCards({
        active: dataOwnerActive,
        cards: displayedCards ?? activeDataView.cards,
        sourceDrag,
      }),
    [activeDataView.cards, dataOwnerActive, displayedCards, sourceDrag],
  );
  const pinnedItemKeys = useMemo(() => pinnedKeys(sourceDrag), [sourceDrag]);
  const presentation = presentedDataView(dataOwnerActive, activeDataView);
  const stableOnCardClick = useStableCallback(onCardClick);
  const stableOnCardDragEnd = useStableCallback(onCardDragEnd);
  const stableOnCardDragStart = useStableCallback(onCardDragStart);
  const stableOnDeleteTask = useStableCallback(onDeleteTask);
  const stableOnInterruptTask = useStableCallback(onInterruptTask);
  const stableOnResumeTask = useStableCallback(onResumeTask);

  useLayoutEffect(() => {
    const scrollElement = columnScrollElementRef.current;
    if (dataOwnerActive && !wasDataOwnerActiveRef.current && scrollElement !== null) {
      scrollElement.scrollTop = 0;
    }
    wasDataOwnerActiveRef.current = dataOwnerActive;
  }, [dataOwnerActive]);
  const releaseDataView = useCallback(() => {
    setDataView(null);
  }, []);

  return (
    <>
      {dataOwnerActive ? (
        <BoardColumnDataOwner
          board={board}
          column={column}
          onCardsLoadError={onCardsLoadError}
          onDataViewChange={setDataView}
          onDataViewRelease={releaseDataView}
          onInterruptedRunObserved={onInterruptedRunObserved}
          onReportColumnSnapshot={onReportColumnSnapshot}
        />
      ) : null}
      <KanbanColumn
        actionsDisabled={actionsDisabled}
        cards={renderedCards}
        column={columnVM}
        columnRef={setRegisteredColumnElement}
        scrollportRef={setRegisteredScrollElement}
        dropState={dropState}
        hasMoreCards={presentation.hasMoreCards}
        hasPreviousCards={presentation.hasPreviousCards}
        initialBoundary={presentation.initialBoundary}
        isCollapsed={isCollapsed}
        isFirstActive={isFirstActive}
        isLoadingMoreCards={presentation.isLoadingMoreCards}
        isLoadingPreviousCards={presentation.isLoadingPreviousCards}
        nextBoundary={presentation.nextBoundary}
        onCardClick={stableOnCardClick}
        onCardDragEnd={stableOnCardDragEnd}
        onCardDragStart={stableOnCardDragStart}
        onDeleteTask={stableOnDeleteTask}
        onDropTask={(event) => {
          onDropTask(event, column);
        }}
        onExpandColumn={() => {
          onExpandColumn(column.id);
        }}
        onInterruptTask={stableOnInterruptTask}
        onLoadMoreCards={activeDataView.onLoadMore}
        onLoadPreviousCards={activeDataView.onLoadPrevious}
        onResumeTask={stableOnResumeTask}
        pinnedItemKeys={pinnedItemKeys}
        previousBoundary={presentation.previousBoundary}
      />
    </>
  );
}

function activeDragForColumn(
  activeDrag: ActiveBoardCardDrag | null,
  columnID: string,
): ActiveBoardCardDrag | null {
  if (activeDrag?.instance.columnID !== columnID) {
    return null;
  }
  return activeDrag;
}

function columnDataOwnerActive(
  visible: boolean,
  sourceDrag: ActiveBoardCardDrag | null,
  collapsed: boolean,
): boolean {
  return (visible || sourceDrag !== null) && !collapsed;
}

function presentedDataView(active: boolean, view: BoardColumnDataView): BoardColumnPresentation {
  if (!active) {
    return inactivePresentation;
  }
  return {
    hasMoreCards: view.hasNextPage,
    hasPreviousCards: view.hasPreviousPage,
    initialBoundary: view.initialBoundary,
    isLoadingMoreCards: view.isFetchingNextPage,
    isLoadingPreviousCards: view.isFetchingPreviousPage,
    nextBoundary: view.nextBoundary,
    previousBoundary: view.previousBoundary,
  };
}

function presentedCards(
  input: Readonly<{
    active: boolean;
    cards: readonly KanbanCardVM[];
    sourceDrag: ActiveBoardCardDrag | null;
  }>,
): readonly KanbanCardVM[] {
  if (!input.active) {
    return [];
  }
  return retainDragSourceSnapshot(input.cards, input.sourceDrag);
}

function retainDragSourceSnapshot(
  cards: readonly KanbanCardVM[],
  sourceDrag: ActiveBoardCardDrag | null,
): readonly KanbanCardVM[] {
  if (sourceDrag === null || cards.some((card) => card.id === sourceDrag.instance.taskID)) {
    return cards;
  }
  const insertionIndex = Math.min(Math.max(sourceDrag.lastVirtualIndex, 0), cards.length);
  return [...cards.slice(0, insertionIndex), sourceDrag.snapshot, ...cards.slice(insertionIndex)];
}

function pinnedKeys(sourceDrag: ActiveBoardCardDrag | null): ReadonlySet<string> | undefined {
  return sourceDrag === null ? undefined : new Set([boardCardInstanceKey(sourceDrag.instance)]);
}
