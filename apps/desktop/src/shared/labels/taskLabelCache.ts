import type { InfiniteData, QueryClient } from "@tanstack/react-query";

import type {
  BoardNodeCardsPage,
  ProjectLabel,
  ProjectLabelCatalog,
  ProjectTaskGroupCounts,
  TaskDetail,
  TaskLabelAssignment,
  TaskListPage,
} from "@/api";
import { queryKeys } from "@/app-facade";

export function patchExistingTaskLabelProjections(
  queryClient: QueryClient,
  taskID: string,
  labelIDs: readonly string[],
  catalogLabels?: readonly ProjectLabel[],
): void {
  const detailKey = queryKeys.task(taskID);
  const detail = queryClient.getQueryData<TaskDetail>(detailKey);
  if (detail !== undefined) {
    queryClient.setQueryData<TaskDetail>(detailKey, {
      ...detail,
      labelIDs: [...labelIDs],
    });
  }
  transformExistingBoardTaskLabels(queryClient, (candidateTaskID, currentLabelIDs) =>
    candidateTaskID === taskID ? labelIDs : currentLabelIDs,
  );
  if (catalogLabels === undefined) {
    return;
  }
  const assigned = new Set(labelIDs);
  const labels = catalogLabels.filter((label) => assigned.has(label.id));
  transformExistingTaskListPages(queryClient, (task) =>
    task.id === taskID ? { ...task, labels } : task,
  );
}

type TaskLabelTransform = (taskID: string, labelIDs: readonly string[]) => readonly string[];

function transformExistingBoardTaskLabels(queryClient: QueryClient, transform: TaskLabelTransform): void {
  queryClient.setQueriesData<InfiniteData<BoardNodeCardsPage, number>>(
    { queryKey: queryKeys.allBoardNodeCards },
    (data) => {
      if (data === undefined) {
        return undefined;
      }
      return {
        ...data,
        pages: data.pages.map((page) => ({
          ...page,
          cards: page.cards.map((card) => ({
            ...card,
            labelIDs: [...transform(card.id, card.labelIDs)],
          })),
        })),
      };
    },
  );
}

export function patchExistingTaskLabelAssignment(
  queryClient: QueryClient,
  assignment: TaskLabelAssignment,
): void {
  const key = queryKeys.taskLabels(assignment.taskID);
  if (queryClient.getQueryData(key) === undefined) {
    return;
  }
  const current = queryClient.getQueryData<TaskLabelAssignment>(key);
  const labelIDs = [...assignment.labelIDs];
  if (
    current?.taskID === assignment.taskID &&
    current.labelIDs.length === labelIDs.length &&
    current.labelIDs.every((labelID, index) => labelID === labelIDs[index])
  ) {
    return;
  }
  queryClient.setQueryData<TaskLabelAssignment>(key, {
    taskID: assignment.taskID,
    labelIDs,
  });
}

export function pruneDeletedLabelFromExistingCaches(
  queryClient: QueryClient,
  projectID: string,
  labelID: string,
): void {
  queryClient.setQueryData<ProjectLabelCatalog>(queryKeys.projectLabels(projectID), (catalog) =>
    catalog === undefined
      ? undefined
      : {
          ...catalog,
          labels: catalog.labels.filter((label) => label.id !== labelID),
        },
  );
  queryClient.setQueriesData<TaskLabelAssignment>({ queryKey: queryKeys.allTaskLabels }, (assignment) =>
    assignment === undefined
      ? undefined
      : {
          ...assignment,
          labelIDs: assignment.labelIDs.filter((assignedLabelID) => assignedLabelID !== labelID),
        },
  );
  queryClient.setQueriesData<TaskDetail>({ queryKey: queryKeys.allTasks }, (detail) =>
    detail === undefined
      ? undefined
      : {
          ...detail,
          labelIDs: detail.labelIDs.filter((assignedLabelID) => assignedLabelID !== labelID),
        },
  );
  transformExistingBoardTaskLabels(queryClient, (_taskID, labelIDs) =>
    labelIDs.filter((assignedLabelID) => assignedLabelID !== labelID),
  );
  transformExistingTaskListPages(queryClient, (task) => ({
    ...task,
    labels: task.labels.filter((label) => label.id !== labelID),
  }));
}

export function removeDeletedTaskFromExistingCaches(queryClient: QueryClient, taskID: string): void {
  queryClient.removeQueries({
    queryKey: queryKeys.taskLabels(taskID),
    exact: true,
  });
  queryClient.removeQueries({
    queryKey: queryKeys.task(taskID),
    exact: true,
  });
  queryClient.setQueriesData<InfiniteData<BoardNodeCardsPage, number>>(
    { queryKey: queryKeys.allBoardNodeCards },
    (data) =>
      data === undefined
        ? undefined
        : {
            ...data,
            pages: data.pages.map((page) => ({
              ...page,
              cards: page.cards.filter((card) => card.id !== taskID),
            })),
          },
  );
  transformExistingTaskListPages(queryClient, (task) => (task.id === taskID ? null : task));
}

type TaskListItem = TaskListPage["tasks"][number];
type TaskListCacheData = InfiniteData<TaskListPage, number> | ProjectTaskGroupCounts;

function transformExistingTaskListPages(
  queryClient: QueryClient,
  transform: (task: TaskListItem) => TaskListItem | null,
): void {
  queryClient.setQueriesData<TaskListCacheData>({ queryKey: queryKeys.allTaskLists }, (data) => {
    if (data === undefined || !("pages" in data)) {
      return data;
    }
    return {
      ...data,
      pages: data.pages.map((page) => ({
        ...page,
        tasks: page.tasks.flatMap((task) => {
          const transformed = transform(task);
          return transformed === null ? [] : [transformed];
        }),
      })),
    };
  });
}
