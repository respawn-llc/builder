import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
} from "@tanstack/react-query";
import { useCallback, useEffect } from "react";

import { boardNodeCardsPageSize, type BoardNodeCardsPage, type WorkflowProjectEvent } from "@/api";
import {
  invalidateProjectBoardQueries,
  invalidateProjectTaskSearches,
  queryKeys,
  reportNonCancelledError,
} from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useConnectionSnapshot } from "@/app-facade";
import { workflowProjectEventCanChangeTaskSearch } from "@/app-facade";
import { workflowProjectQuestionTaskID } from "@/app-facade";
import { useRetainedQueryData } from "@/app-facade";
import { useProjectLabelEffects } from "@/shared/labels";
import { workflowProjectEventAffectsDependencyBoard } from "@/shared/task-dependencies";
import { useBoardQuery } from "./BoardQueryRuntime";
import { useBoardTaskLifecycleAction } from "./useBoardTaskLifecycleAction";

export function useBoard(projectID: string, workflowID: string | undefined) {
  const { api } = useAppServices();
  const { filter, queriesEnabled } = useBoardQuery();
  const query = useQuery({
    queryKey: queryKeys.board(projectID, workflowID, filter),
    queryFn: async () => api.getBoard(projectID, workflowID, filter),
    enabled: queriesEnabled && projectID.trim().length > 0,
    gcTime: 0,
    placeholderData: (previous) => previous,
  });
  const data = useRetainedQueryData({ projectID, workflowID }, query.data, boardScopesEqual);
  return {
    data,
    error: query.error,
    isError: query.isError,
    isPending: query.isPending && data === undefined,
    refetch: query.refetch,
  };
}

type BoardScope = Readonly<{
  projectID: string;
  workflowID: string | undefined;
}>;

function boardScopesEqual(left: BoardScope, right: BoardScope): boolean {
  return left.projectID === right.projectID && left.workflowID === right.workflowID;
}

export function useBoardNodeCards(projectID: string, workflowID: string, nodeID: string, enabled: boolean) {
  const { api } = useAppServices();
  const { filter, queriesEnabled, sort } = useBoardQuery();
  const query = useInfiniteQuery<
    BoardNodeCardsPage,
    Error,
    InfiniteData<BoardNodeCardsPage, number>,
    readonly unknown[],
    number
  >({
    queryKey: queryKeys.boardNodeCards({
      filter,
      nodeID,
      projectID,
      sort,
      workflowID,
    }),
    queryFn: async ({ pageParam }) =>
      api.listBoardNodeCards({
        projectID,
        workflowID,
        nodeID,
        filter,
        offset: pageParam,
        sort,
      }),
    initialPageParam: 0,
    enabled: queriesEnabled && enabled && projectID.length > 0 && workflowID.length > 0 && nodeID.length > 0,
    getPreviousPageParam: (_firstPage, _allPages, firstPageParam) =>
      firstPageParam === 0 ? undefined : Math.max(0, firstPageParam - boardNodeCardsPageSize),
    getNextPageParam: (lastPage) => lastPage.nextOffset ?? undefined,
    maxPages: 3,
    gcTime: 0,
    placeholderData: (previous) => previous,
  });
  const data = useRetainedQueryData({ nodeID, projectID, workflowID }, query.data, cardScopesEqual);
  if (data === query.data) {
    return query;
  }
  return {
    ...query,
    data,
    isPlaceholderData: data !== undefined || query.isPlaceholderData,
  };
}

type CardScope = Readonly<{
  nodeID: string;
  projectID: string;
  workflowID: string;
}>;

function cardScopesEqual(left: CardScope, right: CardScope): boolean {
  return (
    left.nodeID === right.nodeID && left.projectID === right.projectID && left.workflowID === right.workflowID
  );
}

