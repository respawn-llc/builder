import type { QueryClient } from "@tanstack/react-query";

import type { WorkflowProjectEvent } from "@/api";
import { invalidateProjectBoardQueries, queryKeys } from "@/app-facade";
import type { LabelFilterAction } from "./labelFilterState";
import { pruneDeletedLabelFromExistingCaches, removeDeletedTaskFromExistingCaches } from "./taskLabelCache";

export type ProjectLabelEffects = Readonly<{
  consumeProjectEvent(event: WorkflowProjectEvent): Promise<void>;
  refreshAfterSubscriptionBoundary(): Promise<void>;
  scheduleCatalogRefresh(): void;
  scheduleDeleteRefresh(): void;
  scheduleMembershipRefresh(): void;
  scheduleReorderRefresh(): void;
  scheduleTaskAssignmentRefresh(taskID: string): void;
}>;

export function createProjectLabelEffects({
  onFilterAction,
  onBackgroundError,
  projectID,
  queryClient,
}: Readonly<{
  onFilterAction?: ((action: LabelFilterAction) => void) | undefined;
  onBackgroundError: (error: unknown) => void;
  projectID: string;
  queryClient: QueryClient;
}>): ProjectLabelEffects {
  const catalogKey = queryKeys.projectLabels(projectID);
  const refetchOptions = { throwOnError: true };
  const invalidateCatalog = async (): Promise<void> => {
    await queryClient.invalidateQueries(
      {
        queryKey: catalogKey,
        exact: true,
        refetchType: "active",
      },
      refetchOptions,
    );
  };
  const invalidateReorderMembership = async (): Promise<void> => {
    await Promise.all([
      queryClient.invalidateQueries(
        {
          queryKey: queryKeys.projectBoardNodeCardsRoot(projectID),
          refetchType: "active",
        },
        refetchOptions,
      ),
      queryClient.invalidateQueries(
        {
          queryKey: queryKeys.projectTaskListsRoot(projectID),
          refetchType: "active",
        },
        refetchOptions,
      ),
    ]);
  };
  const invalidateReorder = async (): Promise<void> => {
    await Promise.all([invalidateCatalog(), invalidateReorderMembership()]);
  };
  const invalidateMembership = async (): Promise<void> => {
    await Promise.all([
      invalidateProjectBoardQueries(queryClient, projectID),
      queryClient.invalidateQueries(
        {
          queryKey: queryKeys.projectTaskListsRoot(projectID),
          refetchType: "active",
        },
        refetchOptions,
      ),
    ]);
  };
  const invalidateTaskAssignment = async (taskID: string): Promise<void> => {
    await Promise.all([
      queryClient.invalidateQueries(
        {
          queryKey: queryKeys.taskLabels(taskID),
          exact: true,
          refetchType: "active",
        },
        refetchOptions,
      ),
      queryClient.invalidateQueries(
        {
          queryKey: queryKeys.task(taskID),
          exact: true,
          refetchType: "active",
        },
        refetchOptions,
      ),
      invalidateMembership(),
    ]);
  };
  const invalidateDeletedLabel = async (): Promise<void> => {
    await Promise.all([
      invalidateCatalog(),
      queryClient.invalidateQueries(
        { queryKey: queryKeys.allTaskLabels, refetchType: "active" },
        refetchOptions,
      ),
      queryClient.invalidateQueries({ queryKey: queryKeys.allTasks, refetchType: "active" }, refetchOptions),
      invalidateMembership(),
    ]);
  };
  const run = (operation: Promise<void>): void => {
    void operation.catch(onBackgroundError);
  };
  const pruneDeletedLabel = (labelID: string): void => {
    pruneDeletedLabelFromExistingCaches(queryClient, projectID, labelID);
    onFilterAction?.({ type: "label.deleted", labelID });
  };
  return {
    async consumeProjectEvent(event) {
      if (event.projectID !== projectID) {
        return;
      }
      if (event.resource === "label") {
        if (event.action === "deleted") {
          pruneDeletedLabel(event.primaryEntityID);
          await invalidateDeletedLabel();
          return;
        }
        if (event.action === "reordered") {
          await invalidateReorder();
        } else {
          await invalidateCatalog();
        }
        return;
      }
      if (event.resource !== "task") {
        return;
      }
      const taskID = event.primaryEntityID;
      if (event.action === "deleted") {
        removeDeletedTaskFromExistingCaches(queryClient, taskID);
        await invalidateMembership();
        return;
      }
      if (event.action !== "labels_changed") {
        return;
      }
      await invalidateTaskAssignment(taskID);
    },
    async refreshAfterSubscriptionBoundary() {
      await Promise.all([
        invalidateCatalog(),
        queryClient.invalidateQueries(
          { queryKey: queryKeys.allTaskLabels, refetchType: "active" },
          refetchOptions,
        ),
        invalidateMembership(),
      ]);
    },
    scheduleCatalogRefresh() {
      run(invalidateCatalog());
    },
    scheduleDeleteRefresh() {
      run(invalidateDeletedLabel());
    },
    scheduleMembershipRefresh() {
      run(invalidateMembership());
    },
    scheduleReorderRefresh() {
      run(invalidateReorder());
    },
    scheduleTaskAssignmentRefresh(taskID) {
      run(invalidateTaskAssignment(taskID));
    },
  };
}
