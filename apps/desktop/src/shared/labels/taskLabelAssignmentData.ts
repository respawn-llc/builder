import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useSyncExternalStore } from "react";

import type { TaskLabelAssignment } from "@/api";
import { useAppServices } from "@/app-facade";
import {
  createTaskLabelAssignmentController,
  type TaskLabelAssignmentController,
  type TaskLabelAssignmentSnapshot,
  type TaskLabelUpdateInput,
} from "./taskLabelAssignmentController";
import { taskLabelAssignmentRegistryFor } from "./taskLabelAssignmentRegistry";

export type TaskLabelAssignmentData = Readonly<{
  controller: TaskLabelAssignmentController;
  snapshot: TaskLabelAssignmentSnapshot;
}>;

export function useManagedTaskLabelAssignment(
  {
    availableLabelIDs,
    enabled = true,
    initialAssignment,
    projectID,
    taskID,
    workflowID,
  }: Readonly<{
    availableLabelIDs: readonly string[];
    enabled?: boolean | undefined;
    initialAssignment: TaskLabelAssignment;
    projectID: string;
    taskID: string;
    workflowID: string;
  }>,
): TaskLabelAssignmentData {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  const update = useCallback(
    async (input: TaskLabelUpdateInput) =>
      api.updateTaskLabels(taskID, input.addLabelIDs, input.removeLabelIDs),
    [api, taskID],
  );
  const refetch = useCallback(async () => initialAssignment, [initialAssignment]);
  const registry = taskLabelAssignmentRegistryFor(queryClient);
  const subscribeController = useCallback(
    (listener: () => void) => registry.subscribe(taskID, listener),
    [registry, taskID],
  );
  const initialController = useMemo(
    () =>
      createTaskLabelAssignmentController({
        availableLabelIDs,
        initialLabelIDs: initialAssignment.labelIDs,
        refetch,
        taskID,
        update,
      }),
    [availableLabelIDs, initialAssignment, refetch, taskID, update],
  );
  const getController = useCallback(
    () => (enabled ? registry.get(taskID) : null) ?? initialController,
    [enabled, initialController, registry, taskID],
  );
  const controller = useSyncExternalStore(subscribeController, getController, getController);
  useEffect(() => {
    if (!enabled || taskID.length === 0) {
      return;
    }
    const lease = registry.acquire({
      availableLabelIDs,
      controller: initialController,
      initialAssignment,
      projectID,
      taskID,
      workflowID,
    });
    return () => {
      lease.release();
    };
  }, [
    availableLabelIDs,
    enabled,
    initialAssignment,
    initialController,
    projectID,
    queryClient,
    refetch,
    registry,
    taskID,
    update,
    workflowID,
  ]);

  useEffect(() => {
    if (enabled) {
      controller.replaceAvailableLabelIDs(availableLabelIDs);
    }
  }, [availableLabelIDs, controller, enabled]);

  useEffect(() => {
    if (enabled) {
      controller.replaceAuthoritative(initialAssignment);
    }
  }, [controller, enabled, initialAssignment]);

  const subscribe = useCallback((listener: () => void) => controller.subscribe(listener), [controller]);
  const getSnapshot = useCallback(() => controller.getSnapshot(), [controller]);
  const snapshot = useSyncExternalStore(subscribe, getSnapshot, getSnapshot);

  return {
    controller,
    snapshot,
  };
}
