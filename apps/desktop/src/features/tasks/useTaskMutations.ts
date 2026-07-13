import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type { TaskEditInput, TaskMutationInput } from "../../api";
import { queryKeys } from "../../app/queryKeys";
import { useAppServices } from "../../app/useAppServices";

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
      await queryClient.invalidateQueries({ queryKey: queryKeys.board(projectID, boardQueryWorkflowID) });
      if (selectedWorkflowID !== boardQueryWorkflowID) {
        await queryClient.invalidateQueries({ queryKey: queryKeys.board(projectID, selectedWorkflowID) });
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
