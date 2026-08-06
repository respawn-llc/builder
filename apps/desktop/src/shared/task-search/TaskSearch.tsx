import { useInfiniteQuery, type InfiniteData } from "@tanstack/react-query";
import { SearchIcon } from "lucide-react";
import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
} from "react";
import { useTranslation } from "react-i18next";

import { TaskSearchError, errorMessage, type TaskSearchGroup, type TaskSearchResponse } from "@/api";
import { queryKeys, useAppServices, useRetainedQueryData, useTaskSearchMemory } from "@/app-facade";
import {
  Button,
  CommandPaletteDialog,
  ErrorState,
  InfiniteListBoundary,
  InteractiveChip,
  VirtualizedInfiniteList,
  type VirtualizedInfiniteListBoundaryState,
} from "@/ui";
import {
  adjacentSearchResult,
  useTaskSearchSelection,
  type TaskSearchScrollRequest,
} from "./taskSearchSelection";
import { TaskSearchResultRow, type TaskSearchResultItem as SearchResult } from "./TaskSearchResult";

const searchDebounceMs = 300;
const taskSearchPageSize = 40;
const retainedTaskSearchPages = 3;
const taskSearchContext = 20;
const taskSearchInputHeight = 56;
const taskSearchResultEstimatedHeight = 154;
const taskSearchResultAreaPadding = 16;
const taskSearchDialogMaximumHeight = 560;
const taskSearchLoadingDialogHeight = 176;
const taskSearchErrorDialogHeight = 240;

type SearchPage = Readonly<{
  offset: number | null;
  projectID: string | null;
  query: string;
  response: TaskSearchResponse;
}>;

export function BoardTaskSearchChrome({
  compact = false,
  enableShortcuts = true,
  projectID,
  onOpenTask,
}: Readonly<{
  compact?: boolean;
  enableShortcuts?: boolean;
  projectID: string | null;
  onOpenTask(taskID: string): void;
}>) {
  const { t } = useTranslation();
  const memory = useTaskSearchMemory();
  const [open, setOpen] = useState(false);
  const pendingTaskIDRef = useRef<string | null>(null);
  const query = memory.query;
  const debouncedQuery = useDebouncedText(query, searchDebounceMs);
  const search = useBoardTaskSearch(projectID, open, debouncedQuery);
  const selection = useTaskSearchSelection(projectID, search.displayedQuery, search.results);
  const revealActiveSelection = selection.revealActive;

  const close = useCallback((): void => {
    setOpen(false);
  }, []);
  const activate = useCallback(
    (result: SearchResult): void => {
      pendingTaskIDRef.current = result.group.taskID;
      close();
    },
    [close],
  );
  const completeClose = useCallback((): void => {
    const taskID = pendingTaskIDRef.current;
    pendingTaskIDRef.current = null;
    if (taskID !== null) {
      onOpenTask(taskID);
    }
  }, [onOpenTask]);
  const openSearch = useCallback((): void => {
    pendingTaskIDRef.current = null;
    revealActiveSelection();
    setOpen(true);
  }, [revealActiveSelection]);
  useTaskSearchShortcuts(enableShortcuts ? openSearch : null);
  const moveSelection = useCallback(
    (direction: -1 | 1): void => {
      const next = adjacentSearchResult(search.results, selection.activeKey, direction);
      if (next !== null) {
        selection.selectAndReveal(next.key);
      }
    },
    [search.results, selection],
  );

  return (
    <>
      <InteractiveChip
        aria-label={t("taskSearch.open")}
        onClick={openSearch}
        selected={open}
        style={{ paddingInline: "var(--space-3)" }}
        tone={open ? "primary" : "neutral"}
      >
        <SearchIcon aria-hidden="true" className="shrink-0" size={14} strokeWidth={1.8} />
        {compact ? null : t("taskSearch.open")}
      </InteractiveChip>
      <TaskSearchDialog
        onActivate={activate}
        onClose={close}
        onExitComplete={completeClose}
        onMoveSelection={moveSelection}
        onQueryChange={memory.setQuery}
        open={open}
        query={query}
        search={search}
        selection={selection}
      />
    </>
  );
}

