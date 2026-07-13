import { useNavigate, useRouter, useRouterState } from "@tanstack/react-router";
import { useCallback, useEffect, useMemo, useState } from "react";

import { errorMessage } from "../api/errors";
import { runNavigationTransition } from "./navigationTransitions";
import { useAppServices } from "./useAppServices";

type NavigationStackAction = "PUSH" | "REPLACE" | "FORWARD" | "BACK" | "GO";

export type AppNavigation = Readonly<{
  back(): Promise<void>;
  forward(): Promise<void>;
  openHome(): Promise<void>;
  openProject(projectID: string, workflowID?: string): Promise<void>;
  openWorkflowEditor(input: Readonly<{ workflowID: string; projectID?: string | undefined }>): Promise<void>;
  openWorkflowLibrary(): Promise<void>;
  openTask(taskID: string): Promise<void>;
  openProjectTask(projectID: string, workflowID: string, taskID: string): Promise<void>;
  closeProjectTask(projectID: string, workflowID?: string): Promise<void>;
}>;

export type NavigationStackState = Readonly<{
  canGoBack: boolean;
  canGoForward: boolean;
  hasHistory: boolean;
}>;

export function useAppNavigation(): AppNavigation {
  const navigate = useNavigate();
  const router = useRouter();
  const { logger } = useAppServices();
  const runNavigation = useCallback(
    async (action: () => Promise<void>): Promise<void> => {
      try {
        await runNavigationTransition(action);
      } catch (error) {
        await logger.append("warn", "Navigation failed", { error: errorMessage(error) });
      }
    },
    [logger],
  );
  const runImmediateNavigation = useCallback(
    async (action: () => Promise<void>): Promise<void> => {
      try {
        await action();
      } catch (error) {
        await logger.append("warn", "Navigation failed", { error: errorMessage(error) });
      }
    },
    [logger],
  );
  return useMemo(
    () => ({
      async back() {
        await runNavigation(async () => {
          router.history.back();
        });
      },
      async forward() {
        await runNavigation(async () => {
          router.history.forward();
        });
      },
      async openHome() {
        await runNavigation(async () => {
          await navigate({ to: "/" });
        });
      },
      async openProject(projectID, workflowID) {
        await runNavigation(async () => {
          await navigate({
            to: "/projects/$projectId",
            params: { projectId: projectID },
            search: { workflowId: workflowID, taskId: "" },
          });
        });
      },
      async openWorkflowEditor(input) {
        await runNavigation(async () => {
          await navigate({
            to: "/workflows/$workflowId/editor",
            params: { workflowId: input.workflowID },
            search: { projectId: input.projectID ?? "" },
          });
        });
      },
      async openWorkflowLibrary() {
        await runNavigation(async () => {
          await navigate({ to: "/workflows" });
        });
      },
      async openTask(taskID) {
        await runNavigation(async () => {
          await navigate({ to: "/tasks/$taskId", params: { taskId: taskID } });
        });
      },
      async openProjectTask(projectID, workflowID, taskID) {
        await runImmediateNavigation(async () => {
          await navigate({
            to: "/projects/$projectId",
            params: { projectId: projectID },
            search: { workflowId: workflowID, taskId: taskID },
          });
        });
      },
      async closeProjectTask(projectID, workflowID) {
        await runImmediateNavigation(async () => {
          await navigate({
            to: "/projects/$projectId",
            params: { projectId: projectID },
            search: { workflowId: workflowID, taskId: "" },
          });
        });
      },
    }),
    [navigate, router.history, runImmediateNavigation, runNavigation],
  );
}

export function useNavigationStackState(): NavigationStackState {
  const router = useRouter();
  const currentIndex = useRouterState({
    select: (state) => state.location.state.__TSR_index,
  });
  const [maxReachableIndex, setMaxReachableIndex] = useState(() => currentIndex);

  useEffect(() => {
    return router.history.subscribe(({ action, location }) => {
      const nextIndex = location.state.__TSR_index;
      setMaxReachableIndex((currentMax) => nextReachableHistoryIndex(currentMax, action.type, nextIndex));
    });
  }, [currentIndex, router.history]);

  const canGoBack = currentIndex > 0;
  const canGoForward = currentIndex < maxReachableIndex;
  return {
    canGoBack,
    canGoForward,
    hasHistory: canGoBack || canGoForward,
  };
}

export function nextReachableHistoryIndex(
  currentMax: number,
  action: NavigationStackAction,
  nextIndex: number,
): number {
  return action === "PUSH" ? nextIndex : Math.max(currentMax, nextIndex);
}
