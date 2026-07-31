import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

import type { QuestionAnswerInput, TaskDetail } from "@/api";
import { errorMessage } from "@/api";
import { queryKeys } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useConnectionSnapshot } from "@/app-facade";
import {
  dependencyRelatedTaskIDs,
  optimisticTaskDependencyRemoval,
  workflowProjectEventAffectsDependencyDetail,
  type TaskDependencyPair,
} from "@/shared/task-dependencies";

// useTaskDetailLiveRefresh keeps an open task detail in sync with the server by
// subscribing to its project's workflow events. Any event that mutates this
// task (status, Current Nodes/Approvals, comments, questions, title/body)
// invalidates the detail's queries so the surface refreshes on its own,
// regardless of which route hosts it (board sidebar, attention inbox, or the
// standalone task window). Invalidations target active observers only and reuse
// existing cache data during the background refetch, so the refresh is
// flicker-free and never collapses the surface back to a loading state.
export function useTaskDetailLiveRefresh(detail: TaskDetail, enabled: boolean) {
  const { api, logger } = useAppServices();
  const queryClient = useQueryClient();
  const connection = useConnectionSnapshot();
  const connectionPhase = connection.phase;
  const connectionGeneration = connection.generation;

  useEffect(() => {
    if (
      !enabled ||
      detail.id.length === 0 ||
      detail.projectID.length === 0 ||
      connectionPhase !== "connected"
    ) {
      return;
    }
    const relatedTaskIDs = dependencyRelatedTaskIDs(detail);
    const subscription = api.subscribeProject(detail.projectID, {
      onEvent(event) {
        if (!workflowProjectEventAffectsDependencyDetail(event, detail.id, relatedTaskIDs)) {
          return;
        }
        void Promise.all([
          queryClient.invalidateQueries({
            queryKey: queryKeys.task(detail.id),
            refetchType: "active",
          }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.taskAttention(detail.id),
            refetchType: "active",
          }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.activity(detail.id),
            refetchType: "active",
          }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.comments(detail.id),
            refetchType: "active",
          }),
          queryClient.invalidateQueries({ queryKey: queryKeys.allPendingAsks, refetchType: "active" }),
        ]).catch((error: unknown) => {
          void logger.append("warn", "Task detail live refresh failed.", { error: errorMessage(error) });
        });
      },
      onComplete() {
        return;
      },
      onError(error) {
        void logger.append("warn", "Task detail subscription failed.", { error: errorMessage(error) });
      },
    });
    return () => {
      subscription.close();
    };
  }, [api, connectionGeneration, connectionPhase, detail, enabled, logger, queryClient]);
}

export function useTaskDetail(taskID: string, enabled: boolean) {
  const { api } = useAppServices();
  return useQuery({
    queryKey: queryKeys.task(taskID),
    queryFn: async () => api.getTask(taskID),
    enabled: enabled && taskID.length > 0,
  });
}

export function useTaskAttention(taskID: string, enabled: boolean) {
  const { api } = useAppServices();
  return useQuery({
    queryKey: queryKeys.taskAttention(taskID),
    queryFn: async () => api.listTaskAttention(taskID),
    enabled: enabled && taskID.length > 0,
  });
}

export function useTaskActivity(taskID: string, enabled: boolean) {
  const { api } = useAppServices();
  return useInfiniteQuery({
    queryKey: queryKeys.activity(taskID),
    queryFn: async ({ pageParam }) => api.listTaskActivity(taskID, pageParam),
    enabled: enabled && taskID.length > 0,
    initialPageParam: "",
    getNextPageParam: (lastPage) => (lastPage.nextPageToken.length > 0 ? lastPage.nextPageToken : undefined),
  });
}

export function useTaskComments(taskID: string, enabled: boolean) {
  const { api } = useAppServices();
  return useInfiniteQuery({
    queryKey: queryKeys.comments(taskID),
    queryFn: async ({ pageParam }) => api.listTaskComments(taskID, pageParam),
    enabled: enabled && taskID.length > 0,
    initialPageParam: 0,
    getNextPageParam: (lastPage) => lastPage.nextOffset ?? undefined,
  });
}

