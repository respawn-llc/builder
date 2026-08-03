import { useCallback, useRef, useState } from "react";

export type BoardTaskLifecycleAction = Readonly<{
  execute(taskID: string, operation: () => Promise<void>): Promise<void>;
  pendingTaskIDs: ReadonlySet<string>;
}>;

export function useBoardTaskLifecycleAction(): BoardTaskLifecycleAction {
  const [pendingTaskIDs, setPendingTaskIDs] = useState<ReadonlySet<string>>(() => new Set());
  const pendingTaskIDsRef = useRef(pendingTaskIDs);
  const execute = useCallback(async (taskID: string, operation: () => Promise<void>): Promise<void> => {
    const currentPendingTaskIDs = pendingTaskIDsRef.current;
    if (currentPendingTaskIDs.has(taskID)) {
      return;
    }
    const nextPendingTaskIDs = new Set(currentPendingTaskIDs);
    nextPendingTaskIDs.add(taskID);
    pendingTaskIDsRef.current = nextPendingTaskIDs;
    setPendingTaskIDs(nextPendingTaskIDs);
    try {
      await operation();
    } finally {
      const settledPendingTaskIDs = new Set(pendingTaskIDsRef.current);
      settledPendingTaskIDs.delete(taskID);
      pendingTaskIDsRef.current = settledPendingTaskIDs;
      setPendingTaskIDs(settledPendingTaskIDs);
    }
  }, []);
  return { execute, pendingTaskIDs };
}
