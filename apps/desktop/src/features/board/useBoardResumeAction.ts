import { useCallback, useRef } from "react";

import type { WorkflowExecutionTargetSelection } from "@/api";
import {
  resumeTaskInitiatingAction,
  type TaskInitiatingAction,
  type TaskInitiatingActionController,
} from "@/shared/execution-target";
import { useBoardTaskLifecycleAction } from "./useBoardTaskLifecycleAction";

export function useBoardResumeAction(controller: TaskInitiatingActionController): Readonly<{
  actionsDisabled: boolean;
  pendingTaskIDs: ReadonlySet<string>;
  execute(taskID: string): void;
  continueExecution(
    action: Extract<TaskInitiatingAction, { kind: "resume" }>,
    selection: WorkflowExecutionTargetSelection,
  ): void;
}> {
  const { execute: trackAction, pendingTaskIDs } = useBoardTaskLifecycleAction();
  const runningRef = useRef(false);
  const runAction = useCallback(
    (
      taskID: string,
      action: Extract<TaskInitiatingAction, { kind: "resume" }>,
      selection?: WorkflowExecutionTargetSelection,
    ): void => {
      if (runningRef.current || controller.running) {
        return;
      }
      runningRef.current = true;
      void trackAction(taskID, async () => {
        try {
          await controller.run(action, selection);
        } finally {
          runningRef.current = false;
        }
      });
    },
    [controller, trackAction],
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
  return {
    actionsDisabled: controller.running || pendingTaskIDs.size > 0,
    continueExecution,
    execute,
    pendingTaskIDs,
  };
}