export function usePendingAsks(sessionID: string | null) {
  const { api } = useAppServices();
  return useQuery({
    queryKey: queryKeys.pendingAsks(sessionID),
    queryFn: async () => {
      if (sessionID === null) {
        throw new Error("pending ask lookup requires an enabled session");
      }
      if (sessionID.length === 0) {
        throw new Error("pending ask lookup requires a non-empty session id");
      }
      return api.listPendingAsks(sessionID);
    },
    enabled: sessionID !== null && sessionID.length > 0,
    refetchOnMount: "always",
  });
}

type TaskLifecycleAction = "dependency_remove" | "interrupt" | "resume";

type TaskMutationCallbacks = Readonly<{
  onChanged?: (() => void) | undefined;
  onActionError?: ((action: TaskLifecycleAction, error: unknown) => void) | undefined;
}>;

export function useTaskMutations(
  taskID: string,
  projectID: string,
  { onActionError, onChanged }: TaskMutationCallbacks = {},
) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  async function refresh(): Promise<void> {
    await queryClient.invalidateQueries({ queryKey: queryKeys.task(taskID) });
    await queryClient.invalidateQueries({ queryKey: queryKeys.taskAttention(taskID) });
    await queryClient.invalidateQueries({ queryKey: queryKeys.activity(taskID) });
    await queryClient.invalidateQueries({ queryKey: queryKeys.comments(taskID) });
    await queryClient.invalidateQueries({ queryKey: queryKeys.projects });
    await queryClient.invalidateQueries({ queryKey: queryKeys.allAttention });
    await queryClient.invalidateQueries({ queryKey: queryKeys.allBoards });
    await queryClient.invalidateQueries({ queryKey: queryKeys.allBoardNodeCards });
    await queryClient.invalidateQueries({ queryKey: queryKeys.allTasks });
    await queryClient.invalidateQueries({ queryKey: queryKeys.allActivity });
    await queryClient.invalidateQueries({ queryKey: queryKeys.allPendingAsks });
    onChanged?.();
  }
  return {
    refresh,
    addComment: useMutation({
      mutationFn: async (body: string) => api.addComment(taskID, body),
      onSuccess: refresh,
    }),
    replaceComment: useMutation({
      mutationFn: async (input: Readonly<{ commentID: string; body: string }>) =>
        api.replaceComment(input.commentID, input.body),
      onSuccess: refresh,
    }),
    deleteComment: useMutation({
      mutationFn: async (commentID: string) => api.deleteComment(commentID),
      onSuccess: refresh,
    }),
    removeDependency: useMutation({
      mutationFn: async (pair: TaskDependencyPair) =>
        api.removeTaskDependency(pair.blockerTaskID, pair.blockedTaskID),
      onMutate: async (pair) => {
        await queryClient.cancelQueries({ queryKey: queryKeys.task(taskID) });
        queryClient.setQueryData<TaskDetail>(queryKeys.task(taskID), (current) =>
          current === undefined ? current : optimisticTaskDependencyRemoval(current, pair),
        );
      },
      onError: async (error) => {
        onActionError?.("dependency_remove", error);
        await queryClient.invalidateQueries({ queryKey: queryKeys.task(taskID) });
      },
      onSuccess: async (_response, pair) => {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: queryKeys.task(pair.blockerTaskID) }),
          queryClient.invalidateQueries({ queryKey: queryKeys.task(pair.blockedTaskID) }),
          queryClient.invalidateQueries({ queryKey: queryKeys.projectBoardsRoot(projectID) }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.projectBoardNodeCardsRoot(projectID),
          }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.projectTaskListsRoot(projectID),
          }),
        ]);
        onChanged?.();
      },
    }),
    interrupt: useMutation({
      mutationFn: async (sessionID?: string) => api.interruptTask(taskID, sessionID),
      onError: (error) => {
        onActionError?.("interrupt", error);
      },
      onSettled: refresh,
    }),
    resume: useMutation({
      mutationFn: async () => api.resumeTask(taskID),
      onError: (error) => {
        onActionError?.("resume", error);
      },
      onSuccess: refresh,
    }),
    approveApproval: useMutation({
      mutationFn: async (approvalID: string) => api.approveApproval(approvalID),
      onSuccess: refresh,
    }),
    answerQuestion: useMutation({
      mutationFn: async (input: QuestionAnswerInput) => api.answerQuestion(input),
    }),
  };
}
