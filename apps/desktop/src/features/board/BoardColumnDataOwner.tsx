import { useCallback, useEffect, useLayoutEffect, useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";

import { boardFiltersEqual, type BoardColumn, type BoardFilter, type SelectedWorkflowBoard } from "@/api";
import { errorMessage } from "@/api";
import { useProjectLabelCatalog } from "@/shared/labels";
import { useStableCallback, type VirtualizedInfiniteListBoundaryState } from "@/ui";
import { cardBelongsToColumn } from "./BoardCardMotionModel";
import { toKanbanCardVM, type KanbanCardVM } from "./BoardColumnViewModel";
import { useBoardFilterGeneration } from "./BoardFilterGenerationRuntime";
import { useBoardNodeCards } from "./useBoardData";

export type BoardColumnUpdateCause = "hydration" | "pagination" | "domain";

export type BoardColumnQueryDataSnapshot = Readonly<{
  cards: readonly KanbanCardVM[];
  generation: number;
  hasData: boolean;
  isFetching: boolean;
  isSettled: boolean;
  taskCount: number;
}>;

export type BoardColumnQuerySnapshot =
  | Readonly<{ cause: "deactivation" }>
  | Readonly<{
      cause: BoardColumnUpdateCause;
      data: BoardColumnQueryDataSnapshot;
    }>;

export type BoardColumnNoticeEvent =
  | Readonly<{
      kind: "failure";
      columnID: string;
      error: unknown;
      filter: BoardFilter;
      generation: number;
      noticeID: string;
      retry: () => void;
    }>
  | Readonly<{
      kind: "dismiss";
      noticeID: string;
    }>;

export type BoardColumnDataView = Readonly<{
  cards: readonly KanbanCardVM[];
  hasNextPage: boolean;
  hasPreviousPage: boolean;
  initialBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  isFetchingNextPage: boolean;
  isFetchingPreviousPage: boolean;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  onLoadMore: () => void;
  onLoadPrevious: () => void;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
}>;

export function BoardColumnDataOwner({
  board,
  column,
  onBoardColumnNotice,
  onCardsLoadError,
  onDataViewChange,
  onDataViewRelease,
  onReportColumnSnapshot,
}: Readonly<{
  board: SelectedWorkflowBoard;
  column: BoardColumn;
  onBoardColumnNotice: (event: BoardColumnNoticeEvent) => void;
  onCardsLoadError: (error: unknown) => void;
  onDataViewChange: (view: BoardColumnDataView) => void;
  onDataViewRelease: () => void;
  onReportColumnSnapshot: (columnID: string, snapshot: BoardColumnQuerySnapshot) => void;
}>) {
  const { t } = useTranslation();
  const labelCatalog = useProjectLabelCatalog();
  const filterGeneration = useBoardFilterGeneration();
  const stableOnBoardColumnNotice = useStableCallback(onBoardColumnNotice);
  const stableOnCardsLoadError = useStableCallback(onCardsLoadError);
  const stableOnDataViewChange = useStableCallback(onDataViewChange);
  const stableOnDataViewRelease = useStableCallback(onDataViewRelease);
  const stableOnReportColumnSnapshot = useStableCallback(onReportColumnSnapshot);
  const activeFilterGeneration = filterGeneration.snapshot.active;
  const cardsQuery = useBoardNodeCards(board.projectID, board.selectedWorkflow.id, column.id, true);
  const generationRef = useRef(0);
  const hydratedFilterGenerationRef = useRef<number | null>(null);
  const paginationInFlightRef = useRef(false);
  const queryCards = useMemo(
    () => cardsQuery.data?.pages.flatMap((page) => page.cards) ?? [],
    [cardsQuery.data?.pages],
  );
  const workspaceContext = useMemo(
    () => ({
      attachedWorkspaceCount: board.attachedWorkspaceCount,
      defaultWorkspaceID: board.defaultWorkspaceID,
    }),
    [board.attachedWorkspaceCount, board.defaultWorkspaceID],
  );
  const cardVMs = useMemo(
    () =>
      queryCards
        .map((card) => toKanbanCardVM(card, workspaceContext, labelCatalog.data ?? null))
        .filter((card) => cardBelongsToColumn(column, card)),
    [column, labelCatalog.data, queryCards, workspaceContext],
  );
  const {
    error,
    fetchNextPage,
    fetchPreviousPage,
    hasNextPage,
    hasPreviousPage,
    isError,
    isFetchNextPageError,
    isFetchPreviousPageError,
    isFetching,
    isFetchingNextPage,
    isFetchingPreviousPage,
    isPlaceholderData,
    isPending,
    refetch,
  } = cardsQuery;
  const requestEnabled = !activeFilterGeneration.retiring && filterGeneration.snapshot.desiredFilter === null;
  const paginationEnabled = requestEnabled && !isPlaceholderData && cardsQuery.data !== undefined;
  const retryInitial = useCallback(() => {
    if (requestEnabled) {
      void refetch();
    }
  }, [refetch, requestEnabled]);
  const mountedRef = useRef(true);
  const noticeRef = useRef<Readonly<{ generation: number; noticeID: string }> | null>(null);
  const noticeID = boardColumnNoticeID(
    board.projectID,
    board.selectedWorkflow.id,
    column.id,
    activeFilterGeneration.generation,
  );
  const retryReplacement = useCallback(() => {
    if (!mountedRef.current) {
      return;
    }
    const current = filterGeneration.controller.getSnapshot();
    if (
      current.active.generation !== activeFilterGeneration.generation ||
      current.active.retiring ||
      current.desiredFilter !== null ||
      !boardFiltersEqual(current.active.filter, activeFilterGeneration.filter)
    ) {
      return;
    }
    void refetch();
  }, [
    activeFilterGeneration.filter,
    activeFilterGeneration.generation,
    filterGeneration.controller,
    refetch,
  ]);
  const loadNewer = useCallback(() => {
    if (paginationEnabled && hasPreviousPage && !isFetchingPreviousPage) {
      void fetchPreviousPage();
    }
  }, [fetchPreviousPage, hasPreviousPage, isFetchingPreviousPage, paginationEnabled]);
  const loadOlder = useCallback(() => {
    if (paginationEnabled && hasNextPage && !isFetchingNextPage) {
      void fetchNextPage();
    }
  }, [fetchNextPage, hasNextPage, isFetchingNextPage, paginationEnabled]);
  const initialBoundary = useMemo<VirtualizedInfiniteListBoundaryState | undefined>(
    () =>
      cardsQuery.data === undefined
        ? isError
          ? {
              state: "error",
              message: errorMessage(error),
              retryLabel: t("app.retry"),
              onRetry: retryInitial,
            }
          : {
              state: "loading",
              label: t("states.loading"),
            }
        : undefined,
    [cardsQuery.data, error, isError, retryInitial, t],
  );
  const previousBoundary = useMemo(
    () =>
      directionalBoundary({
        error,
        failed: isFetchPreviousPageError,
        loading: isFetchingPreviousPage,
        loadingLabel: t("app.loadingMore"),
        onRetry: loadNewer,
        retryLabel: t("app.retry"),
      }),
    [error, isFetchPreviousPageError, isFetchingPreviousPage, loadNewer, t],
  );
  const nextBoundary = useMemo(
    () =>
      directionalBoundary({
        error,
        failed: isFetchNextPageError,
        loading: isFetchingNextPage,
        loadingLabel: t("app.loadingMore"),
        onRetry: loadOlder,
        retryLabel: t("app.retry"),
      }),
    [error, isFetchNextPageError, isFetchingNextPage, loadOlder, t],
  );
  const dataView = useMemo<BoardColumnDataView>(
    () => ({
      cards: cardVMs,
      hasNextPage: paginationEnabled && hasNextPage,
      hasPreviousPage: paginationEnabled && hasPreviousPage,
      initialBoundary,
      isFetchingNextPage: paginationEnabled && isFetchingNextPage,
      isFetchingPreviousPage: paginationEnabled && isFetchingPreviousPage,
      nextBoundary: paginationEnabled ? nextBoundary : undefined,
      onLoadMore: loadOlder,
      onLoadPrevious: loadNewer,
      previousBoundary: paginationEnabled ? previousBoundary : undefined,
    }),
    [
      cardVMs,
      hasNextPage,
      hasPreviousPage,
      initialBoundary,
      isFetchingNextPage,
      isFetchingPreviousPage,
      loadOlder,
      loadNewer,
      nextBoundary,
      paginationEnabled,
      previousBoundary,
    ],
  );

  useLayoutEffect(() => {
    stableOnDataViewChange(dataView);
  }, [dataView, stableOnDataViewChange]);

  useEffect(() => {
    if (isError && !isPlaceholderData) {
      stableOnCardsLoadError(error);
    }
  }, [error, isError, isPlaceholderData, stableOnCardsLoadError]);

  useEffect(() => {
    const notice = noticeRef.current;
    if (
      notice !== null &&
      (notice.generation !== activeFilterGeneration.generation ||
        activeFilterGeneration.retiring ||
        filterGeneration.snapshot.desiredFilter !== null)
    ) {
      stableOnBoardColumnNotice({ kind: "dismiss", noticeID: notice.noticeID });
      noticeRef.current = null;
    }
  }, [
    activeFilterGeneration.generation,
    activeFilterGeneration.retiring,
    filterGeneration.snapshot.desiredFilter,
    stableOnBoardColumnNotice,
  ]);

  useEffect(() => {
    if (isError && isPlaceholderData && cardsQuery.data !== undefined && requestEnabled) {
      noticeRef.current = { generation: activeFilterGeneration.generation, noticeID };
      stableOnBoardColumnNotice({
        kind: "failure",
        columnID: column.id,
        error,
        filter: activeFilterGeneration.filter,
        generation: activeFilterGeneration.generation,
        noticeID,
        retry: retryReplacement,
      });
      return;
    }
    const notice = noticeRef.current;
    if (notice !== null && notice.noticeID === noticeID && !isFetching && !isError && !isPlaceholderData) {
      stableOnBoardColumnNotice({ kind: "dismiss", noticeID: notice.noticeID });
      noticeRef.current = null;
    }
  }, [
    activeFilterGeneration.filter,
    activeFilterGeneration.generation,
    cardsQuery.data,
    column.id,
    error,
    isError,
    isFetching,
    isPlaceholderData,
    noticeID,
    stableOnBoardColumnNotice,
    requestEnabled,
    retryReplacement,
  ]);

  useEffect(() => {
    if (isFetchingPreviousPage || isFetchingNextPage || isFetchPreviousPageError || isFetchNextPageError) {
      paginationInFlightRef.current = true;
    }
  }, [isFetchNextPageError, isFetchPreviousPageError, isFetchingNextPage, isFetchingPreviousPage]);

  useEffect(() => {
    const activeFilterGeneration = filterGeneration.snapshot.active.generation;
    const cause: BoardColumnUpdateCause =
      hydratedFilterGenerationRef.current !== activeFilterGeneration
        ? "hydration"
        : paginationInFlightRef.current
          ? "pagination"
          : "domain";
    generationRef.current += 1;
    stableOnReportColumnSnapshot(column.id, {
      cause,
      data: {
        cards: cardVMs,
        generation: generationRef.current,
        hasData: cardsQuery.data !== undefined,
        isFetching,
        isSettled: !isPending && !isFetching,
        taskCount: column.taskCount,
      },
    });
    if (cardsQuery.data !== undefined && !isPlaceholderData) {
      hydratedFilterGenerationRef.current = activeFilterGeneration;
    }
    if (!isFetchingPreviousPage && !isFetchingNextPage) {
      paginationInFlightRef.current = false;
    }
  }, [
    cardVMs,
    cardsQuery.data,
    column.id,
    column.taskCount,
    filterGeneration.snapshot.active.generation,
    isFetching,
    isFetchingNextPage,
    isFetchingPreviousPage,
    isPending,
    isPlaceholderData,
    stableOnReportColumnSnapshot,
  ]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      const notice = noticeRef.current;
      if (notice !== null) {
        stableOnBoardColumnNotice({ kind: "dismiss", noticeID: notice.noticeID });
        noticeRef.current = null;
      }
      stableOnDataViewRelease();
      stableOnReportColumnSnapshot(column.id, { cause: "deactivation" });
    };
  }, [column.id, stableOnBoardColumnNotice, stableOnDataViewRelease, stableOnReportColumnSnapshot]);

  return null;
}

function boardColumnNoticeID(
  projectID: string,
  workflowID: string,
  columnID: string,
  generation: number,
): string {
  return `board-cards:${projectID}:${workflowID}:${columnID}:${generation.toString()}`;
}

function directionalBoundary(
  input: Readonly<{
    error: unknown;
    failed: boolean;
    loading: boolean;
    loadingLabel: string;
    onRetry: () => void;
    retryLabel: string;
  }>,
): VirtualizedInfiniteListBoundaryState | undefined {
  if (input.failed) {
    return {
      state: "error",
      message: errorMessage(input.error),
      retryLabel: input.retryLabel,
      onRetry: input.onRetry,
    };
  }
  if (input.loading) {
    return { state: "loading", label: input.loadingLabel };
  }
  return undefined;
}
