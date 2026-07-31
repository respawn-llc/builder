import { useCallback, useLayoutEffect, useMemo, useState, useSyncExternalStore, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { defaultBoardNodeCardsSort, type BoardNodeCardsSort, type TaskLabelFilter } from "@/api";
import { useStableCallback } from "@/ui";
import { createBoardFilterGenerationController } from "./BoardFilterGenerationController";
import { createBoardGenerationRequestAdapter } from "./BoardGenerationRequestAdapter";
import { createBoardGenerationQueryRegistry } from "./BoardGenerationQueryRegistry";
import { BoardFilterGenerationContext } from "./BoardFilterGenerationRuntime";

export function BoardFilterGenerationProvider({
  children,
  initialFilter,
  initialSort = defaultBoardNodeCardsSort,
  onBackgroundError,
  desiredFilter = initialFilter,
  desiredSort = initialSort,
  queriesEnabled = true,
}: Readonly<{
  children: ReactNode;
  desiredFilter?: TaskLabelFilter;
  desiredSort?: BoardNodeCardsSort;
  initialFilter: TaskLabelFilter;
  initialSort?: BoardNodeCardsSort;
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
      initialSort,
      onBackgroundError: reportBackgroundError,
      onPromoted: (generation) => {
        queryRegistry.releaseGeneration(generation.generation - 1);
      },
      onRetiring: async (generation) => {
        await queryRegistry.cancelGeneration(generation.generation);
      },
    }),
  );
  const requestAdapter = useMemo(
    () => createBoardGenerationRequestAdapter({ controller, queryClient, queryRegistry }),
    [controller, queryClient, queryRegistry],
  );
  const subscribe = useCallback((listener: () => void) => controller.subscribe(listener), [controller]);
  const getSnapshot = useCallback(() => controller.getSnapshot(), [controller]);
  const snapshot = useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
  useLayoutEffect(() => {
    controller.setDesiredFilter(desiredFilter);
  }, [controller, desiredFilter]);
  useLayoutEffect(() => {
    controller.setDesiredSort(desiredSort);
  }, [controller, desiredSort]);
  const value = useMemo(
    () => ({
      controller,
      queriesEnabled,
      queryRegistry,
      requestAdapter,
      snapshot,
    }),
    [controller, queriesEnabled, queryRegistry, requestAdapter, snapshot],
  );
  return (
    <BoardFilterGenerationContext.Provider value={value}>{children}</BoardFilterGenerationContext.Provider>
  );
}
