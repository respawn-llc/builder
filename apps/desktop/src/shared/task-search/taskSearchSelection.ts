import { useCallback, useRef, useState } from "react";

import {
  taskSearchScopesEqual,
  useTaskSearchMemory,
  type TaskSearchMemorySelection,
  type TaskSearchScope,
} from "@/app-facade";
import { prefersReducedMotion } from "@/ui";
import type { TaskSearchResultItem as SearchResult } from "./TaskSearchResult";

export type TaskSearchScrollRequest = Readonly<{
  behavior: "auto" | "smooth";
  key: string;
  requestID: number;
}>;

export function useTaskSearchSelection(
  scope: TaskSearchScope,
  displayedQuery: string | null,
  results: readonly SearchResult[],
) {
  const memory = useTaskSearchMemory();
  const selected = memory.selectionFor(scope);
  const [scrollRequest, setScrollRequest] = useState<TaskSearchScrollRequest | null>(null);
  const nextScrollRequestIDRef = useRef(1);
  const activeKey = resolveActiveSearchResult(results, selected, scope, displayedQuery);
  const select = useCallback(
    (key: string): void => {
      if (displayedQuery === null) {
        throw new Error("Task Search result selection requires a displayed query.");
      }
      const next = { key, scope, query: displayedQuery };
      memory.rememberSelection(next);
    },
    [displayedQuery, memory, scope],
  );
  const requestReveal = useCallback(
    (key: string, behavior: "auto" | "smooth"): void => {
      const requestID = nextScrollRequestIDRef.current;
      nextScrollRequestIDRef.current += 1;
      setScrollRequest({ behavior, key, requestID });
    },
    [setScrollRequest],
  );
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
  selected: TaskSearchMemorySelection | null,
  scope: TaskSearchScope,
  displayedQuery: string | null,
): string | null {
  if (
    displayedQuery !== null &&
    selected !== null &&
    taskSearchScopesEqual(selected.scope, scope) &&
    selected.query === displayedQuery
  ) {
    return results.some((result) => result.key === selected.key) ? selected.key : (results[0]?.key ?? null);
  }
  return results[0]?.key ?? null;
}