function useTaskSearchShortcuts(onOpen: (() => void) | null): void {
  useEffect(() => {
    const handleKeyDown = (event: globalThis.KeyboardEvent): void => {
      if (onOpen === null || !isTaskSearchShortcut(event)) {
        return;
      }
      event.preventDefault();
      if (!event.repeat) {
        onOpen();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [onOpen]);
}

function isTaskSearchShortcut(event: globalThis.KeyboardEvent): boolean {
  const saveShortcut =
    (event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.code === "KeyS";
  const altSpaceShortcut =
    event.altKey && !event.metaKey && !event.ctrlKey && !event.shiftKey && event.code === "Space";
  return saveShortcut || altSpaceShortcut;
}

function useDebouncedText(value: string, delayMs: number): string {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => {
      setDebounced(value);
    }, delayMs);
    return () => {
      window.clearTimeout(timer);
    };
  }, [delayMs, value]);
  return debounced;
}

function useBoardTaskSearch(projectID: string | null, open: boolean, debouncedQuery: string) {
  const { api } = useAppServices();
  const trimmedQuery = debouncedQuery.trim();
  const searchable = Array.from(trimmedQuery).length >= 3;
  const request = useInfiniteQuery<
    SearchPage,
    Error,
    InfiniteData<SearchPage, number | null>,
    readonly (string | null)[],
    number | null
  >({
    queryKey: queryKeys.taskSearch(projectID, trimmedQuery),
    queryFn: async ({ pageParam }) => ({
      offset: pageParam,
      projectID,
      query: trimmedQuery,
      response: await api.searchTasks(
        {
          mode: "literal",
          query: trimmedQuery,
          context: taskSearchContext,
          caseSensitive: false,
          includeComments: true,
          ...(projectID === null ? {} : { projectIDs: [projectID] }),
          pageSize: taskSearchPageSize,
          offset: pageParam ?? undefined,
        },
      ),
    }),
    initialPageParam: null,
    enabled: open && searchable,
    getNextPageParam: (lastPage) => lastPage.response.nextOffset ?? undefined,
    maxPages: retainedTaskSearchPages,
    retry: (failureCount, error) => !(error instanceof TaskSearchError) && failureCount < 1,
  });
  const retainedData = useRetainedQueryData({ projectID }, request.data, sameTaskSearchProject);
  const normalizedTooShort = request.error instanceof TaskSearchError;
  const visibleData = searchable && !normalizedTooShort ? retainedData : undefined;
  const paginationUsesVisibleData = visibleData !== undefined && visibleData === request.data;
  const results = useMemo(() => flattenSearchResults(visibleData), [visibleData]);
  return {
    displayedQuery: visibleData?.pages[0]?.query ?? null,
    normalizedTooShort,
    paginationUsesVisibleData,
    request,
    results,
    searchable,
  };
}

function TaskSearchDialog({
  onActivate,
  onClose,
  onExitComplete,
  onMoveSelection,
  onQueryChange,
  open,
  query,
  search,
  selection,
}: Readonly<{
  onActivate(result: SearchResult): void;
  onClose(): void;
  onExitComplete(): void;
  onMoveSelection(direction: -1 | 1): void;
  onQueryChange(value: string): void;
  open: boolean;
  query: string;
  search: ReturnType<typeof useBoardTaskSearch>;
  selection: ReturnType<typeof useTaskSearchSelection>;
}>) {
  const { t } = useTranslation();
  const inputRef = useRef<HTMLInputElement | null>(null);
  const listID = useId();
  const resultsVisible = search.results.length > 0;
  const loadingVisible = search.searchable && search.request.isFetching && search.results.length === 0;
  const errorVisible = search.request.isError && !search.normalizedTooShort && search.results.length === 0;
  const expanded = resultsVisible || loadingVisible || errorVisible;
  useSmoothTaskSearchReveal(listID, selection.scrollRequest, selection.revealImmediately);
  return (
    <CommandPaletteDialog
      closeLabel={t("app.close")}
      expanded={expanded}
      height={taskSearchDialogHeight(search.results.length, loadingVisible, errorVisible)}
      input={
        <TaskSearchInput
          activeKey={selection.activeKey}
          inputRef={inputRef}
          listID={listID}
          onActivate={() => {
            if (selection.activeResult !== null) {
              onActivate(selection.activeResult);
            }
          }}
          onMoveSelection={onMoveSelection}
          onQueryChange={onQueryChange}
          popupExpanded={expanded}
          query={query}
          resultsVisible={resultsVisible}
        />
      }
      inputFocusRef={inputRef}
      onClose={onClose}
      onExitComplete={onExitComplete}
      open={open}
      title={t("taskSearch.title")}
    >
      <TaskSearchResults
        listID={listID}
        onActivate={onActivate}
        onSelect={selection.select}
        search={search}
        scrollRequest={selection.scrollRequest}
        selectedKey={selection.activeKey}
      />
    </CommandPaletteDialog>
  );
}

function useSmoothTaskSearchReveal(
  listID: string,
  request: TaskSearchScrollRequest | null,
  revealImmediately: (key: string) => void,
): void {
  useEffect(() => {
    if (request?.behavior !== "smooth") {
      return undefined;
    }
    const frame = window.requestAnimationFrame(() => {
      const result = document.getElementById(taskSearchOptionID(listID, request.key));
      if (result === null) {
        revealImmediately(request.key);
        return;
      }
      if (!(result.scrollIntoView instanceof Function)) {
        revealImmediately(request.key);
        return;
      }
      result.scrollIntoView({ behavior: "smooth", block: "nearest", inline: "nearest" });
    });
    return () => {
      window.cancelAnimationFrame(frame);
    };
  }, [listID, request, revealImmediately]);
}

function TaskSearchInput({
  activeKey,
  inputRef,
  listID,
  onActivate,
  onMoveSelection,
  onQueryChange,
  popupExpanded,
  query,
  resultsVisible,
}: Readonly<{
  activeKey: string | null;
  inputRef: RefObject<HTMLInputElement | null>;
  listID: string;
  onActivate(): void;
  onMoveSelection(direction: -1 | 1): void;
  onQueryChange(value: string): void;
  popupExpanded: boolean;
  query: string;
  resultsVisible: boolean;
}>) {
  const { t } = useTranslation();
  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>): void {
    const direction = searchSelectionDirection(event.key);
    if (direction !== null) {
      event.preventDefault();
      onMoveSelection(direction);
      return;
    }
    if (event.key === "Enter" && activeKey !== null) {
      event.preventDefault();
      onActivate();
    }
  }
  return (
    <div className="flex min-h-14 items-center gap-[var(--space-3)] px-[var(--space-4)] pr-[var(--space-7)]">
      <SearchIcon
        aria-hidden="true"
        className="shrink-0 text-[var(--color-muted)]"
        size={19}
        strokeWidth={1.7}
      />
      <input
        aria-activedescendant={activeKey === null ? undefined : taskSearchOptionID(listID, activeKey)}
        aria-controls={resultsVisible ? listID : undefined}
        aria-label={t("taskSearch.input")}
        aria-expanded={popupExpanded}
        aria-haspopup="listbox"
        autoComplete="off"
        className="min-w-0 flex-1 border-0 bg-transparent p-0 text-base text-[var(--color-on-island)] outline-none placeholder:text-[var(--color-muted)]"
        inputMode="search"
        onChange={(event) => {
          onQueryChange(event.currentTarget.value);
        }}
        onKeyDown={handleKeyDown}
        placeholder={t("taskSearch.input")}
        ref={inputRef}
        role="searchbox"
        spellCheck={false}
        type="text"
        value={query}
      />
    </div>
  );
}

