import { useCallback, useLayoutEffect, useMemo, useState, useSyncExternalStore, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";

import {
  canonicalBoardFilter,
  defaultBoardNodeCardsSort,
  type BoardFilter,
  type BoardNodeCardsSort,
  type TaskLabelFilter,
} from "@/api";
import { useStableCallback } from "@/ui";
import { createBoardFilterGenerationController } from "./BoardFilterGenerationController";
import { createBoardGenerationRequestAdapter } from "./BoardGenerationRequestAdapter";
import { createBoardGenerationQueryRegistry } from "./BoardGenerationQueryRegistry";
import { BoardFilterGenerationContext } from "./BoardFilterGenerationRuntime";

export function BoardFilterGenerationProvider({
  children,
  initialFilter,
  onBackgroundError,
  desiredLabelFilter,
  queriesEnabled = true,
}: Readonly<{
  children: ReactNode;
  desiredLabelFilter?: TaskLabelFilter;
  initialFilter: BoardFilter;
  onBackgroundError?: ((error: unknown) => void) | undefined;
  queriesEnabled?: boolean;
}>) {
  const queryClient = useQueryClient();
  const reportBackgroundError = useStableCallback((error: unknown) => {
    onBackgroundError?.(error);
  });
  const [queryRegistry] = useState(() => createBoardGenerationQueryRegistry(queryClient));
  const [controller] = useState(() =>
    createBoardFilterGenerationController(initialFilter, {
      onBackgroundError: reportBackgroundError,
      onPromoted: (generation) => {
        queryRegistry.releaseGeneration(generation.generation - 1);
      },
      onRetiring: async (generation) => {
        await queryRegistry.cancelGeneration(generation.generation);
      },
    }),
  );
  const [sort, setSort] = useState<BoardNodeCardsSort>(defaultBoardNodeCardsSort);
  const requestAdapter = useMemo(
    () => createBoardGenerationRequestAdapter({ controller, queryClient, queryRegistry }),
    [controller, queryClient, queryRegistry],
  );
  const subscribe = useCallback((listener: () => void) => controller.subscribe(listener), [controller]);
  const getSnapshot = useCallback(() => controller.getSnapshot(), [controller]);
  const snapshot = useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
  useLayoutEffect(() => {
    if (desiredLabelFilter === undefined) {
      return;
    }
    const snapshot = controller.getSnapshot();
    const current = snapshot.desiredFilter ?? snapshot.active.filter;
    controller.setDesiredFilter(
      canonicalBoardFilter({
        labelFilter: desiredLabelFilter,
        dependencyFilter: current.dependencyFilter,
      }),
    );
  }, [controller, desiredLabelFilter]);
  const value = useMemo(
    () => ({
      controller,
      queriesEnabled,
      queryRegistry,
      requestAdapter,
      snapshot,
      setSort,
      sort,
    }),
    [controller, queriesEnabled, queryRegistry, requestAdapter, setSort, snapshot, sort],
  );
  return (
    <BoardFilterGenerationContext.Provider value={value}>{children}</BoardFilterGenerationContext.Provider>
  );
}
