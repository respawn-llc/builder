import { useCallback } from "react";

import type { WorkflowExecutionTargetSelection } from "@/api";
import {
  resumeTaskInitiatingAction,
  type TaskInitiatingAction,
  type TaskInitiatingActionController,
} from "@/shared/execution-target";
import { useBoardTaskLifecycleAction } from "./useBoardTaskLifecycleAction";

export function useBoardResumeAction(controller: TaskInitiatingActionController) {
  const pending = useBoardTaskLifecycleAction();
  const run = useCallback(
    async (
      taskID: string,
      action: Extract<TaskInitiatingAction, { kind: "resume" }>,
      selection?: WorkflowExecutionTargetSelection,
    ) => pending.execute(taskID, async () => controller.run(action, selection)),
    [controller, pending],
  );
  return {
    actionsDisabled: controller.running || pending.pendingTaskIDs.size > 0,
    pendingTaskIDs: pending.pendingTaskIDs,
    execute: async (taskID: string) => run(taskID, resumeTaskInitiatingAction(taskID)),
    continueExecution: async (
      action: Extract<TaskInitiatingAction, { kind: "resume" }>,
      selection: WorkflowExecutionTargetSelection,
    ) => run(action.taskID, action, selection),
  };
}
