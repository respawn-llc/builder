import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo } from "react";

import type { SessionCatalogSummary, SessionCategory } from "@/api";
import {
  mainSessionCatalogInfiniteQueryOptions,
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

export function useProjectSessionsData(
  projectID: string,
  category: SessionCategory,
): ProjectSessionsData {
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

  if (pages === undefined) {
    throw new Error("Ready Project Sessions data requires retained pages.");
  }
  const newerCursor = pages[0]?.newer ?? null;
  const olderCursor = pages.at(-1)?.older ?? null;
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
    loadMoreKey: sessionCatalogLoadKey(projectID, category, "older", olderCursor),
    previousLoadKey: sessionCatalogLoadKey(projectID, category, "newer", newerCursor),
    loadOlder: () => {
      void query.fetchNextPage();
    },
    loadNewer: () => {
      void query.fetchPreviousPage();
    },
    retry,
  };
}

function projectSessionRows(
  pages:
    | readonly Readonly<{ sessions: readonly SessionCatalogSummary[] }>[]
    | undefined,
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
  cursor: string | null,
): string | undefined {
  if (cursor === null) return undefined;
  return JSON.stringify([projectID, category, direction, cursor]);
}

function projectSessionsPhase(input: Readonly<{
  hasPages: boolean;
  isError: boolean;
  olderFailed: boolean;
  newerFailed: boolean;
}>): "loading" | "error" | "ready" {
  if (!input.hasPages) return input.isError ? "error" : "loading";
  if (input.isError && !input.olderFailed && !input.newerFailed) return "error";
  return "ready";
}
