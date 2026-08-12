import { useInfiniteQuery, type InfiniteData } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";

import { TaskSearchError, type TaskSearchGroup, type TaskSearchResponse } from "@/api";
import { queryKeys } from "./queryKeys";
import { useAppServices } from "./useAppServices";
import { useRetainedQueryData } from "./useRetainedQueryData";

export const taskSearchDebounceMs = 300;
const taskSearchPageSize = 40;
const retainedTaskSearchPages = 3;
const taskSearchContext = 20;

type SearchPage = Readonly<{
  offset: number | null;
  projectID: string | null;
  query: string;
  response: TaskSearchResponse;
}>;

export type TaskSearchResult = Readonly<{
  key: string;
  group: TaskSearchGroup;
}>;

export function useDebouncedText(value: string, delayMs: number): string {
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

export function useTaskSearch(projectID: string | null, open: boolean, debouncedQuery: string) {
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
    queryFn: async ({ pageParam, signal }) => ({
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
          projectIDs: projectID === null ? undefined : [projectID],
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

function flattenSearchResults(
  data: InfiniteData<SearchPage, number | null> | undefined,
): readonly TaskSearchResult[] {
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
  return JSON.stringify([page.projectID, page.query, page.offset, groupIndex, group.taskID, firstOrdinal]);
}

function sameTaskSearchProject(
  left: Readonly<{ projectID: string | null }>,
  right: Readonly<{ projectID: string | null }>,
): boolean {
  return left.projectID === right.projectID;
}
