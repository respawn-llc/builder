import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo } from "react";

import type { SessionCatalogSummary, SessionCategory } from "@/api";
import {
  mainSessionCatalogInfiniteQueryOptions,
  previousSessionCatalogOffset,
  queryKeys,
  subagentSessionCatalogInfiniteQueryOptions,
  useAppServices,
} from "@/app-facade";

export type ProjectSessionsData =
  | Readonly<{
      kind: "loading";
      retry(): void;
    }>
  | Readonly<{
      kind: "error";
      error: Error;
      retry(): void;
    }>
  | Readonly<{
      kind: "ready";
      rows: readonly SessionCatalogSummary[];
      error: Error | null;
      hasOlder: boolean;
      hasNewer: boolean;
      loadingOlder: boolean;
      loadingNewer: boolean;
      olderFailed: boolean;
      newerFailed: boolean;
      loadMoreKey: string | undefined;
      previousLoadKey: string | undefined;
      loadOlder(): void;
      loadNewer(): void;
      retry(): void;
    }>;

export function useProjectSessionsData(projectID: string, category: SessionCategory): ProjectSessionsData {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  const query = useInfiniteQuery(
    category === "main"
      ? mainSessionCatalogInfiniteQueryOptions(api, projectID)
      : subagentSessionCatalogInfiniteQueryOptions(api, projectID),
  );
  useEffect(() => {
    const capturedQueryKey = queryKeys.projectSessionCatalog(projectID, category);
    return () => {
      queryClient.removeQueries({ queryKey: capturedQueryKey, exact: true });
    };
  }, [category, projectID, queryClient]);

  const pages = query.data?.pages;
  const rows = useMemo(() => projectSessionRows(pages), [pages]);
  const retry = () => {
    void query.refetch();
  };
  const phase = projectSessionsPhase({
    hasPages: pages !== undefined,
    isError: query.isError,
    olderFailed: query.isFetchNextPageError,
    newerFailed: query.isFetchPreviousPageError,
  });
  if (phase === "loading") {
    return { kind: "loading", retry };
  }
  if (phase === "error") {
    if (query.error === null) {
      throw new Error("Failed Project Sessions data requires a query error.");
    }
    return { kind: "error", error: query.error, retry };
  }

  const retainedData = query.data;
  if (pages === undefined || retainedData === undefined) {
    throw new Error("Ready Project Sessions data requires retained pages.");
  }
  const { nextOffset, previousOffset } = sessionCatalogOffsets(
    retainedData.pageParams[0],
    pages.at(-1)?.nextOffset,
  );
  return {
    kind: "ready",
    rows,
    error: query.error,
    hasOlder: query.hasNextPage,
    hasNewer: query.hasPreviousPage,
    loadingOlder: query.isFetchingNextPage,
    loadingNewer: query.isFetchingPreviousPage,
    olderFailed: query.isFetchNextPageError,
    newerFailed: query.isFetchPreviousPageError,
    loadMoreKey: sessionCatalogLoadKey(projectID, category, "older", nextOffset),
    previousLoadKey: sessionCatalogLoadKey(projectID, category, "newer", previousOffset),
    loadOlder: () => {
      void query.fetchNextPage();
    },
    loadNewer: () => {
      void query.fetchPreviousPage();
    },
    retry,
  };
}

function sessionCatalogOffsets(
  firstOffset: number | null | undefined,
  nextOffset: number | null | undefined,
): Readonly<{ nextOffset: number | null; previousOffset: number | null }> {
  if (firstOffset === null) {
    throw new Error("Ready Project Sessions data cannot have a null first page offset.");
  }
  return {
    nextOffset: nextOffset ?? null,
    previousOffset: firstOffset === undefined ? null : (previousSessionCatalogOffset(firstOffset) ?? null),
  };
}

function projectSessionRows(
  pages: readonly Readonly<{ sessions: readonly SessionCatalogSummary[] }>[] | undefined,
): readonly SessionCatalogSummary[] {
  if (pages === undefined) return [];
  const seen = new Set<string>();
  const rows: SessionCatalogSummary[] = [];
  for (const page of pages) {
    for (const session of page.sessions) {
      if (seen.has(session.id)) continue;
      seen.add(session.id);
      rows.push(session);
    }
  }
  return rows;
}

function sessionCatalogLoadKey(
  projectID: string,
  category: SessionCategory,
  direction: "older" | "newer",
  offset: number | null,
): string | undefined {
  if (offset === null) return undefined;
  return JSON.stringify([projectID, category, direction, offset]);
}

function projectSessionsPhase(
  input: Readonly<{
    hasPages: boolean;
    isError: boolean;
    olderFailed: boolean;
    newerFailed: boolean;
  }>,
): "loading" | "error" | "ready" {
  if (!input.hasPages) return input.isError ? "error" : "loading";
  if (input.isError && !input.olderFailed && !input.newerFailed) return "error";
  return "ready";
}
