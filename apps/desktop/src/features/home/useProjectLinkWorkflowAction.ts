import { useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";

import { queryKeys, useOwnedSidebarRoots, type SidebarMode } from "@/app-facade";

export function useProjectLinkWorkflowAction(projectID: string, sidebarMode: SidebarMode) {
  const queryClient = useQueryClient();
  const { open } = useOwnedSidebarRoots();
  return useCallback(() => {
    open({
      kind: "linkWorkflow",
      mode: sidebarMode,
      onCompleted: async () => {
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
          queryClient.invalidateQueries({
            queryKey: queryKeys.projectTaskListsRoot(projectID),
            refetchType: "active",
          }),
        ]);
      },
      projectID,
    });
  }, [open, projectID, queryClient, sidebarMode]);
}
