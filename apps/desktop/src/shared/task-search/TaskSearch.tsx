import { useInfiniteQuery, type InfiniteData } from "@tanstack/react-query";
import { SearchIcon } from "lucide-react";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
  type RefObject,
} from "react";
import { useTranslation } from "react-i18next";

import { TaskSearchError, errorMessage } from "@/api";
import {
  queryKeys,
  taskSearchScopesEqual,
  useSidebar,
  useAppServices,
  useTaskSearchMemory,
  type TaskSearchScope,
} from "@/app-facade";
import {
  CommandPaletteDialog,
  ErrorState,
  InfiniteListBoundary,
  InteractiveChip,
  VirtualizedInfiniteList,
} from "@/ui";
import { useRetainedQueryData } from "@/app-facade";
import {
  adjacentSearchResult,
  useTaskSearchSelection,
  type TaskSearchScrollRequest,
} from "./taskSearchSelection";
import { TaskSearchResultRow, type TaskSearchResultItem as SearchResult } from "./TaskSearchResult";
import {
  flattenSearchResults,
  SearchRefreshError,
  searchBoundaryState,
  searchSelectionDirection,
  taskSearchDialogHeight,
  taskSearchOptionID,
  taskSearchResultEstimatedHeight,
  type SearchPage,
} from "./taskSearchPresentation";
import { useDebouncedText, useTaskSearchShortcuts } from "./taskSearchShortcuts";

const searchDebounceMs = 300;
const taskSearchPageSize = 40;
const retainedTaskSearchPages = 3;
const taskSearchContext = 20;

export type TaskSearchActivationPolicy =
  | Readonly<{ kind: "project"; onOpenTask(taskID: string): void; ownerKey: string }>
  | Readonly<{ kind: "sidebar" }>;

export type TaskSearchInvocation = Readonly<{
  scope: TaskSearchScope;
  activation: TaskSearchActivationPolicy;
}>;

type TaskSearchController = Readonly<{
  activeInvocation: TaskSearchInvocation | null;
  cancelSearch(): void;
  closeSearch(): void;
  openSearch(invocation: TaskSearchInvocation): void;
  searchOpen: boolean;
}>;

const TaskSearchControllerContext = createContext<TaskSearchController | null>(null);

export function TaskSearchProvider({ children }: Readonly<{ children: ReactNode }>) {
  const [state, setState] = useState<Pick<TaskSearchController, "activeInvocation" | "searchOpen">>({
    activeInvocation: null,
    searchOpen: false,
  });
  const cancelSearch = useCallback(() => {
    setState({ activeInvocation: null, searchOpen: false });
  }, []);
  const closeSearch = useCallback(() => {
    setState((current) => ({ ...current, searchOpen: false }));
  }, []);
  const openSearch = useCallback((activeInvocation: TaskSearchInvocation) => {
    setState({ activeInvocation, searchOpen: true });
  }, []);
  return (
    <TaskSearchControllerContext.Provider
      value={useMemo(
        () => ({ ...state, cancelSearch, closeSearch, openSearch }),
        [cancelSearch, closeSearch, openSearch, state],
      )}
    >
      {children}
    </TaskSearchControllerContext.Provider>
  );
}

function useTaskSearchController(): TaskSearchController {
  const controller = useContext(TaskSearchControllerContext);
  if (controller === null) {
    throw new Error("Task Search requires TaskSearchProvider.");
  }
  return controller;
}

export function TaskSearchProjectTrigger({
  ownerKey,
  projectID,
  onOpenTask,
}: Readonly<{
  ownerKey: string;
  projectID: string;
  onOpenTask(taskID: string): void;
}>) {
  const { t } = useTranslation();
  const { activeInvocation, cancelSearch, openSearch, searchOpen } = useTaskSearchController();
  const scope: TaskSearchScope = { kind: "project", projectID };
  const isOpen =
    searchOpen && activeInvocation !== null && taskSearchScopesEqual(activeInvocation.scope, scope);
  const activeInvocationRef = useRef(activeInvocation);
  useEffect(() => {
    activeInvocationRef.current = activeInvocation;
  }, [activeInvocation]);
  useEffect(
    () => () => {
      const current = activeInvocationRef.current;
      if (
        current?.activation.kind === "project" &&
        current.activation.ownerKey === ownerKey
      ) {
        cancelSearch();
      }
    },
    [cancelSearch, ownerKey],
  );

  return (
    <InteractiveChip
      aria-label={t("taskSearch.open")}
      onClick={() => {
        openSearch({
          activation: { kind: "project", onOpenTask, ownerKey },
          scope,
        });
      }}
      selected={isOpen}
      style={{ paddingInline: "var(--space-3)" }}
      tone={isOpen ? "primary" : "neutral"}
    >
      <SearchIcon aria-hidden="true" className="shrink-0" size={14} strokeWidth={1.8} />
      {t("taskSearch.open")}
    </InteractiveChip>
  );
}

