import type { NativeBridge, NativeWorkflowDeleted } from "@app/native-bridge";
import type { QueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

import { errorMessage } from "@/api";
import { queryKeys } from "@/app-facade";
import type { SidebarController } from "@/app-facade";
import { useAppServices } from "@/app-facade";

export function useWorkflowDeletedEvents(
  nativeBridge: NativeBridge,
  handler: (event: NativeWorkflowDeleted) => void,
) {
  const { logger } = useAppServices();
  useEffect(() => {
    let active = true;
    let unlisten: (() => void) | null = null;
    void nativeBridge.workflowDeletion
      .onDeleted((event) => {
        if (active) {
          handler(event);
        }
      })
      .then((nextUnlisten) => {
        if (active) {
          unlisten = nextUnlisten;
          return;
        }
        nextUnlisten();
      })
      .catch((error: unknown) => {
        void logger.append("warn", "Workflow deletion event listener failed.", {
          error: errorMessage(error),
        });
      });
    return () => {
      active = false;
      unlisten?.();
    };
  }, [handler, logger, nativeBridge.workflowDeletion]);
}

export async function completeWorkflowDeletion({
  closeSidebar,
  navigateWorkflowLibrary,
  pushDeletedToast,
  queryClient,
  workflowID,
}: Readonly<{
  closeSidebar: SidebarController["closeSidebar"];
  navigateWorkflowLibrary: () => Promise<void>;
  pushDeletedToast: () => void;
  queryClient: QueryClient;
  workflowID: string;
}>): Promise<void> {
  await invalidateWorkflowDeleteQueries(queryClient, workflowID);
  closeSidebar("closed");
  await navigateWorkflowLibrary();
  pushDeletedToast();
}

export async function invalidateWorkflowDeleteQueries(
  queryClient: QueryClient,
  workflowID: string,
): Promise<void> {
  queryClient.removeQueries({ queryKey: queryKeys.workflowDefinition(workflowID) });
  queryClient.removeQueries({ queryKey: queryKeys.workflowValidation(workflowID, "execution") });
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: queryKeys.allWorkflows }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allWorkflowDefinitions }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allWorkflowValidations }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allWorkflowGraphLayouts }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allProjectWorkflowLinks }),
    queryClient.invalidateQueries({ queryKey: queryKeys.projects }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allBoards }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allAttention }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allTasks }),
  ]);
}
