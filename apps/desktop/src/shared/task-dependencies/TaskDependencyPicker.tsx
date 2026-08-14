import { Plus, SearchIcon } from "lucide-react";
import { useMemo, useRef, useState, type ReactElement } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import { taskSearchDebounceMs, useDebouncedText, useTaskSearch, type TaskSearchResult } from "@/app-facade";
import { TaskStatusIcon } from "@/shared/task-status";
import {
  IconTooltipButton,
  InfiniteListBoundary,
  Popover,
  PopoverContent,
  PopoverTrigger,
  Spinner,
  VirtualizedInfiniteList,
  fieldInputClassName,
  type VirtualizedInfiniteListBoundaryState,
} from "@/ui";

export function TaskDependencyPicker({
  disabled,
  excludedTaskIDs,
  onCreateTask,
  onSelect,
  projectID,
  trigger,
}: Readonly<{
  disabled: boolean;
  excludedTaskIDs: ReadonlySet<string>;
  onCreateTask(): void;
  onSelect(result: TaskSearchResult): Promise<unknown>;
  projectID: string;
  trigger: ReactElement;
}>) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [pendingTaskID, setPendingTaskID] = useState<string | null>(null);
  const [acceptedTaskIDs, setAcceptedTaskIDs] = useState<ReadonlySet<string>>(() => new Set());
  const inputRef = useRef<HTMLInputElement | null>(null);
  const debouncedQuery = useDebouncedText(query, taskSearchDebounceMs);
  const search = useTaskSearch(projectID, open && !disabled, debouncedQuery);
  const results = useMemo(
    () =>
      search.results.filter(
        (result) => !excludedTaskIDs.has(result.group.taskID) && !acceptedTaskIDs.has(result.group.taskID),
      ),
    [acceptedTaskIDs, excludedTaskIDs, search.results],
  );
  const close = (): void => {
    setOpen(false);
    setQuery("");
    setPendingTaskID(null);
    setAcceptedTaskIDs(new Set());
  };
  const select = async (result: TaskSearchResult): Promise<void> => {
    const selectedTaskID = result.group.taskID;
    setPendingTaskID(selectedTaskID);
    try {
      await onSelect(result);
      setAcceptedTaskIDs((current) => new Set([...current, selectedTaskID]));
      setPendingTaskID(null);
    } catch {
      setPendingTaskID(null);
    }
  };
  return (
    <Popover
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen);
        if (!nextOpen) {
          setQuery("");
          setPendingTaskID(null);
          setAcceptedTaskIDs(new Set());
        }
      }}
      open={open}
    >
      <PopoverTrigger asChild>{trigger}</PopoverTrigger>
      <PopoverContent
        align="end"
        className="w-[min(26rem,calc(100vw-24px))] gap-[var(--space-2)] p-[var(--space-2)]"
        collisionPadding={12}
        level={3}
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          inputRef.current?.focus();
        }}
      >
        <div className="flex items-center gap-[var(--space-1)]">
          <span className="relative block min-w-0 flex-1">
            <SearchIcon
              aria-hidden="true"
              className="pointer-events-none absolute top-1/2 left-[var(--space-2)] -translate-y-1/2 text-[var(--color-muted)]"
              size={16}
              strokeWidth={1.8}
            />
            <input
              aria-label={t("task.dependenciesSearch")}
              autoComplete="off"
              className={`${fieldInputClassName} text-sm`}
              inputMode="search"
              onChange={(event) => {
                setQuery(event.currentTarget.value);
              }}
              placeholder={t("task.dependenciesSearch")}
              ref={inputRef}
              style={{
                height: "var(--space-6)",
                paddingBlock: "var(--space-0)",
                paddingInlineStart: "calc(var(--space-2) + var(--space-4) + var(--space-1))",
              }}
              type="text"
              value={query}
            />
          </span>
          <IconTooltipButton
            disabled={disabled || pendingTaskID !== null}
            label={t("task.dependenciesCreateTask")}
            onClick={() => {
              close();
              onCreateTask();
            }}
            size="icon"
          >
            <Plus aria-hidden="true" size={16} strokeWidth={1.8} />
          </IconTooltipButton>
        </div>
        <TaskDependencySearchResults
          onActivate={(result) => void select(result)}
          pendingTaskID={pendingTaskID}
          results={results}
          search={search}
        />
      </PopoverContent>
    </Popover>
  );
}