export function useProjectBoardSubscription(
  projectID: string,
  boardQueryWorkflowID: string | undefined,
  input: Readonly<{
    selectedWorkflowID: string | undefined;
    selectedTaskID?: string;
    onBackgroundError?: (error: unknown) => void;
    onSelectedTaskDeleted?: () => void;
  }>,
) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  const connection = useConnectionSnapshot();
  const labelEffects = useProjectLabelEffects();
  const { onBackgroundError, onSelectedTaskDeleted, selectedTaskID, selectedWorkflowID } = input;
  const consumeBackgroundError = useCallback(
    (error: unknown): void => {
      reportNonCancelledError(error, (failure) => onBackgroundError?.(failure));
    },
    [onBackgroundError],
  );

  useEffect(() => {
    if (projectID.length === 0 || connection.phase !== "connected") {
      return;
    }
    async function refresh(): Promise<void> {
      await Promise.all([
        invalidateProjectBoardQueries(queryClient, projectID),
        queryClient.invalidateQueries({ queryKey: queryKeys.attention }),
      ]);
    }
    async function refreshAttention(): Promise<void> {
      await queryClient.invalidateQueries({ queryKey: queryKeys.attention });
    }
    async function refreshQuestionTask(event: WorkflowProjectEvent): Promise<void> {
      const taskID = workflowProjectQuestionTaskID(event);
      if (taskID === null) {
        return;
      }
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.task(taskID), refetchType: "active" }),
        queryClient.invalidateQueries({ queryKey: queryKeys.activity(taskID), refetchType: "active" }),
        queryClient.invalidateQueries({ queryKey: queryKeys.allPendingAsks, refetchType: "active" }),
      ]);
    }
    async function refreshTaskSearch(): Promise<void> {
      await invalidateProjectTaskSearches(queryClient, projectID);
    }
    async function refreshSubscriptionBoundary(): Promise<void> {
      await Promise.all([refreshAttention(), refreshTaskSearch()]);
    }
    const subscription = api.subscribeProject(projectID, {
      onOpen() {
        void labelEffects.refreshAfterSubscriptionBoundary().catch(consumeBackgroundError);
        void refreshSubscriptionBoundary().catch(consumeBackgroundError);
      },
      onEvent(event) {
        void labelEffects.consumeProjectEvent(event).catch(consumeBackgroundError);
        if (isDeletedTaskEvent(event, selectedTaskID)) {
          onSelectedTaskDeleted?.();
        }
        void refreshQuestionTask(event).catch(consumeBackgroundError);
        if (workflowProjectEventCanChangeTaskSearch(event)) {
          void refreshTaskSearch().catch(consumeBackgroundError);
        }
        if (shouldRefreshBoardFromProjectEvent(event, boardQueryWorkflowID, selectedWorkflowID)) {
          void refresh().catch(consumeBackgroundError);
        }
      },
      onComplete() {
        return;
      },
      onError(error) {
        consumeBackgroundError(error);
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
    labelEffects,
    onBackgroundError,
    onSelectedTaskDeleted,
    projectID,
    queryClient,
    selectedTaskID,
    selectedWorkflowID,
  ]);
}

function isDeletedTaskEvent(event: WorkflowProjectEvent, taskID: string | undefined): boolean {
  if (taskID === undefined) {
    return false;
  }
  const trimmedTaskID = taskID.trim();
  if (trimmedTaskID.length === 0) {
    return false;
  }
  return event.resource === "task" && event.action === "deleted" && event.primaryEntityID === trimmedTaskID;
}

export function shouldRefreshBoardFromProjectEvent(
  event: WorkflowProjectEvent,
  boardQueryWorkflowID: string | undefined,
  selectedWorkflowID: string | undefined,
): boolean {
  if (event.resource === "task" && event.action === "labels_changed" && event.workflowID !== null) {
    return false;
  }
  return (
    workflowProjectEventAffectsDependencyBoard(event, boardQueryWorkflowID) ||
    workflowProjectEventAffectsDependencyBoard(event, selectedWorkflowID)
  );
}

export function useBoardTaskActions(projectID: string) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  const refresh = useCallback(async (): Promise<void> => {
    await Promise.all([
      invalidateProjectBoardQueries(queryClient, projectID),
      invalidateProjectTaskSearches(queryClient, projectID),
    ]);
  }, [projectID, queryClient]);
  const refreshAfterTaskDelete = useCallback(
    async (taskID: string): Promise<void> => {
      await Promise.all([
        refresh(),
        queryClient.invalidateQueries({ queryKey: queryKeys.task(taskID) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.taskAttention(taskID) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.activity(taskID) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.allTasks }),
        queryClient.invalidateQueries({ queryKey: queryKeys.allActivity }),
        queryClient.invalidateQueries({ queryKey: queryKeys.allAttention }),
        queryClient.invalidateQueries({ queryKey: queryKeys.attention }),
      ]);
    },
    [queryClient, refresh],
  );
  const interruptMutation = useMutation({
    mutationFn: async (taskID: string) => api.interruptTask(taskID),
    onSettled: refresh,
  });
  const interrupt = useBoardTaskLifecycleAction();
  return {
    refresh,
    interrupt: {
      execute: async (taskID: string) =>
        interrupt.execute(taskID, async () => interruptMutation.mutateAsync(taskID)),
      pendingTaskIDs: interrupt.pendingTaskIDs,
    },
    delete: useMutation({
      mutationFn: async (taskID: string) => api.deleteTask(taskID),
      onSuccess: async (_result, taskID) => {
        await refreshAfterTaskDelete(taskID);
      },
    }),
  };
}