function TaskSearchResults({
  listID,
  onActivate,
  onSelect,
  search,
  scrollRequest,
  selectedKey,
}: Readonly<{
  listID: string;
  onActivate(result: SearchResult): void;
  onSelect(key: string): void;
  search: ReturnType<typeof useBoardTaskSearch>;
  scrollRequest: TaskSearchScrollRequest | null;
  selectedKey: string | null;
}>) {
  const { t } = useTranslation();
  if (!search.searchable || search.normalizedTooShort) {
    return null;
  }
  if (search.request.isError && search.results.length === 0) {
    return (
      <ErrorState
        body={errorMessage(search.request.error)}
        onRetry={() => {
          void search.request.refetch();
        }}
        retryLabel={t("app.retry")}
        title={t("taskSearch.failed")}
      />
    );
  }
  if (search.request.isFetching && search.results.length === 0) {
    return (
      <div className="grid h-full place-items-center">
        <InfiniteListBoundary
          direction="initial"
          state={{ state: "loading", label: t("taskSearch.searching") }}
        />
      </div>
    );
  }
  if (search.results.length === 0) {
    return null;
  }
  return (
    <TaskSearchResultList
      listID={listID}
      onActivate={onActivate}
      onSelect={onSelect}
      search={search}
      scrollRequest={scrollRequest}
      selectedKey={selectedKey}
    />
  );
}

