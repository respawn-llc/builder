import { useCallback, useState } from "react";

import { resumeTaskInitiatingAction, type TaskInitiatingActionController } from "@/shared/execution-target";

export function useBoardResumeAction(controller: TaskInitiatingActionController): Readonly<{
  pendingTaskIDs: ReadonlySet<string>;
  execute(taskID: string): void;
}> {
  const [pendingTaskIDs, setPendingTaskIDs] = useState<ReadonlySet<string>>(() => new Set());
  const execute = useCallback(
    (taskID: string): void => {
      setPendingTaskIDs((current) => new Set(current).add(taskID));
      void controller.run(resumeTaskInitiatingAction(taskID)).finally(() => {
        setPendingTaskIDs((current) => {
          const next = new Set(current);
          next.delete(taskID);
          return next;
        });
      });
    },
    [controller],
  );
  return { execute, pendingTaskIDs };
}