export function TaskSearchGlobalTrigger() {
  const { t } = useTranslation();
  const { openSearch } = useTaskSearchController();
  return (
    <button
      aria-label={t("taskSearch.open")}
      className="grid h-6 w-6 place-items-center rounded-full border border-transparent bg-transparent text-[var(--color-on-island)]"
      data-testid="app-chrome-search"
      onClick={() => {
        openSearch({ activation: { kind: "sidebar" }, scope: { kind: "global" } });
      }}
      type="button"
    >
      <SearchIcon aria-hidden="true" size={16} strokeWidth={1.25} />
    </button>
  );
}

export function TaskSearchHost() {
  const { activeInvocation, closeSearch, openSearch, searchOpen } = useTaskSearchController();
  const { activeDestination, openSidebar, phase, replaceSidebar } = useSidebar();
  const memory = useTaskSearchMemory();
  const pendingTaskIDRef = useRef<string | null>(null);
  const invocation = activeInvocation ?? {
    activation: { kind: "sidebar" as const },
    scope: { kind: "global" as const },
  };
  const query = memory.query;
  const debouncedQuery = useDebouncedText(query, searchDebounceMs);
  const search = useTaskSearch(invocation.scope, searchOpen, debouncedQuery);
  const selection = useTaskSearchSelection(invocation.scope, search.displayedQuery, search.results);
  useEffect(() => {
    pendingTaskIDRef.current = null;
  }, [activeInvocation]);
  const { activeKey, revealActive } = selection;
  useEffect(() => {
    if (!searchOpen || activeInvocation === null || search.results.length === 0 || activeKey === null) {
      return;
    }
    revealActive();
  }, [activeInvocation, activeKey, revealActive, search.results.length, searchOpen]);
  const activate = useCallback(
    (result: SearchResult): void => {
      pendingTaskIDRef.current = result.group.taskID;
      closeSearch();
    },
    [closeSearch],
  );
  const completeClose = useCallback((): void => {
    const taskID = pendingTaskIDRef.current;
    pendingTaskIDRef.current = null;
    if (taskID === null || activeInvocation === null) {
      return;
    }
    if (activeInvocation.activation.kind === "project") {
      activeInvocation.activation.onOpenTask(taskID);
      return;
    }
    const destination = { kind: "taskDetail" as const, mode: "overlay" as const, taskID };
    if (activeDestination?.kind === "taskDetail" && phase === "open") {
      replaceSidebar(destination);
      return;
    }
    void openSidebar(destination);
  }, [activeDestination, activeInvocation, openSidebar, phase, replaceSidebar]);
  const openGlobalSearch = useCallback((): void => {
    pendingTaskIDRef.current = null;
    openSearch({ activation: { kind: "sidebar" }, scope: { kind: "global" } });
  }, [openSearch]);
  useTaskSearchShortcuts(openGlobalSearch);
  const moveSelection = useCallback(
    (direction: -1 | 1): void => {
      const next = adjacentSearchResult(search.results, selection.activeKey, direction);
      if (next !== null) {
        selection.selectAndReveal(next.key);
      }
    },
    [search.results, selection],
  );

  if (activeInvocation === null) {
    return null;
  }
  return (
    <TaskSearchDialog
      onActivate={activate}
      onClose={closeSearch}
      onExitComplete={completeClose}
      onMoveSelection={moveSelection}
      onQueryChange={memory.setQuery}
      open={searchOpen}
      query={query}
      search={search}
      selection={selection}
    />
  );
}

function useTaskSearch(scope: TaskSearchScope, open: boolean, debouncedQuery: string) {
  const { api } = useAppServices();
  const trimmedQuery = debouncedQuery.trim();
  const searchable = Array.from(trimmedQuery).length >= 3;
  const request = useInfiniteQuery<
    SearchPage,
    Error,
    InfiniteData<SearchPage, number | null>,
    readonly unknown[],
    number | null
  >({
    queryKey: queryKeys.taskSearch(scope, trimmedQuery),
    queryFn: async ({ pageParam, signal }) => ({
      offset: pageParam,
      scope,
      query: trimmedQuery,
      response: await api.searchTasks(
        {
          mode: "literal",
          query: trimmedQuery,
          context: taskSearchContext,
          caseSensitive: false,
          includeComments: true,
          ...(scope.kind === "project" ? { projectIDs: [scope.projectID] } : {}),
          pageSize: taskSearchPageSize,
          offset: pageParam ?? undefined,
        },
        signal,
      ),
    }),
    initialPageParam: null,
    enabled: open && searchable,
    getNextPageParam: (lastPage) => lastPage.response.nextOffset ?? undefined,
    maxPages: retainedTaskSearchPages,
    retry: (failureCount, error) => !(error instanceof TaskSearchError) && failureCount < 1,
  });
  const retainedData = useRetainedQueryData(scope, request.data, taskSearchScopesEqual);
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
  search: ReturnType<typeof useTaskSearch>;
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
  search: ReturnType<typeof useTaskSearch>;
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
  search: ReturnType<typeof useTaskSearch>;
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
      loadMoreKey={
        search.paginationUsesVisibleData
          ? search.request.data?.pages.at(-1)?.response.nextOffset?.toString()
          : undefined
      }
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
