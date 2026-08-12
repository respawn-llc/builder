import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";

import { invalidateAllTaskSearches, queryKeys } from "@/app-facade";
import { useConnectionSnapshot } from "@/app-facade";

export function useReconnectRefresh() {
  const connection = useConnectionSnapshot();
  const queryClient = useQueryClient();
  const sawDisconnectRef = useRef(false);

  useEffect(() => {
    if (connection.phase === "disconnected") {
      sawDisconnectRef.current = true;
      return;
    }
    if (connection.phase !== "connected" || !sawDisconnectRef.current) {
      return;
    }
    sawDisconnectRef.current = false;
    void refreshVisibleQueries(queryClient);
  }, [connection.generation, connection.phase, queryClient]);
}

async function refreshVisibleQueries(queryClient: ReturnType<typeof useQueryClient>): Promise<void> {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: queryKeys.readiness }),
    queryClient.invalidateQueries({ queryKey: queryKeys.projects }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allAttention }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allBoards }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allProjectEdits }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allProjectCatalogs }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allTasks }),
    invalidateAllTaskSearches(queryClient),
    queryClient.invalidateQueries({ queryKey: queryKeys.allActivity }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allPendingAsks }),
  ]);
}
