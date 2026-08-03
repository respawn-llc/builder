import {
  hashKey,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
} from "@tanstack/react-query";
import { useCallback, useEffect } from "react";

import type { BoardNodeCardsPage, WorkflowProjectEvent } from "@/api";
import { invalidateProjectTaskSearches, queryKeys } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useConnectionSnapshot } from "@/app-facade";
import { workflowProjectEventCanChangeTaskSearch } from "@/app-facade";
import { workflowProjectQuestionTaskID } from "@/app-facade";
import { useProjectLabelEffects } from "@/shared/labels";
import { workflowProjectEventAffectsDependencyBoard } from "@/shared/task-dependencies";
import { useBoardFilterGeneration } from "./BoardFilterGenerationRuntime";
import { useRetainedQueryData } from "./useRetainedQueryData";
import { useBoardTaskLifecycleAction } from "./useBoardTaskLifecycleAction";

export function useBoard(projectID: string, workflowID: string | undefined) {
  const { api } = useAppServices();
  const { queriesEnabled, requestAdapter, snapshot } = useBoardFilterGeneration();
  const { active } = snapshot;
  const queryKey = queryKeys.board(projectID, workflowID, active.filter);
  const query = useQuery({
    queryKey,
    queryFn: async ({ signal }) =>
      requestAdapter.requestBoard({
        generation: active.generation,
        queryKey,
        requestIdentity: hashKey(queryKey),
        signal,
        transport: async () => api.getBoard(projectID, workflowID, active.filter),
      }),
    enabled: queriesEnabled && !active.retiring && projectID.trim().length > 0,
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
  const { queriesEnabled, requestAdapter, snapshot } = useBoardFilterGeneration();
  const { active } = snapshot;
  const queryKey = queryKeys.boardNodeCards(projectID, workflowID, nodeID, active.filter);
  const query = useInfiniteQuery<
    BoardNodeCardsPage,
    Error,
    InfiniteData<BoardNodeCardsPage, string | null>,
    readonly string[],
    string | null
  >({
    queryKey,
    queryFn: async ({ pageParam, signal }) =>
      requestAdapter.requestCards({
        generation: active.generation,
        queryKey,
        requestIdentity: hashKey([...queryKey, pageParam]),
        signal,
        transport: async () =>
          api.listBoardNodeCards({
            projectID,
            workflowID,
            nodeID,
            labelFilter: active.filter,
            pageToken: pageParam,
          }),
      }),
    initialPageParam: null,
    enabled:
      queriesEnabled &&
      !active.retiring &&
      enabled &&
      projectID.length > 0 &&
      workflowID.length > 0 &&
      nodeID.length > 0,
    getPreviousPageParam: (firstPage) => firstPage.previousPageToken ?? undefined,
    getNextPageParam: (lastPage) => lastPage.nextPageToken ?? undefined,
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
  const boardGeneration = useBoardFilterGeneration();
  const connection = useConnectionSnapshot();
  const labelEffects = useProjectLabelEffects();
  const { onBackgroundError, onSelectedTaskDeleted, selectedTaskID, selectedWorkflowID } = input;
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
      const activeGeneration = boardGeneration.controller.getSnapshot().active.generation;
      await boardGeneration.queryRegistry.invalidateGeneration(activeGeneration);
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
      await Promise.all([refresh(), refreshTaskSearch()]);
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
        void labelEffects.refreshAfterSubscriptionBoundary().catch(consumeBackgroundError);
        void refreshSubscriptionBoundary().catch(consumeBackgroundError);
      },
      onError(error) {
        consumeBackgroundError(error);
        void labelEffects.refreshAfterSubscriptionBoundary().catch(consumeBackgroundError);
        void refreshSubscriptionBoundary().catch(consumeBackgroundError);
      },
    });
    return () => {
      subscription.close();
    };
  }, [
    api,
    boardGeneration.controller,
    boardGeneration.queryRegistry,
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
  return (
    workflowProjectEventAffectsDependencyBoard(event, boardQueryWorkflowID) ||
    workflowProjectEventAffectsDependencyBoard(event, selectedWorkflowID)
  );
}

export function useBoardTaskActions(projectID: string) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  const boardGeneration = useBoardFilterGeneration();
  const refresh = useCallback(async (): Promise<void> => {
    const activeGeneration = boardGeneration.controller.getSnapshot().active.generation;
    await Promise.all([
      boardGeneration.queryRegistry.invalidateGeneration(activeGeneration),
      invalidateProjectTaskSearches(queryClient, projectID),
    ]);
  }, [boardGeneration.controller, boardGeneration.queryRegistry, projectID, queryClient]);
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
  return {
    refresh,
    interrupt: useBoardTaskLifecycleMutation(interruptMutation),
    delete: useMutation({
      mutationFn: async (taskID: string) => api.deleteTask(taskID),
      onSuccess: async (_result, taskID) => {
        await refreshAfterTaskDelete(taskID);
      },
    }),
  };
}

type BoardTaskLifecycleAction = Readonly<{
  execute(taskID: string): Promise<void>;
  pendingTaskIDs: ReadonlySet<string>;
}>;

function useBoardTaskLifecycleMutation(
  mutation: Readonly<{
    mutateAsync(taskID: string): Promise<void>;
  }>,
): BoardTaskLifecycleAction {
  const { execute: track, pendingTaskIDs } = useBoardTaskLifecycleAction();
  const { mutateAsync } = mutation;
  const execute = useCallback(
    async (taskID: string): Promise<void> => {
      await track(taskID, async () => {
        await mutateAsync(taskID);
      });
    },
    [mutateAsync, track],
  );
  return {
    execute,
    pendingTaskIDs,
  };
}