function TaskSearchResultList({
  listID,
  onActivate,
  onSelect,
  search,
  scrollRequest,
  selectedKey,
}: Readonly<{
  listID: string;
  onActivate(result: SearchResult): void;
  onSelect(key: string): void;
  search: ReturnType<typeof useBoardTaskSearch>;
  scrollRequest: TaskSearchScrollRequest | null;
  selectedKey: string | null;
}>) {
  const { t } = useTranslation();
  const listHeader = search.request.isError ? (
    <SearchRefreshError
      message={errorMessage(search.request.error)}
      onRetry={() => {
        void search.request.refetch();
      }}
    />
  ) : undefined;
  const boundaryCopy = {
    errorMessage: t("taskSearch.loadMoreFailed"),
    loadingLabel: t("taskSearch.searching"),
    retryLabel: t("app.retry"),
  };
  const lastPointerPositionRef = useRef<Readonly<{ x: number; y: number }> | null>(null);
  const selectFromPointer = useCallback(
    (key: string, event: ReactPointerEvent<HTMLElement>): void => {
      if (event.pointerType !== "mouse") {
        return;
      }
      const current = { x: event.clientX, y: event.clientY };
      const previous = lastPointerPositionRef.current;
      lastPointerPositionRef.current = current;
      if (
        (previous !== null && previous.x === current.x && previous.y === current.y) ||
        key === selectedKey
      ) {
        return;
      }
      onSelect(key);
    },
    [onSelect, selectedKey],
  );
  const immediateScrollRequest = scrollRequest?.behavior === "auto" ? scrollRequest : null;
  return (
    <VirtualizedInfiniteList
      ariaLabel={t("taskSearch.results")}
      className="h-full min-h-0 overflow-y-auto px-[var(--space-2)]"
      estimateSize={() => taskSearchResultEstimatedHeight}
      getItemKey={(result) => result.key}
      hasNextPage={search.paginationUsesVisibleData && search.request.hasNextPage}
      header={listHeader}
      id={listID}
      initialScrollAlign="auto"
      initialScrollKey={immediateScrollRequest?.key}
      initialScrollRequestKey={immediateScrollRequest?.requestID.toString()}
      isFetchingNextPage={search.paginationUsesVisibleData && search.request.isFetchingNextPage}
      items={search.results}
      loadMoreKey={search.paginationUsesVisibleData ? taskSearchNextOffset(search) : undefined}
      loadingLabel={t("taskSearch.searching")}
      nextBoundary={searchBoundaryState(search, boundaryCopy)}
      onLoadMore={() => {
        if (search.paginationUsesVisibleData) {
          void search.request.fetchNextPage();
        }
      }}
      paddingEnd={8}
      paddingStart={8}
      renderItem={(result) => (
        <TaskSearchResultRow
          active={result.key === selectedKey}
          id={taskSearchOptionID(listID, result.key)}
          onActivate={() => {
            onActivate(result);
          }}
          onPointerMove={(event) => {
            selectFromPointer(result.key, event);
          }}
          result={result}
        />
      )}
      rowSpacing="tight"
      role="listbox"
      testId={listID}
    />
  );
}

