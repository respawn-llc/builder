import type { QueryClient } from "@tanstack/react-query";

import type { ProjectLabel, ProjectLabelCatalog, WorkflowProjectEvent } from "@/api";
import { invalidateProjectBoardQueries, queryKeys } from "@/app-facade";
import type { LabelFilterAction } from "./labelFilterState";
import type { ProjectCatalogAuthority } from "./projectCatalogAuthority";
import { taskLabelAssignmentRegistryFor } from "./taskLabelAssignmentRegistry";
import { pruneDeletedLabelFromExistingCaches, removeDeletedTaskFromExistingCaches } from "./taskLabelCache";

export type ProjectLabelEffects = Readonly<{
  applyLocalCreate(label: ProjectLabel): Promise<void>;
  applyLocalReorder(catalog: ProjectLabelCatalog, generation: number): Promise<void>;
  applyLocalRename(label: ProjectLabel): Promise<void>;
  applyLocalDelete(labelID: string): Promise<void>;
  consumeProjectEvent(event: WorkflowProjectEvent): Promise<void>;
  refreshAfterSubscriptionBoundary(): Promise<void>;
}>;

export function createProjectLabelEffects({
  authority,
  onFilterAction,
  onBackgroundError,
  projectID,
  queryClient,
}: Readonly<{
  authority: ProjectCatalogAuthority;
  onFilterAction?: ((action: LabelFilterAction) => void) | undefined;
  onBackgroundError: (error: unknown) => void;
  projectID: string;
  queryClient: QueryClient;
}>): ProjectLabelEffects {
  const registry = taskLabelAssignmentRegistryFor(queryClient);
  const refreshBoardCards = async (): Promise<void> => {
    await queryClient.invalidateQueries({
      queryKey: queryKeys.projectBoardNodeCardsRoot(projectID),
      refetchType: "active",
    });
  };
  const refreshMembership = async (): Promise<void> => {
    await Promise.all([
      invalidateProjectBoardQueries(queryClient, projectID),
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
    await refreshMembership();
  };
  return {
    async applyLocalCreate(label) {
      authority.applyCreate(label);
    },
    async applyLocalReorder(catalog, generation) {
      authority.installCatalog(catalog, generation);
      void refreshBoardCards().catch(onBackgroundError);
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
        if (event.action === "reordered") {
          await refreshBoardCards();
        }
        return;
      }
      if (event.resource !== "task" || event.workflowID === null) {
        return;
      }
      const taskID = event.primaryEntityID;
      if (event.action === "deleted") {
        registry.deleteTask(taskID);
        removeDeletedTaskFromExistingCaches(queryClient, taskID);
        await refreshMembership();
        return;
      }
      if (event.action !== "labels_changed") {
        return;
      }
      await refreshMembership();
    },
    async refreshAfterSubscriptionBoundary() {
      authority.requestRefresh();
      await refreshMembership();
    },
  };
}
