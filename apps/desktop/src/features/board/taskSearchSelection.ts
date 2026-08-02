import { useCallback, useRef, useState } from "react";

import { prefersReducedMotion } from "@/ui";
import type { TaskSearchResultItem as SearchResult } from "./TaskSearchResult";

type RememberedSelection = Readonly<{
  key: string;
  projectID: string;
  query: string;
}>;

export type TaskSearchScrollRequest = Readonly<{
  behavior: "auto" | "smooth";
  key: string;
  requestID: number;
}>;

let rememberedTaskSearchSelection: RememberedSelection | null = null;

export function useTaskSearchSelection(
  projectID: string,
  displayedQuery: string,
  results: readonly SearchResult[],
) {
  const [selected, setSelected] = useState<RememberedSelection | null>(() =>
    rememberedTaskSearchSelection?.projectID === projectID ? rememberedTaskSearchSelection : null,
  );
  const [scrollRequest, setScrollRequest] = useState<TaskSearchScrollRequest | null>(null);
  const nextScrollRequestIDRef = useRef(1);
  const activeSelection = resolveActiveSearchResult(results, selected, projectID, displayedQuery);
  const activeKey = activeSelection.key;
  const select = useCallback(
    (key: string): void => {
      const next = { key, projectID, query: displayedQuery };
      setSelected(next);
      rememberedTaskSearchSelection = next;
    },
    [displayedQuery, projectID],
  );
  const requestReveal = useCallback((key: string, behavior: "auto" | "smooth"): void => {
    const requestID = nextScrollRequestIDRef.current;
    nextScrollRequestIDRef.current += 1;
    setScrollRequest({ behavior, key, requestID });
  }, []);
  const selectAndReveal = useCallback(
    (key: string): void => {
      select(key);
      requestReveal(key, prefersReducedMotion() ? "auto" : "smooth");
    },
    [requestReveal, select],
  );
  const revealActive = useCallback((): void => {
    if (activeKey !== null) {
      requestReveal(activeKey, "auto");
    }
  }, [activeKey, requestReveal]);
  const revealImmediately = useCallback(
    (key: string): void => {
      requestReveal(key, "auto");
    },
    [requestReveal],
  );
  return {
    activeKey,
    activeResult: results.find((result) => result.key === activeKey) ?? null,
    navigationBlocked: activeSelection.navigationBlocked,
    revealActive,
    revealImmediately,
    scrollRequest,
    select,
    selectAndReveal,
  };
}

export function adjacentSearchResult(
  results: readonly SearchResult[],
  activeKey: string | null,
  direction: -1 | 1,
): SearchResult | null {
  if (results.length === 0) {
    return null;
  }
  if (activeKey === null) {
    return results[0] ?? null;
  }
  const currentIndex = results.findIndex((result) => result.key === activeKey);
  if (currentIndex < 0) {
    return null;
  }
  const nextIndex = currentIndex + direction;
  if (nextIndex < 0 || nextIndex >= results.length) {
    return null;
  }
  return results[nextIndex] ?? null;
}

function resolveActiveSearchResult(
  results: readonly SearchResult[],
  selected: RememberedSelection | null,
  projectID: string,
  displayedQuery: string,
): Readonly<{ key: string | null; navigationBlocked: boolean }> {
  if (selected?.projectID === projectID && selected.query === displayedQuery) {
    return results.some((result) => result.key === selected.key)
      ? { key: selected.key, navigationBlocked: false }
      : { key: null, navigationBlocked: true };
  }
  return { key: results[0]?.key ?? null, navigationBlocked: false };
}
