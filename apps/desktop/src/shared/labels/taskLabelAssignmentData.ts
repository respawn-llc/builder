import {
  useQuery,
  useQueryClient,
  type UseQueryOptions,
  type UseQueryResult,
} from "@tanstack/react-query";
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
    initialAssignment: TaskLabelAssignment | null;
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
  const refetch = useCallback(async () => api.getTaskLabels(taskID), [api, taskID]);
  const registry = taskLabelAssignmentRegistryFor(queryClient);
  const subscribeController = useCallback(
    (listener: () => void) => registry.subscribe(taskID, listener),
    [registry, taskID],
  );
  const getController = useCallback(() => registry.get(taskID), [registry, taskID]);
  const controller = useSyncExternalStore(subscribeController, getController, getController);
  const assignmentOptions = {
    queryKey: queryKeys.taskLabels(taskID),
    queryFn: async () => {
      if (controller === null) {
        throw new Error(`Task label assignment controller is unavailable for Task ${taskID}.`);
      }
      return controller.readAuthoritative();
    },
    enabled: enabled && taskID.length > 0 && controller !== null && !controller.getSnapshot().closed,
    retry: false,
    staleTime: initialAssignment === null ? 0 : Infinity,
    ...(initialAssignment === null ? {} : { initialData: initialAssignment }),
  } satisfies UseQueryOptions<TaskLabelAssignment>;
  const assignment = useQuery<TaskLabelAssignment>(assignmentOptions);

  useEffect(() => {
    if (!enabled || taskID.length === 0) {
      return;
    }
    const cachedAssignment = queryClient.getQueryData<TaskLabelAssignment>(queryKeys.taskLabels(taskID));
    const lease = registry.acquire({
      availableLabelIDs,
      initialAssignment: cachedAssignment ?? initialAssignment,
      projectID,
      refetch,
      taskID,
      update,
      workflowID,
    });
    return () => {
      lease.release();
    };
  }, [
    availableLabelIDs,
    enabled,
    initialAssignment,
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
