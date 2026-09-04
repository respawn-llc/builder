import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";

import type { DesktopProcess } from "@/api";
import { queryKeys, useAppServices, useConnectionSnapshot, useWindowFocus } from "@/app-facade";

const processRefreshIntervalMs = 1_500;

type PendingTermination =
  Readonly<{ phase: "requesting" }> | Readonly<{ phase: "awaiting_read"; afterSequence: number }>;

export type ProcessesData = Readonly<{
  processes: readonly DesktopProcess[] | undefined;
  observationTime: number | null;
  error: unknown;
  isError: boolean;
  isLoading: boolean;
  isConnected: boolean;
  pendingTerminationIDs: ReadonlySet<string>;
  retry(): void;
  terminate(processID: string): Promise<void>;
}>;

export function useProcessesData(projectID: string): ProcessesData {
  const { api } = useAppServices();
  const connection = useConnectionSnapshot();
  const windowFocused = useWindowFocus();
  const issuedReadSequenceRef = useRef(0);
  const pendingRef = useRef(new Map<string, PendingTermination>());
  const mountedRef = useRef(true);
  const previousWindowFocusedRef = useRef<boolean | null>(null);
  const [pendingTerminationIDs, setPendingTerminationIDs] = useState<ReadonlySet<string>>(() => new Set());

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const publishPending = useCallback(() => {
    if (mountedRef.current) {
      setPendingTerminationIDs(new Set(pendingRef.current.keys()));
    }
  }, []);

  const query = useQuery({
    queryKey: queryKeys.processes(projectID),
    queryFn: async () => {
      const sequence = ++issuedReadSequenceRef.current;
      const processes = await api.listProcesses(projectID);
      let changed = false;
      for (const [processID, pending] of pendingRef.current) {
        if (pending.phase === "awaiting_read" && sequence > pending.afterSequence) {
          pendingRef.current.delete(processID);
          changed = true;
        }
      }
      if (changed) {
        publishPending();
      }
      return processes;
    },
    refetchInterval: windowFocused === true ? processRefreshIntervalMs : false,
    refetchOnMount: "always",
    refetchOnWindowFocus: false,
  });
  const refetchProcesses = query.refetch;

  useEffect(() => {
    const previousWindowFocused = previousWindowFocusedRef.current;
    previousWindowFocusedRef.current = windowFocused;
    if (previousWindowFocused === false && windowFocused === true) {
      void refetchProcesses();
    }
  }, [refetchProcesses, windowFocused]);

  const terminate = useCallback(
    async (processID: string) => {
      if (connection.phase !== "connected" || pendingRef.current.has(processID)) {
        return;
      }
      pendingRef.current.set(processID, { phase: "requesting" });
      publishPending();
      try {
        await api.killProcess(processID);
      } finally {
        if (pendingRef.current.has(processID)) {
          pendingRef.current.set(processID, {
            phase: "awaiting_read",
            afterSequence: issuedReadSequenceRef.current,
          });
          void refetchProcesses();
        }
      }
    },
    [api, connection.phase, publishPending, refetchProcesses],
  );

  return {
    processes: query.data,
    observationTime: query.data === undefined ? null : query.dataUpdatedAt,
    error: query.error,
    isError: query.isError,
    isLoading: query.data === undefined && query.isPending,
    isConnected: connection.phase === "connected",
    pendingTerminationIDs,
    retry: () => {
      void refetchProcesses();
    },
    terminate,
  };
}
