import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type UseInfiniteQueryResult,
} from "@tanstack/react-query";
import { useCallback, useEffect } from "react";

import {
  errorMessage,
  noTaskLabelFilter,
  type ProjectTaskGroupCounts,
  type ProjectTaskGroup,
  type TaskListItem,
  type TaskListPage,
  type WorkflowProjectEvent,
} from "@/api";
import { queryKeys, useAppServices, useConnectionSnapshot } from "@/app-facade";

export const projectTaskGroups = ["active", "backlog", "done"] as const satisfies readonly ProjectTaskGroup[];
export type { ProjectTaskGroup } from "@/api";

export const projectTaskGroupPageSize = 25;
export const projectTaskGroupRetainedPages = 3;

export type ProjectTaskGroupDisclosure = Readonly<Record<ProjectTaskGroup, boolean>>;
export type ProjectTaskGroupData = Readonly<{
  error: Error | null;
  fetchNextPage(): Promise<unknown>;
  fetchPreviousPage(): Promise<unknown>;
  hasNextPage: boolean;
  hasPreviousPage: boolean;
  isError: boolean;
  isFetchNextPageError: boolean;
  isFetchPreviousPageError: boolean;
  isFetching: boolean;
  isFetchingNextPage: boolean;
  isFetchingPreviousPage: boolean;
  isPending: boolean;
  nextRequestGeneration: string;
  pages: readonly TaskListPage[];
  previousRequestGeneration: string;
  refetch(): Promise<unknown>;
  tasks: readonly TaskListItem[];
}>;

export type ProjectTaskListData = Readonly<{
  active: ProjectTaskGroupData;
  backlog: ProjectTaskGroupData;
  counts: ReturnType<typeof useProjectTaskGroupCounts>;
  done: ProjectTaskGroupData;
}>;

const updatedDescending = [{ field: "updated", direction: "desc" }] as const;

export function useProjectTaskListData({
  expanded,
  projectID,
}: Readonly<{
  expanded: ProjectTaskGroupDisclosure;
  projectID: string;
}>): ProjectTaskListData {
  const counts = useProjectTaskGroupCounts(projectID);
  const active = useProjectTaskGroupData(projectID, "active", expanded.active);
  const backlog = useProjectTaskGroupData(projectID, "backlog", expanded.backlog);
  const done = useProjectTaskGroupData(projectID, "done", expanded.done);
  return { active, backlog, counts, done };
}

function useProjectTaskGroupCounts(projectID: string) {
  const { api } = useAppServices();
  return useQuery({
    queryKey: queryKeys.projectTaskGroupCounts(projectID),
    queryFn: async (): Promise<ProjectTaskGroupCounts> =>
      api.getProjectTaskGroupCounts({
        projectID,
      }),
    enabled: projectID.length > 0,
    placeholderData: (previous) => previous,
  });
}

function useProjectTaskGroupData(
  projectID: string,
  group: ProjectTaskGroup,
  enabled: boolean,
): ProjectTaskGroupData {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  const queryKey = queryKeys.projectTaskGroup(projectID, group);
  const query = useInfiniteQuery<
    TaskListPage,
    Error,
    InfiniteData<TaskListPage, number>,
    readonly unknown[],
    number
  >({
    queryKey,
    queryFn: async ({ pageParam }) =>
      api.listTasks({
        projectID,
        group,
        labelFilter: noTaskLabelFilter,
        sort: updatedDescending,
        offset: pageParam,
        limit: projectTaskGroupPageSize,
      }),
    initialPageParam: 0,
    enabled: enabled && projectID.length > 0,
    getPreviousPageParam: (_firstPage, _allPages, firstPageParam) =>
      firstPageParam === 0 ? undefined : Math.max(0, firstPageParam - projectTaskGroupPageSize),
    getNextPageParam: (lastPage) => lastPage.nextOffset ?? undefined,
    maxPages: projectTaskGroupRetainedPages,
  });
  useEffect(() => {
    if (enabled) {
      return;
    }
    queryClient.removeQueries({
      queryKey: queryKeys.projectTaskGroup(projectID, group),
      exact: true,
    });
  }, [enabled, group, projectID, queryClient]);
  return projectTaskGroupData(query, enabled, projectID);
}

