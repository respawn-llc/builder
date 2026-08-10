import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef } from "react";

import type { OffsetPage, QuestionAnswerInput, TaskDetail } from "@/api";
import { errorMessage } from "@/api";
import { invalidateProjectTaskSearches, queryKeys } from "@/app-facade";
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
  const taskID = detail.id;
  const projectID = detail.projectID;
  const relatedTaskIDs = useMemo(() => dependencyRelatedTaskIDs(detail), [detail]);
  const relatedTaskIDsRef = useRef(relatedTaskIDs);

  useEffect(() => {
    relatedTaskIDsRef.current = relatedTaskIDs;
  }, [relatedTaskIDs]);

  useEffect(() => {
    if (!enabled || taskID.length === 0 || projectID.length === 0 || connectionPhase !== "connected") {
      return;
    }
    const refresh = async (): Promise<void> => {
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: queryKeys.task(taskID),
          refetchType: "active",
        }),
        queryClient.invalidateQueries({
          queryKey: queryKeys.taskAttention(taskID),
          refetchType: "active",
        }),
        queryClient.invalidateQueries({
          queryKey: queryKeys.activity(taskID),
          refetchType: "active",
        }),
        queryClient.invalidateQueries({
          queryKey: queryKeys.comments(taskID),
          refetchType: "active",
        }),
        queryClient.invalidateQueries({ queryKey: queryKeys.allPendingAsks, refetchType: "active" }),
      ]);
    };
    const refreshOrReport = (): void => {
      void refresh().catch((error: unknown) => {
        void logger.append("warn", "Task detail live refresh failed.", { error: errorMessage(error) });
      });
    };
    const subscription = api.subscribeProject(projectID, {
      onOpen() {
        refreshOrReport();
      },
      onEvent(event) {
        if (!workflowProjectEventAffectsDependencyDetail(event, taskID, relatedTaskIDsRef.current)) {
          return;
        }
        refreshOrReport();
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
  }, [api, connectionGeneration, connectionPhase, enabled, logger, projectID, queryClient, taskID]);
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

const taskDetailFeedPageSize = 50;
const taskDetailFeedMaxPages = 10;

export type TaskDetailFeedPage<T> = OffsetPage<T> & Readonly<{ offset: number }>;

function taskDetailFeedOptions<T>(
  queryKey: readonly unknown[],
  enabled: boolean,
  loadPage: (offset: number) => Promise<OffsetPage<T>>,
) {
  return {
    queryKey,
    queryFn: async ({ pageParam }: { pageParam: number }): Promise<TaskDetailFeedPage<T>> => ({
      ...(await loadPage(pageParam)),
      offset: pageParam,
    }),
    enabled,
    initialPageParam: 0,
    getNextPageParam: (lastPage: TaskDetailFeedPage<T>) => lastPage.nextOffset ?? undefined,
    getPreviousPageParam: (firstPage: TaskDetailFeedPage<T>) =>
      firstPage.offset === 0 ? undefined : firstPage.offset - taskDetailFeedPageSize,
    maxPages: taskDetailFeedMaxPages,
  };
}

export function useTaskActivity(taskID: string, enabled: boolean) {
  const { api } = useAppServices();
  return useInfiniteQuery(
    taskDetailFeedOptions(queryKeys.activity(taskID), enabled && taskID.length > 0, async (offset) =>
      api.listTaskActivity(taskID, offset),
    ),
  );
}

export function useTaskComments(taskID: string, enabled: boolean) {
  const { api } = useAppServices();
  return useInfiniteQuery(
    taskDetailFeedOptions(queryKeys.comments(taskID), enabled && taskID.length > 0, async (offset) =>
      api.listTaskComments(taskID, offset),
    ),
  );
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

type TaskLifecycleAction = "dependency_remove" | "interrupt";

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
    await invalidateProjectTaskSearches(queryClient, projectID);
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
        const previous = queryClient.getQueryData<TaskDetail>(queryKeys.task(taskID)) ?? null;
        queryClient.setQueryData<TaskDetail>(queryKeys.task(taskID), (current) =>
          current === undefined ? current : optimisticTaskDependencyRemoval(current, pair),
        );
        return { previous };
      },
      onError: async (error, _pair, context) => {
        if (context?.previous != null) {
          queryClient.setQueryData(queryKeys.task(taskID), context.previous);
        }
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
    approveApproval: useMutation({
      mutationFn: async (approvalID: string) => api.approveApproval(approvalID),
      onSuccess: refresh,
    }),
    answerQuestion: useMutation({
      mutationFn: async (input: QuestionAnswerInput) => api.answerQuestion(input),
    }),
  };
}