function taskSearchDialogHeight(resultCount: number, loadingVisible: boolean, errorVisible: boolean): number {
  if (resultCount > 0) {
    return Math.min(
      taskSearchDialogMaximumHeight,
      taskSearchInputHeight + taskSearchResultAreaPadding + resultCount * taskSearchResultEstimatedHeight,
    );
  }
  if (loadingVisible) {
    return taskSearchLoadingDialogHeight;
  }
  return errorVisible ? taskSearchErrorDialogHeight : taskSearchInputHeight;
}

function taskSearchNextOffset(search: ReturnType<typeof useBoardTaskSearch>): string | undefined {
  const nextOffset = search.request.data?.pages.at(-1)?.response.nextOffset;
  return nextOffset == null ? undefined : nextOffset.toString();
}

function SearchRefreshError({
  message,
  onRetry,
}: Readonly<{
  message: string;
  onRetry(): void;
}>) {
  const { t } = useTranslation();
  return (
    <div
      className="flex items-center justify-between gap-[var(--space-3)] px-[var(--space-3)] py-[var(--space-2)] text-sm text-[var(--color-error)]"
      role="alert"
    >
      <span className="min-w-0 truncate">{message}</span>
      <Button className="shrink-0 font-semibold" onClick={onRetry} variant="ghost">
        {t("app.retry")}
      </Button>
    </div>
  );
}

function flattenSearchResults(
  data: InfiniteData<SearchPage, number | null> | undefined,
): readonly SearchResult[] {
  if (data === undefined) {
    return [];
  }
  return data.pages.flatMap((page) =>
    page.response.groups.map((group, groupIndex) => ({
      key: taskSearchResultKey(page, group, groupIndex),
      group,
    })),
  );
}

function taskSearchResultKey(page: SearchPage, group: TaskSearchGroup, groupIndex: number): string {
  const firstOrdinal = group.hits[0]?.ordinal;
  if (firstOrdinal === undefined) {
    throw new Error(`Task Search group ${group.taskID} at offset ${String(page.offset)} has no hits.`);
  }
  return [
    group.projectID,
    page.query,
    String(page.offset),
    groupIndex.toString(),
    group.taskID,
    firstOrdinal.toString(),
  ].join(":");
}

function taskSearchOptionID(listID: string, resultKey: string): string {
  return `${listID}-option-${resultKey}`;
}

function searchSelectionDirection(key: string): -1 | 1 | null {
  if (key === "ArrowDown") {
    return 1;
  }
  if (key === "ArrowUp") {
    return -1;
  }
  return null;
}

function sameTaskSearchProject(
  left: Readonly<{ projectID: string | null }>,
  right: Readonly<{ projectID: string | null }>,
): boolean {
  return left.projectID === right.projectID;
}

function searchBoundaryState(
  search: ReturnType<typeof useBoardTaskSearch>,
  copy: Readonly<{
    errorMessage: string;
    loadingLabel: string;
    retryLabel: string;
  }>,
): VirtualizedInfiniteListBoundaryState | undefined {
  if (!search.paginationUsesVisibleData) {
    return undefined;
  }
  return taskSearchBoundaryState({
    error: search.request.isFetchNextPageError ? search.request.error : null,
    loading: search.request.isFetchingNextPage,
    loadingLabel: copy.loadingLabel,
    errorMessage: copy.errorMessage,
    retryLabel: copy.retryLabel,
    onRetry: () => {
      void search.request.fetchNextPage();
    },
  });
}

function taskSearchBoundaryState({
  error,
  loading,
  loadingLabel,
  errorMessage: boundaryErrorMessage,
  retryLabel,
  onRetry,
}: Readonly<{
  error: Error | null;
  loading: boolean;
  loadingLabel: string;
  errorMessage: string;
  retryLabel: string;
  onRetry(): void;
}>): VirtualizedInfiniteListBoundaryState | undefined {
  if (loading) {
    return { state: "loading", label: loadingLabel };
  }
  if (error !== null) {
    return {
      state: "error",
      message: `${boundaryErrorMessage} ${errorMessage(error)}`,
      retryLabel,
      onRetry,
    };
  }
  return undefined;
}
