import { useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { useCallback, useEffect, useSyncExternalStore } from "react";

import type { TaskLabelAssignment } from "@/api";
import { queryKeys, useAppServices } from "@/app-facade";
import {
  type TaskLabelAssignmentController,
  type TaskLabelAssignmentSnapshot,
  type TaskLabelUpdateInput,
} from "./taskLabelAssignmentController";
import { taskLabelAssignmentRegistryFor } from "./taskLabelAssignmentRegistry";

export type TaskLabelAssignmentData = Readonly<{
  assignment: UseQueryResult<TaskLabelAssignment>;
  controller: TaskLabelAssignmentController | null;
  snapshot: TaskLabelAssignmentSnapshot | null;
}>;

export function useManagedTaskLabelAssignment(
  taskID: string,
  availableLabelIDs: readonly string[],
  enabled = true,
): TaskLabelAssignmentData {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  const update = useCallback(
    async (input: TaskLabelUpdateInput) =>
      api.updateTaskLabels(taskID, input.addLabelIDs, input.removeLabelIDs),
    [api, taskID],
  );
  const refetch = useCallback(async () => api.getTaskLabels(taskID), [api, taskID]);
  const registry = taskLabelAssignmentRegistryFor(queryClient);
  const subscribeController = useCallback(
    (listener: () => void) => registry.subscribe(taskID, listener),
    [registry, taskID],
  );
  const getController = useCallback(() => registry.get(taskID), [registry, taskID]);
  const controller = useSyncExternalStore(subscribeController, getController, getController);
  const assignment = useQuery({
    queryKey: queryKeys.taskLabels(taskID),
    queryFn: async () => {
      if (controller === null) {
        throw new Error(`Task label assignment controller is unavailable for Task ${taskID}.`);
      }
      const assignment = await controller.readAuthoritative();
      controller.replaceAuthoritative(assignment);
      return assignment;
    },
    enabled: enabled && taskID.length > 0 && controller !== null && !controller.getSnapshot().closed,
    retry: false,
  });

  useEffect(() => {
    if (!enabled || taskID.length === 0) {
      return;
    }
    const cachedAssignment = queryClient.getQueryData<TaskLabelAssignment>(queryKeys.taskLabels(taskID));
    const lease = registry.acquire({
      availableLabelIDs,
      initialAssignment: cachedAssignment ?? null,
      refetch,
      taskID,
      update,
    });
    return () => {
      lease.release();
    };
  }, [availableLabelIDs, enabled, queryClient, refetch, registry, taskID, update]);

  useEffect(() => {
    if (enabled) {
      controller?.replaceAvailableLabelIDs(availableLabelIDs);
    }
  }, [availableLabelIDs, controller, enabled]);

  const subscribe = useCallback(
    (listener: () => void) => controller?.subscribe(listener) ?? noOpUnsubscribe,
    [controller],
  );
  const getSnapshot = useCallback(() => controller?.getSnapshot() ?? null, [controller]);
  const snapshot = useSyncExternalStore(subscribe, getSnapshot, getSnapshot);

  return {
    assignment,
    controller,
    snapshot,
  };
}

function noOpUnsubscribe(): void {
  return;
}
