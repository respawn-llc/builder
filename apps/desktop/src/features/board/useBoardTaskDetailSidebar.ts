import { useCallback, useEffect, useRef } from "react";

import type {
  AppNavigation,
  SidebarDestination,
  SidebarRootController,
  SidebarRootHandle,
} from "@/app-facade";

type BoardTaskDetailDestination = Extract<SidebarDestination, { kind: "taskDetail" }>;

export function useBoardTaskDetailSidebar({
  navigation,
  onNavigationError,
  openSidebar,
  projectID,
  selectedTaskID,
  workflowID,
}: Readonly<{
  navigation: AppNavigation;
  onNavigationError(error: unknown): void;
  openSidebar: SidebarRootController["open"];
  projectID: string;
  selectedTaskID: string;
  workflowID: string;
}>): void {
  const rootRef = useRef<SidebarRootHandle | null>(null);
  const lastPageTaskIDRef = useRef<string | null>(null);
  const registerRoot = useCallback(
    (root: SidebarRootHandle, taskID: string) => {
      rootRef.current = root;
      lastPageTaskIDRef.current = taskID;
      void root.lifecycle.then((outcome) => {
        if (rootRef.current !== root) {
          return;
        }
        rootRef.current = null;
        lastPageTaskIDRef.current = null;
        if (outcome === "closed") {
          void navigation.closeProjectTask(projectID, workflowID).catch(onNavigationError);
        }
      });
    },
    [navigation, onNavigationError, projectID, workflowID],
  );

  useEffect(() => {
    if (selectedTaskID.length === 0) {
      rootRef.current?.release();
      rootRef.current = null;
      lastPageTaskIDRef.current = null;
      return;
    }

    const destination = boardTaskDetailDestination(selectedTaskID);
    const root = rootRef.current;
    if (root === null) {
      registerRoot(openSidebar(destination), selectedTaskID);
      return;
    }
    if (lastPageTaskIDRef.current === selectedTaskID) {
      return;
    }

    const outcome = root.push(destination);
    if (outcome !== "accepted") {
      root.release();
      registerRoot(openSidebar(destination), selectedTaskID);
      return;
    }
    lastPageTaskIDRef.current = selectedTaskID;
  }, [openSidebar, registerRoot, selectedTaskID]);

  useEffect(
    () => () => {
      const root = rootRef.current;
      rootRef.current = null;
      root?.release();
    },
    [],
  );
}

function boardTaskDetailDestination(taskID: string): BoardTaskDetailDestination {
  return {
    kind: "taskDetail",
    mode: "overlay",
    onMutated: undefined,
    taskID,
  };
}
