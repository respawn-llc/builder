import type { InfiniteData, QueryClient } from "@tanstack/react-query";

import type { BoardNodeCardsPage, TaskDetail, TaskLabelAssignment, TaskListPage } from "@/api";
import { queryKeys } from "@/app-facade";

export function patchExistingTaskLabelProjections(
  queryClient: QueryClient,
  taskID: string,
  labelIDs: readonly string[],
): void {
  const detailKey = queryKeys.task(taskID);
  const detail = queryClient.getQueryData<TaskDetail>(detailKey);
  if (detail !== undefined) {
    queryClient.setQueryData<TaskDetail>(detailKey, {
      ...detail,
      labelIDs: [...labelIDs],
    });
  }
  transformExistingPagedTaskLabels(queryClient, (candidateTaskID, currentLabelIDs) =>
    candidateTaskID === taskID ? labelIDs : currentLabelIDs,
  );
}

type TaskLabelTransform = (taskID: string, labelIDs: readonly string[]) => readonly string[];

function transformExistingPagedTaskLabels(queryClient: QueryClient, transform: TaskLabelTransform): void {
  queryClient.setQueriesData<InfiniteData<BoardNodeCardsPage, string | null>>(
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
  queryClient.setQueriesData<TaskListPage>({ queryKey: queryKeys.allTaskLists }, (page) => {
    if (page === undefined) {
      return undefined;
    }
    return {
      ...page,
      tasks: page.tasks.map((task) => ({
        ...task,
        labelIDs: [...transform(task.id, task.labelIDs)],
      })),
    };
  });
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

export function pruneDeletedLabelFromExistingCaches(queryClient: QueryClient, labelID: string): void {
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
  transformExistingPagedTaskLabels(queryClient, (_taskID, labelIDs) =>
    labelIDs.filter((assignedLabelID) => assignedLabelID !== labelID),
  );
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
  queryClient.setQueriesData<InfiniteData<BoardNodeCardsPage, string | null>>(
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
  queryClient.setQueriesData<TaskListPage>({ queryKey: queryKeys.allTaskLists }, (page) =>
    page === undefined
      ? undefined
      : {
          ...page,
          tasks: page.tasks.filter((task) => task.id !== taskID),
        },
  );
}
