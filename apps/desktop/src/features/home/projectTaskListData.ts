import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type UseInfiniteQueryResult,
} from "@tanstack/react-query";
import { useCallback, useEffect, useReducer } from "react";

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
import { defaultProjectTaskSort, type ProjectTaskSort } from "./projectTaskSorting";

export const projectTaskGroups = ["active", "backlog", "done"] as const satisfies readonly ProjectTaskGroup[];
export type { ProjectTaskGroup } from "@/api";

export const projectTaskGroupPageSize = 50;
export const projectTaskGroupRetainedPages = 10;
export const projectTaskGroupPrefetchPages = 1;

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
  isSortReplacement: boolean;
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

export function projectTaskListWorkflowCardinality(
  data: ProjectTaskListData,
): TaskListPage["matchingWorkflowCardinality"] | undefined {
  for (const group of projectTaskGroups) {
    const page = data[group].pages.at(0);
    if (page !== undefined) return page.matchingWorkflowCardinality;
  }
  return undefined;
}

export function useProjectTaskListData({
  expanded,
  projectID,
  sort = defaultProjectTaskSort,
}: Readonly<{
  expanded: ProjectTaskGroupDisclosure;
  projectID: string;
  sort?: ProjectTaskSort;
}>): ProjectTaskListData {
  const counts = useProjectTaskGroupCounts(projectID);
  const active = useProjectTaskGroupData(projectID, "active", expanded.active, sort);
  const backlog = useProjectTaskGroupData(projectID, "backlog", expanded.backlog, sort);
  const done = useProjectTaskGroupData(projectID, "done", expanded.done, sort);
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
  sort: ProjectTaskSort,
): ProjectTaskGroupData {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  const queryKey = queryKeys.projectTaskGroup(projectID, group, sort);
  const queryKeyRoot = queryKeys.projectTaskGroupRoot(projectID, group);
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
        sort: [sort],
        offset: pageParam,
        limit: projectTaskGroupPageSize,
      }),
    initialPageParam: 0,
    enabled: enabled && projectID.length > 0,
    getPreviousPageParam: (_firstPage, _allPages, firstPageParam) =>
      firstPageParam === 0 ? undefined : Math.max(0, firstPageParam - projectTaskGroupPageSize),
    getNextPageParam: (lastPage) => lastPage.nextOffset ?? undefined,
    maxPages: projectTaskGroupRetainedPages,
    placeholderData: (previous) => previous,
  });
  const [generation, dispatchGeneration] = useReducer(projectTaskGenerationReducer, emptyGenerationState);
  const sortChanged = generation.currentSort !== null && !projectTaskSortsEqual(generation.currentSort, sort);
  const queryEstablished = !query.isPlaceholderData && !query.isError && query.data !== undefined;
  const targetEstablished = queryEstablished && query.data.pageParams[0] === 0;
  const isSortReplacement = projectTaskSortReplacement({
    hasSource: query.isPlaceholderData || generation.establishedData !== null,
    replacementSort: generation.replacementSort,
    sort,
    sortChanged,
    targetEstablished,
  });
  const displayedData = query.data ?? (isSortReplacement ? generation.establishedData : undefined);
  useEffect(() => {
    if (sortChanged) {
      dispatchGeneration({ kind: "selected", sort });
      return;
    }
    if (queryEstablished) {
      dispatchGeneration({ data: query.data, kind: "established", sort });
    }
  }, [query.data, queryEstablished, sort, sortChanged]);
  useEffect(() => {
    if (enabled) {
      return;
    }
    dispatchGeneration({ kind: "disabled" });
    queryClient.removeQueries({
      queryKey: queryKeyRoot,
    });
  }, [enabled, group, projectID, queryClient]);
  return projectTaskGroupData({
    displayedData: displayedData ?? undefined,
    enabled,
    isSortReplacement,
    projectID,
    query,
  });
}

type ProjectTaskGenerationState = Readonly<{
  currentSort: ProjectTaskSort | null;
  establishedData: InfiniteData<TaskListPage, number> | null;
  replacementSort: ProjectTaskSort | null;
}>;

type ProjectTaskGenerationAction =
  | Readonly<{ data: InfiniteData<TaskListPage, number>; kind: "established"; sort: ProjectTaskSort }>
  | Readonly<{ kind: "disabled" }>
  | Readonly<{ kind: "selected"; sort: ProjectTaskSort }>;

const emptyGenerationState: ProjectTaskGenerationState = {
  currentSort: null,
  establishedData: null,
  replacementSort: null,
};

function projectTaskGenerationReducer(
  state: ProjectTaskGenerationState,
  action: ProjectTaskGenerationAction,
): ProjectTaskGenerationState {
  if (action.kind === "disabled") {
    return emptyGenerationState;
  }
  if (action.kind === "selected") {
    return projectTaskSortsEqual(state.currentSort, action.sort)
      ? state
      : { ...state, currentSort: action.sort, replacementSort: action.sort };
  }
  return projectTaskSortsEqual(state.currentSort, action.sort) &&
    state.establishedData === action.data &&
    state.replacementSort === null
    ? state
    : { currentSort: action.sort, establishedData: action.data, replacementSort: null };
}

function projectTaskSortsEqual(left: ProjectTaskSort | null, right: ProjectTaskSort): boolean {
  return left !== null && left.field === right.field && left.direction === right.direction;
}

function projectTaskSortReplacement({
  hasSource,
  replacementSort,
  sort,
  sortChanged,
  targetEstablished,
}: Readonly<{
  hasSource: boolean;
  replacementSort: ProjectTaskSort | null;
  sort: ProjectTaskSort;
  sortChanged: boolean;
  targetEstablished: boolean;
}>): boolean {
  return (
    hasSource &&
    (sortChanged || (replacementSort !== null && projectTaskSortsEqual(replacementSort, sort))) &&
    !targetEstablished
  );
}

function projectTaskGroupData({
  displayedData,
  enabled,
  isSortReplacement,
  projectID,
  query,
}: Readonly<{
  displayedData: InfiniteData<TaskListPage, number> | undefined;
  enabled: boolean;
  isSortReplacement: boolean;
  projectID: string;
  query: UseInfiniteQueryResult<InfiniteData<TaskListPage, number>>;
}>): ProjectTaskGroupData {
  if (!enabled) {
    return emptyProjectTaskGroupData;
  }
  const pages = displayedData?.pages ?? [];
  const pageParams = displayedData?.pageParams ?? [];
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
    isSortReplacement,
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
  isSortReplacement: false,
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
