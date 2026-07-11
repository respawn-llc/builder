import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect } from "react";

import { queryKeys } from "../../app/queryKeys";
import { useAppServices } from "../../app/useAppServices";
import { useConnectionSnapshot } from "../../app/useConnectionSnapshot";
import { workflowProjectEvent, workflowProjectQuestionTaskID } from "../../app/workflowProjectEvents";
import type { TaskMoveInput, TaskStartInput } from "../../api";

export function useBoard(projectID: string, workflowID: string) {
  const { api } = useAppServices();
  return useQuery({
    queryKey: queryKeys.board(projectID, workflowID),
    queryFn: async () => api.getBoard(projectID, workflowID),
    enabled: projectID.trim().length > 0,
  });
}

export function useBoardNodeCards(projectID: string, workflowID: string, nodeID: string, enabled: boolean) {
  const { api } = useAppServices();
  return useInfiniteQuery({
    queryKey: queryKeys.boardNodeCards(projectID, workflowID, nodeID),
    queryFn: async ({ pageParam }) => api.listBoardNodeCards(projectID, workflowID, nodeID, pageParam),
    initialPageParam: "",
    enabled: enabled && projectID.length > 0 && workflowID.length > 0 && nodeID.length > 0,
    getNextPageParam: (lastPage) => (lastPage.nextPageToken.length > 0 ? lastPage.nextPageToken : undefined),
  });
}

export function useProjectBoardSubscription(
  projectID: string,
  boardQueryWorkflowID: string,
  input: Readonly<{
    selectedWorkflowID: string;
    selectedTaskID?: string;
    onBackgroundError?: (error: unknown) => void;
    onSelectedTaskDeleted?: () => void;
  }>,
) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  const connection = useConnectionSnapshot();
  const { onBackgroundError, onSelectedTaskDeleted, selectedTaskID = "", selectedWorkflowID } = input;
  const consumeBackgroundError = useCallback(
    (error: unknown): void => {
      onBackgroundError?.(error);
    },
    [onBackgroundError],
  );

  useEffect(() => {
    if (projectID.length === 0 || connection.phase !== "connected") {
      return;
    }
    async function refresh(): Promise<void> {
      await queryClient.invalidateQueries({ queryKey: queryKeys.board(projectID, boardQueryWorkflowID) });
      if (selectedWorkflowID !== boardQueryWorkflowID) {
        await queryClient.invalidateQueries({ queryKey: queryKeys.board(projectID, selectedWorkflowID) });
      }
      await queryClient.invalidateQueries({
        queryKey: queryKeys.boardNodeCardsRoot(projectID, selectedWorkflowID),
      });
      await queryClient.invalidateQueries({ queryKey: queryKeys.attention("") });
      await queryClient.invalidateQueries({ queryKey: queryKeys.attention(projectID) });
    }
    async function refreshQuestionTask(params: unknown): Promise<void> {
      const taskID = workflowProjectQuestionTaskID(params);
      if (taskID === null) {
        return;
      }
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.task(taskID), refetchType: "active" }),
        queryClient.invalidateQueries({ queryKey: queryKeys.activity(taskID), refetchType: "active" }),
        queryClient.invalidateQueries({ queryKey: queryKeys.allPendingAsks, refetchType: "active" }),
      ]);
    }
    const subscription = api.subscribeProject(projectID, {
      onOpen() {
        void refresh().catch(consumeBackgroundError);
      },
      onEvent(_method, params) {
        if (isDeletedTaskEvent(params, selectedTaskID)) {
          onSelectedTaskDeleted?.();
        }
        void refreshQuestionTask(params).catch(consumeBackgroundError);
        if (shouldRefreshBoardFromProjectEvent(params, boardQueryWorkflowID, selectedWorkflowID)) {
          void refresh().catch(consumeBackgroundError);
        }
      },
      onComplete() {
        void refresh().catch(consumeBackgroundError);
      },
      onError() {
        void refresh().catch(consumeBackgroundError);
      },
    });
    return () => {
      subscription.close();
    };
  }, [
    api,
    boardQueryWorkflowID,
    connection.generation,
    connection.phase,
    consumeBackgroundError,
    onBackgroundError,
    onSelectedTaskDeleted,
    projectID,
    queryClient,
    selectedTaskID,
    selectedWorkflowID,
  ]);
}

function isDeletedTaskEvent(params: unknown, taskID: string): boolean {
  const trimmedTaskID = taskID.trim();
  const event = workflowProjectEvent(params);
  if (trimmedTaskID.length === 0 || event === null) {
    return false;
  }
  return event.resource === "task" && event.action === "deleted" && event.changedIDs.includes(trimmedTaskID);
}

export function shouldRefreshBoardFromProjectEvent(
  params: unknown,
  boardQueryWorkflowID: string,
  selectedWorkflowID: string,
): boolean {
  const event = workflowProjectEvent(params);
  if (event === null) {
    return true;
  }
  if (event.resource === "workflow_link") {
    return true;
  }
  if (event.resource === "workflow" || event.resource === "task") {
    return (
      event.workflowID.length === 0 ||
      event.workflowID === boardQueryWorkflowID ||
      event.workflowID === selectedWorkflowID
    );
  }
  return false;
}

export function useBoardTaskActions(
  projectID: string,
  boardQueryWorkflowID: string,
  selectedWorkflowID: string,
) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  async function refresh(): Promise<void> {
    await queryClient.invalidateQueries({ queryKey: queryKeys.board(projectID, boardQueryWorkflowID) });
    if (selectedWorkflowID !== boardQueryWorkflowID) {
      await queryClient.invalidateQueries({ queryKey: queryKeys.board(projectID, selectedWorkflowID) });
    }
    await queryClient.invalidateQueries({
      queryKey: queryKeys.boardNodeCardsRoot(projectID, selectedWorkflowID),
    });
  }
  async function refreshAfterTaskDelete(taskID: string): Promise<void> {
    await Promise.all([
      refresh(),
      queryClient.invalidateQueries({ queryKey: queryKeys.task(taskID) }),
      queryClient.invalidateQueries({ queryKey: queryKeys.activity(taskID) }),
      queryClient.invalidateQueries({ queryKey: queryKeys.allTasks }),
      queryClient.invalidateQueries({ queryKey: queryKeys.allActivity }),
      queryClient.invalidateQueries({ queryKey: queryKeys.allAttention }),
      queryClient.invalidateQueries({ queryKey: queryKeys.attention("") }),
      queryClient.invalidateQueries({ queryKey: queryKeys.attention(projectID) }),
    ]);
  }
  return {
    start: useMutation({
      mutationFn: async (input: TaskStartInput) => api.startTask(input),
    }),
    move: useMutation({
      mutationFn: async (input: TaskMoveInput) => api.moveTask(input),
    }),
    interrupt: useMutation({
      mutationFn: async (taskID: string) => api.interruptTask(taskID),
      onSuccess: refresh,
    }),
    delete: useMutation({
      mutationFn: async (taskID: string) => api.deleteTask(taskID),
      onSuccess: async (_result, taskID) => {
        await refreshAfterTaskDelete(taskID);
      },
    }),
    resume: useMutation({
      mutationFn: async (taskID: string) => api.resumeTask(taskID),
      onSuccess: refresh,
    }),
    refresh,
  };
}
