import type { QueryClient } from "@tanstack/react-query";

import type { ProjectLabel, WorkflowProjectEvent } from "@/api";
import { queryKeys } from "@/app-facade";
import type { LabelFilterAction } from "./labelFilterState";
import type { ProjectCatalogAuthority } from "./projectCatalogAuthority";
import { taskLabelAssignmentRegistryFor } from "./taskLabelAssignmentRegistry";
import { pruneDeletedLabelFromExistingCaches, removeDeletedTaskFromExistingCaches } from "./taskLabelCache";

export type LabelMembershipRefreshEffect =
  | Readonly<{
      kind: "catalog.deleted";
      labelID: string;
      projectID: string;
    }>
  | Readonly<{
      kind: "task.labels_changed";
      projectID: string;
      taskID: string;
      workflowID: string;
    }>
  | Readonly<{
      kind: "task.deleted";
      projectID: string;
      taskID: string;
      workflowID: string;
    }>
  | Readonly<{
      kind: "subscription.refresh";
      projectID: string;
    }>;

export type ProjectLabelEffects = Readonly<{
  applyLocalCreate(label: ProjectLabel): Promise<void>;
  applyLocalRename(label: ProjectLabel): Promise<void>;
  applyLocalDelete(labelID: string): Promise<void>;
  consumeProjectEvent(event: WorkflowProjectEvent): Promise<void>;
  refreshAfterSubscriptionBoundary(): Promise<void>;
}>;

export function createProjectLabelEffects({
  authority,
  onFilterAction,
  onMembershipRefresh,
  projectID,
  queryClient,
}: Readonly<{
  authority: ProjectCatalogAuthority;
  onFilterAction?: ((action: LabelFilterAction) => void) | undefined;
  onMembershipRefresh?: ((effect: LabelMembershipRefreshEffect) => Promise<void> | void) | undefined;
  projectID: string;
  queryClient: QueryClient;
}>): ProjectLabelEffects {
  const registry = taskLabelAssignmentRegistryFor(queryClient);
  const refreshMembership = async (effect: LabelMembershipRefreshEffect): Promise<void> => {
    await onMembershipRefresh?.(effect);
    const workflowID =
      effect.kind === "task.labels_changed" || effect.kind === "task.deleted"
        ? effect.workflowID
        : undefined;
    await Promise.all([
      queryClient.invalidateQueries({
        queryKey:
          workflowID === undefined
            ? queryKeys.projectBoardsRoot(projectID)
            : queryKeys.boardWorkflowRoot(projectID, workflowID),
        refetchType: "active",
      }),
      queryClient.invalidateQueries({
        queryKey:
          workflowID === undefined
            ? queryKeys.projectBoardNodeCardsRoot(projectID)
            : queryKeys.boardNodeCardsWorkflowRoot(projectID, workflowID),
        refetchType: "active",
      }),
      queryClient.invalidateQueries({
        queryKey: queryKeys.projectTaskListsRoot(projectID),
        refetchType: "active",
      }),
    ]);
  };
  const deleteLabel = async (labelID: string): Promise<void> => {
    authority.applyDelete(labelID);
    registry.deleteLabel(projectID, labelID);
    pruneDeletedLabelFromExistingCaches(queryClient, labelID);
    onFilterAction?.({ type: "label.deleted", labelID });
    await refreshMembership({
      kind: "catalog.deleted",
      labelID,
      projectID,
    });
  };
  return {
    async applyLocalCreate(label) {
      authority.applyCreate(label);
    },
    async applyLocalRename(label) {
      authority.applyRename(label);
    },
    async applyLocalDelete(labelID) {
      await deleteLabel(labelID);
    },
    async consumeProjectEvent(event) {
      if (event.projectID !== projectID) {
        return;
      }
      if (event.resource === "label") {
        if (event.action === "deleted") {
          await deleteLabel(event.primaryEntityID);
          return;
        }
        authority.requestRefresh();
        return;
      }
      if (event.resource === "label_catalog") {
        authority.requestRefresh();
        await refreshMembership({
          kind: "subscription.refresh",
          projectID,
        });
        return;
      }
      if (event.resource !== "task" || event.workflowID === null) {
        return;
      }
      const taskID = event.primaryEntityID;
      if (event.action === "deleted") {
        registry.deleteTask(taskID);
        removeDeletedTaskFromExistingCaches(queryClient, taskID);
        await refreshMembership({
          kind: "task.deleted",
          projectID,
          taskID,
          workflowID: event.workflowID,
        });
        return;
      }
      if (event.action !== "labels_changed") {
        return;
      }
      await refreshMembership({
        kind: "task.labels_changed",
        projectID,
        taskID,
        workflowID: event.workflowID,
      });
    },
    async refreshAfterSubscriptionBoundary() {
      authority.requestRefresh();
      await refreshMembership({
        kind: "subscription.refresh",
        projectID,
      });
    },
  };
}
