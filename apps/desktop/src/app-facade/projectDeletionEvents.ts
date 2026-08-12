import type { NativeBridge, NativeProjectDeleted } from "@app/native-bridge";
import type { QueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

import { errorMessage } from "@/api";
import { clearLastProjectRoute } from "./projectRoutePersistence";
import { queryKeys } from "./queryKeys";
import { removeProjectTaskSearches } from "./taskSearchQueries";
import { useAppServices } from "./useAppServices";

export function useProjectDeletedEvents(
  nativeBridge: NativeBridge,
  handler: (event: NativeProjectDeleted) => void,
) {
  const { logger } = useAppServices();
  useEffect(() => {
    let active = true;
    let unlisten: (() => void) | null = null;
    void nativeBridge.projectDeletion
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
        void logger.append("warn", "Project deletion event listener failed.", {
          error: errorMessage(error),
        });
      });
    return () => {
      active = false;
      unlisten?.();
    };
  }, [handler, logger, nativeBridge.projectDeletion]);
}

export async function completeProjectDeletion({
  navigateHome,
  projectID,
  pushDeletedToast,
  queryClient,
}: Readonly<{
  navigateHome?: (() => Promise<void>) | undefined;
  projectID: string;
  pushDeletedToast: () => void;
  queryClient: QueryClient;
}>): Promise<void> {
  await invalidateProjectDeleteQueries(queryClient, projectID);
  clearLastProjectRoute(projectID);
  if (navigateHome !== undefined) await navigateHome();
  pushDeletedToast();
}

export async function invalidateProjectDeleteQueries(
  queryClient: QueryClient,
  projectID: string,
): Promise<void> {
  await removeProjectTaskSearches(queryClient, projectID);
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: queryKeys.projects }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allProjectEdits }),
    queryClient.invalidateQueries({ queryKey: queryKeys.projectCatalog(projectID) }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allBoards }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allAttention }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allTasks }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allProjectWorkflowLinks, refetchType: "active" }),
  ]);
}
