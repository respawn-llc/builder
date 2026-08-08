import { useCallback, useRef, useState } from "react";

export function useBoardTaskLifecycleAction() {
  const [pendingTaskIDs, setPendingTaskIDs] = useState<ReadonlySet<string>>(() => new Set());
  const pendingTaskIDsRef = useRef(pendingTaskIDs);
  const execute = useCallback(async (taskID: string, action: () => Promise<void>): Promise<void> => {
    if (pendingTaskIDsRef.current.has(taskID)) {
      return;
    }
    const pending = new Set(pendingTaskIDsRef.current);
    pending.add(taskID);
    pendingTaskIDsRef.current = pending;
    setPendingTaskIDs(pending);
    try {
      await action();
    } finally {
      const settled = new Set(pendingTaskIDsRef.current);
      settled.delete(taskID);
      pendingTaskIDsRef.current = settled;
      setPendingTaskIDs(settled);
    }
  }, []);
  return { execute, pendingTaskIDs };
}
