import { useMutation, useQueryClient } from "@tanstack/react-query";

import type { TaskEditInput, TaskMutationInput } from "@/api";
import { invalidateProjectTaskSearches, queryKeys, useAppServices } from "@/app-facade";

export function useCreateTask(
  projectID: string,
  boardQueryWorkflowID: string | undefined,
  selectedWorkflowID: string,
) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: TaskMutationInput) => api.createTask(input),
    onSuccess: async () => {
      const workflowIDs = new Set<string | undefined>([boardQueryWorkflowID, selectedWorkflowID]);
      const invalidations: Promise<void>[] = [];
      for (const workflowID of workflowIDs) {
        invalidations.push(
          queryClient.invalidateQueries({
            queryKey: queryKeys.boardWorkflowRoot(projectID, workflowID),
          }),
        );
        if (workflowID !== undefined) {
          invalidations.push(
            queryClient.invalidateQueries({
              queryKey: queryKeys.boardNodeCardsWorkflowRoot(projectID, workflowID),
            }),
          );
        }
      }
      invalidations.push(invalidateProjectTaskSearches(queryClient, projectID));
      await Promise.all(invalidations);
    },
  });
}

export function useUpdateTask(taskID: string, projectID: string) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: TaskEditInput) => api.updateTask(input),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.task(taskID) });
      await queryClient.invalidateQueries({ queryKey: queryKeys.allBoards });
      await queryClient.invalidateQueries({ queryKey: queryKeys.allAttention });
      await invalidateProjectTaskSearches(queryClient, projectID);
    },
  });
}