function projectTaskGroupData(
  query: UseInfiniteQueryResult<InfiniteData<TaskListPage, number>>,
  enabled: boolean,
  projectID: string,
): ProjectTaskGroupData {
  if (!enabled) {
    return emptyProjectTaskGroupData;
  }
  const pages = query.data?.pages ?? [];
  const pageParams = query.data?.pageParams ?? [];
  const firstPageParam = pageParams[0] ?? 0;
  const nextPageParam = pages.at(-1)?.nextOffset;
  return {
    error: query.error,
    fetchNextPage: query.fetchNextPage,
    fetchPreviousPage: query.fetchPreviousPage,
    hasNextPage: query.hasNextPage,
    hasPreviousPage: query.hasPreviousPage,
    isError: query.isError,
    isFetchNextPageError: query.isFetchNextPageError,
    isFetchPreviousPageError: query.isFetchPreviousPageError,
    isFetching: query.isFetching,
    isFetchingNextPage: query.isFetchingNextPage,
    isFetchingPreviousPage: query.isFetchingPreviousPage,
    isPending: query.isPending,
    nextRequestGeneration: `${projectID}:${nextPageParam?.toString() ?? "end"}`,
    pages,
    previousRequestGeneration: `${projectID}:${firstPageParam.toString()}`,
    refetch: query.refetch,
    tasks: pages.flatMap((page) => page.tasks),
  };
}

const emptyProjectTaskGroupData: ProjectTaskGroupData = {
  error: null,
  fetchNextPage: async () => undefined,
  fetchPreviousPage: async () => undefined,
  hasNextPage: false,
  hasPreviousPage: false,
  isError: false,
  isFetchNextPageError: false,
  isFetchPreviousPageError: false,
  isFetching: false,
  isFetchingNextPage: false,
  isFetchingPreviousPage: false,
  isPending: false,
  nextRequestGeneration: "disabled",
  pages: [],
  previousRequestGeneration: "disabled",
  refetch: async () => undefined,
  tasks: [],
};

export function useProjectTaskListEvents({
  enabled,
  projectID,
}: Readonly<{
  enabled: boolean;
  projectID: string;
}>): void {
  const { api, logger } = useAppServices();
  const connection = useConnectionSnapshot();
  const queryClient = useQueryClient();
  const reportBackgroundError = useCallback(
    (error: unknown): void => {
      void logger.append("warn", "Project Task-list refresh failed.", {
        error: errorMessage(error),
        projectID,
      });
    },
    [logger, projectID],
  );
  const refreshTaskLists = useCallback(async (): Promise<void> => {
    await queryClient.invalidateQueries({
      queryKey: queryKeys.projectTaskListsRoot(projectID),
      refetchType: "active",
    });
  }, [projectID, queryClient]);
  const refreshWorkflows = useCallback(async (): Promise<void> => {
    await Promise.all([
      queryClient.invalidateQueries({
        queryKey: queryKeys.projectWorkflowLinks(projectID),
        exact: true,
        refetchType: "active",
      }),
      queryClient.resetQueries({
        queryKey: queryKeys.projectTaskWorkflows(projectID),
        exact: true,
      }),
      queryClient.invalidateQueries({
        queryKey: queryKeys.projectBoardsRoot(projectID),
        refetchType: "active",
      }),
    ]);
  }, [projectID, queryClient]);
  const refreshBoundary = useCallback(async (): Promise<void> => {
    await Promise.all([refreshWorkflows(), refreshTaskLists()]);
  }, [refreshTaskLists, refreshWorkflows]);

  useEffect(() => {
    if (!enabled || projectID.length === 0 || connection.phase !== "connected") {
      return;
    }
    const run = (operation: Promise<void>): void => {
      void operation.catch(reportBackgroundError);
    };
    const subscription = api.subscribeProject(projectID, {
      onOpen() {
        run(refreshBoundary());
      },
      onEvent(event) {
        if (event.projectID !== null && event.projectID !== projectID) {
          return;
        }
        if (event.resource === "workflow" || event.resource === "workflow_link") {
          run(Promise.all([refreshWorkflows(), refreshTaskLists()]).then(() => undefined));
          return;
        }
        if (event.resource === "label") {
          run(refreshTaskLists());
          return;
        }
        if (projectTaskListEventCanChangeRows(event)) {
          run(refreshTaskLists());
        }
      },
      onComplete() {
        run(refreshBoundary());
      },
      onError(error) {
        reportBackgroundError(error);
        run(refreshBoundary());
      },
    });
    return () => {
      subscription.close();
    };
  }, [
    api,
    connection.generation,
    connection.phase,
    enabled,
    projectID,
    refreshBoundary,
    refreshTaskLists,
    refreshWorkflows,
    reportBackgroundError,
  ]);
}

export function projectTaskListEventCanChangeRows(event: WorkflowProjectEvent): boolean {
  if (event.resource === "label") {
    return true;
  }
  if (event.resource !== "task") {
    return false;
  }
  return taskListChangingActions.has(event.action);
}

const taskListChangingActions: ReadonlySet<WorkflowProjectEvent["action"]> = new Set([
  "created",
  "updated",
  "deleted",
  "started",
  "interrupted",
  "resumed",
  "approved",
  "moved",
  "completed",
  "question_waiting",
  "question_cleared",
  "labels_changed",
  "dependencies_changed",
]);
