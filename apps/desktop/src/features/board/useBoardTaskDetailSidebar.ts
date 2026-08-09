import { useEffect, useRef } from "react";
import type { AppNavigation, SidebarRootController, SidebarRootHandle } from "@/app-facade";

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
  const lastTaskIDRef = useRef<string | null>(null);
  useEffect(() => {
    if (selectedTaskID.length === 0) {
      rootRef.current?.release();
      rootRef.current = null;
      lastTaskIDRef.current = null;
      return;
    }
    const destination = {
      kind: "taskDetail" as const,
      mode: "overlay" as const,
      onMutated: undefined,
      taskID: selectedTaskID,
    };
    const openRoot = () => {
      const root = openSidebar(destination, () => void navigation.back().catch(onNavigationError));
      rootRef.current = root;
      lastTaskIDRef.current = selectedTaskID;
      void root.lifecycle.then((outcome) => {
        if (rootRef.current !== root) return;
        rootRef.current = null;
        lastTaskIDRef.current = null;
        if (outcome === "closed") {
          void navigation.closeProjectTask(projectID, workflowID).catch(onNavigationError);
        }
      });
    };
    const root = rootRef.current;
    if (root === null) {
      openRoot();
      return;
    }
    if (lastTaskIDRef.current === selectedTaskID) return;
    if (root.push(destination) !== "accepted") {
      root.release();
      openRoot();
      return;
    }
    lastTaskIDRef.current = selectedTaskID;
  }, [navigation, onNavigationError, openSidebar, projectID, selectedTaskID, workflowID]);

  useEffect(() => () => rootRef.current?.release(), []);
}
