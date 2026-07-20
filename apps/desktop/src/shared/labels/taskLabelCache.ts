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
          cards: page.cards.map((card) =>
            card.id === taskID
              ? {
                  ...card,
                  labelIDs: [...labelIDs],
                }
              : card,
          ),
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
      tasks: page.tasks.map((task) =>
        task.id === taskID
          ? {
              ...task,
              labelIDs: [...labelIDs],
            }
          : task,
      ),
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
