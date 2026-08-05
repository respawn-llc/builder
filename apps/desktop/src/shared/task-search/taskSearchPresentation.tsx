import type { InfiniteData } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { errorMessage, type TaskSearchGroup, type TaskSearchResponse } from "@/api";
import type { TaskSearchScope } from "@/app-facade";
import { Button, type VirtualizedInfiniteListBoundaryState } from "@/ui";
import type { TaskSearchResultItem as SearchResult } from "./TaskSearchResult";

export type SearchPage = Readonly<{
  offset: number | null;
  scope: TaskSearchScope;
  query: string;
  response: TaskSearchResponse;
}>;

export const taskSearchResultEstimatedHeight = 154;
const taskSearchInputHeight = 56;
const taskSearchResultAreaPadding = 16;
const taskSearchDialogMaximumHeight = 560;
const taskSearchLoadingDialogHeight = 176;
const taskSearchErrorDialogHeight = 240;

export function taskSearchDialogHeight(
  resultCount: number,
  loadingVisible: boolean,
  errorVisible: boolean,
): number {
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

export function SearchRefreshError({
  message,
  onRetry,
}: Readonly<{ message: string; onRetry(): void }>) {
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

export function flattenSearchResults(
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
    page.scope.kind,
    page.scope.kind === "project" ? page.scope.projectID : null,
    page.query,
    String(page.offset),
    groupIndex.toString(),
    group.taskID,
    firstOrdinal.toString(),
  ].join(":");
}

export function taskSearchOptionID(listID: string, resultKey: string): string {
  return `${listID}-option-${resultKey}`;
}

export function searchSelectionDirection(key: string): -1 | 1 | null {
  if (key === "ArrowDown") {
    return 1;
  }
  if (key === "ArrowUp") {
    return -1;
  }
  return null;
}

export function searchBoundaryState(
  search: Readonly<{
    paginationUsesVisibleData: boolean;
    request: Readonly<{
      error: Error | null;
      fetchNextPage(): Promise<unknown>;
      isFetchNextPageError: boolean;
      isFetchingNextPage: boolean;
    }>;
  }>,
  copy: Readonly<{
    errorMessage: string;
    loadingLabel: string;
    retryLabel: string;
  }>,
): VirtualizedInfiniteListBoundaryState | undefined {
  if (!search.paginationUsesVisibleData) {
    return undefined;
  }
  if (search.request.isFetchingNextPage) {
    return { state: "loading", label: copy.loadingLabel };
  }
  if (search.request.isFetchNextPageError && search.request.error !== null) {
    return {
      state: "error",
      message: `${copy.errorMessage} ${errorMessage(search.request.error)}`,
      retryLabel: copy.retryLabel,
      onRetry: () => {
        void search.request.fetchNextPage();
      },
    };
  }
  return undefined;
}
