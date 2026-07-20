import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { noTaskLabelFilter, type TaskEditInput, type TaskMutationInput } from "@/api";
import { queryKeys, useAppServices } from "@/app-facade";

export function useWorkspaces(projectID: string) {
  const { api } = useAppServices();
  return useQuery({
    queryKey: queryKeys.workspaces(projectID),
    queryFn: async () => api.listWorkspaces(projectID),
    enabled: projectID.length > 0,
  });
}

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
      await queryClient.invalidateQueries({
        queryKey: queryKeys.board(projectID, boardQueryWorkflowID, noTaskLabelFilter),
      });
      if (selectedWorkflowID !== boardQueryWorkflowID) {
        await queryClient.invalidateQueries({
          queryKey: queryKeys.board(projectID, selectedWorkflowID, noTaskLabelFilter),
        });
      }
    },
  });
}

export function useUpdateTask(taskID: string) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: TaskEditInput) => api.updateTask(input),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.task(taskID) });
      await queryClient.invalidateQueries({ queryKey: queryKeys.allBoards });
      await queryClient.invalidateQueries({ queryKey: queryKeys.allAttention });
    },
  });
}
