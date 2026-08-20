import { useCallback } from "react";

import type { WorkflowExecutionTargetSelection } from "@/api";
import { resumeTaskInitiatingAction, type TaskInitiatingAction } from "./executionTargetContinuation";
import type { TaskInitiatingActionController } from "./useExecutionTargetContinuation";
import { useTaskLifecycleAction } from "./useTaskLifecycleAction";

export function useTaskResumeAction(controller: TaskInitiatingActionController) {
  const pending = useTaskLifecycleAction();
  const run = useCallback(
    async (
      taskID: string,
      action: Extract<TaskInitiatingAction, { kind: "resume" }>,
      selection?: WorkflowExecutionTargetSelection,
    ) => pending.execute(taskID, async () => controller.run(action, selection)),
    [controller, pending],
  );
  return {
    pendingTaskIDs: pending.pendingTaskIDs,
    execute: async (taskID: string) => run(taskID, resumeTaskInitiatingAction(taskID)),
    continueExecution: async (
      action: Extract<TaskInitiatingAction, { kind: "resume" }>,
      selection: WorkflowExecutionTargetSelection,
    ) => run(action.taskID, action, selection),
  };
}
