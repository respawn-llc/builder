import { useCallback, useState } from "react";

import type { WorkflowExecutionTargetSelection } from "@/api";
import {
  resumeTaskInitiatingAction,
  type TaskInitiatingAction,
  type TaskInitiatingActionController,
} from "@/shared/execution-target";

export function useBoardResumeAction(controller: TaskInitiatingActionController): Readonly<{
  pendingTaskIDs: ReadonlySet<string>;
  execute(taskID: string): void;
  continueExecution(
    action: Extract<TaskInitiatingAction, { kind: "resume" }>,
    selection: WorkflowExecutionTargetSelection,
  ): void;
}> {
  const [pendingTaskIDs, setPendingTaskIDs] = useState<ReadonlySet<string>>(() => new Set());
  const runAction = useCallback(
    (
      taskID: string,
      action: Extract<TaskInitiatingAction, { kind: "resume" }>,
      selection?: WorkflowExecutionTargetSelection,
    ): void => {
      setPendingTaskIDs((current) => new Set(current).add(taskID));
      void controller.run(action, selection).finally(() => {
        setPendingTaskIDs((current) => {
          const next = new Set(current);
          next.delete(taskID);
          return next;
        });
      });
    },
    [controller],
  );
  const execute = useCallback(
    (taskID: string): void => {
      runAction(taskID, resumeTaskInitiatingAction(taskID));
    },
    [runAction],
  );
  const continueExecution = useCallback(
    (
      action: Extract<TaskInitiatingAction, { kind: "resume" }>,
      selection: WorkflowExecutionTargetSelection,
    ): void => {
      runAction(action.taskID, action, selection);
    },
    [runAction],
  );
  return { continueExecution, execute, pendingTaskIDs };
}