function TaskDependencySearchResults({
  onActivate,
  pendingTaskID,
  results,
  search,
}: Readonly<{
  onActivate(result: TaskSearchResult): void;
  pendingTaskID: string | null;
  results: readonly TaskSearchResult[];
  search: ReturnType<typeof useTaskSearch>;
}>) {
  const { t } = useTranslation();
  const resultsVisible = taskDependencySearchResultsVisible(search, results.length);
  if (!resultsVisible) {
    return <TaskDependencySearchFallback results={results} search={search} />;
  }
  return (
    <VirtualizedInfiniteList
      ariaLabel={t("task.dependenciesSearchResults")}
      className="max-h-72 min-h-0 overflow-y-auto"
      estimateSize={() => 32}
      getItemKey={(result) => result.key}
      hasNextPage={search.paginationUsesVisibleData && search.request.hasNextPage}
      isFetchingNextPage={search.paginationUsesVisibleData && search.request.isFetchingNextPage}
      items={results}
      loadMoreKey={
        search.paginationUsesVisibleData
          ? search.request.data?.pages.at(-1)?.response.nextOffset?.toString()
          : undefined
      }
      loadingLabel={t("taskSearch.searching")}
      nextBoundary={taskDependencySearchBoundary(
        search,
        t("taskSearch.searching"),
        t("taskSearch.loadMoreFailed"),
        t("app.retry"),
      )}
      onLoadMore={() => {
        if (search.paginationUsesVisibleData) {
          void search.request.fetchNextPage();
        }
      }}
      renderItem={(result) => (
        <button
          aria-selected={false}
          className="flex w-full min-w-0 items-center gap-[var(--space-2)] rounded-[var(--radius-s)] border-0 bg-transparent py-[calc(var(--space-2)/1.5)] text-left text-[var(--color-on-island)] outline-none hover:bg-[var(--color-island-2)] focus-visible:ring-[3px] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_35%,transparent)] disabled:cursor-wait disabled:opacity-55"
          data-testid={`dependency-candidate-${result.group.taskID}`}
          disabled={pendingTaskID !== null}
          onClick={() => {
            onActivate(result);
          }}
          role="option"
          type="button"
        >
          <span className="grid size-[15px] shrink-0 place-items-center">
            {pendingTaskID === result.group.taskID ? (
              <Spinner className="size-[15px]" size="sm" />
            ) : (
              <TaskStatusIcon status={result.group.status.kind} />
            )}
          </span>
          <span className="shrink-0 font-mono text-xs text-[var(--color-muted)]">{result.group.shortID}</span>
          <span className="min-w-0 truncate text-sm">{result.group.title}</span>
        </button>
      )}
      role="listbox"
      rowSpacing="tight"
    />
  );
}

function taskDependencySearchResultsVisible(
  search: ReturnType<typeof useTaskSearch>,
  resultCount: number,
): boolean {
  if (!search.searchable || search.normalizedTooShort || resultCount === 0) {
    return false;
  }
  if (search.results.length > 0) {
    return true;
  }
  return !search.request.isError && !search.request.isFetching;
}

function TaskDependencySearchFallback({
  results,
  search,
}: Readonly<{
  results: readonly TaskSearchResult[];
  search: ReturnType<typeof useTaskSearch>;
}>) {
  const { t } = useTranslation();
  if (!search.searchable || search.normalizedTooShort) {
    return null;
  }
  if (search.request.isError && search.results.length === 0) {
    return (
      <div className="grid min-h-16 place-items-center text-sm text-[var(--color-error)]" role="alert">
        {errorMessage(search.request.error)}
      </div>
    );
  }
  if (search.request.isFetching && search.results.length === 0) {
    return (
      <InfiniteListBoundary
        direction="initial"
        state={{ state: "loading", label: t("taskSearch.searching") }}
      />
    );
  }
  if (results.length === 0) {
    return (
      <p className="m-0 px-[var(--space-2)] py-[var(--space-3)] text-sm text-[var(--color-muted)]">
        {t("task.dependenciesNoMatches")}
      </p>
    );
  }
  return null;
}

function taskDependencySearchBoundary(
  search: ReturnType<typeof useTaskSearch>,
  loadingLabel: string,
  message: string,
  retryLabel: string,
): VirtualizedInfiniteListBoundaryState | undefined {
  if (!search.paginationUsesVisibleData) {
    return undefined;
  }
  if (search.request.isFetchingNextPage) {
    return { state: "loading", label: loadingLabel };
  }
  if (search.request.isFetchNextPageError) {
    return {
      state: "error",
      message: `${message} ${errorMessage(search.request.error)}`,
      retryLabel,
      onRetry: () => {
        void search.request.fetchNextPage();
      },
    };
  }
  return undefined;
}
