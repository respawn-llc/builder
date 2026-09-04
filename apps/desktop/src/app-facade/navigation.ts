import { useNavigate, useRouter, useRouterState } from "@tanstack/react-router";
import { useCallback, useEffect, useMemo, useState } from "react";

import { errorMessage } from "@/api";
import { runNavigationTransition } from "./navigationTransitions";
import type { SessionChatCatalogOrigin, SessionChatHistoryState } from "./sessionChatHistory";
import { useAppServices } from "./useAppServices";

type NavigationStackAction = "PUSH" | "REPLACE" | "FORWARD" | "BACK" | "GO";

export const sessionChatRoutePath = "/projects/$projectId/sessions/$sessionId" as const;

export type { SessionChatCatalogOrigin, SessionChatHistoryState } from "./sessionChatHistory";

export type SessionChatTarget = Readonly<{
  projectID: string;
  sessionID: string;
  catalogOrigin?: SessionChatCatalogOrigin;
}>;

export type AppNavigation = Readonly<{
  back(): Promise<void>;
  forward(): Promise<void>;
  openHome(): Promise<void>;
  selectHomeProject(projectID: string | null): Promise<void>;
  openProjectTasks(projectID: string): Promise<void>;
  openProject(projectID: string, workflowID?: string): Promise<void>;
  openWorkflowEditor(input: Readonly<{ workflowID: string; projectID?: string | undefined }>): Promise<void>;
  openWorkflowLibrary(): Promise<"completed" | "failed">;
  openTask(taskID: string): Promise<void>;
  replaceTask(taskID: string): Promise<void>;
  openProjectTask(projectID: string, workflowID: string, taskID: string): Promise<void>;
  openSessionChat(target: SessionChatTarget): Promise<void>;
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
  const location = useRouterState({
    select: (state) => ({
      pathname: state.location.pathname,
      searchStr: state.location.searchStr,
    }),
  });
  const { logger } = useAppServices();
  const runNavigation = useCallback(
    async (action: () => Promise<void>): Promise<"completed" | "failed"> => {
      try {
        await runNavigationTransition(action);
        return "completed";
      } catch (error) {
        await logger.append("warn", "Navigation failed", { error: errorMessage(error) });
        return "failed";
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
        if (isHomeLocation(location.pathname, location.searchStr, null)) {
          return;
        }
        if (location.pathname === "/") {
          await runImmediateNavigation(async () => {
            await navigate({ to: "/", search: {} });
          });
          return;
        }
        await runNavigation(async () => {
          await navigate({ to: "/", search: {} });
        });
      },
      async selectHomeProject(projectID) {
        if (isHomeLocation(location.pathname, location.searchStr, projectID)) {
          return;
        }
        await runImmediateNavigation(async () => {
          await navigate({ to: "/", search: projectID === null ? {} : { projectId: projectID } });
        });
      },
      async openProjectTasks(projectID) {
        if (location.pathname === `/projects/${projectID}/tasks`) {
          return;
        }
        await runNavigation(async () => {
          await navigate({ to: "/projects/$projectId/tasks", params: { projectId: projectID } });
        });
      },
      async openProject(projectID, workflowID) {
        if (
          location.pathname === `/projects/${projectID}` &&
          searchValue(location.searchStr, "workflowId") === (workflowID ?? null) &&
          searchValue(location.searchStr, "taskId") === null
        ) {
          return;
        }
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
        return runNavigation(async () => {
          await navigate({ to: "/workflows" });
        });
      },
      async openTask(taskID) {
        await runNavigation(async () => {
          await navigate({ to: "/tasks/$taskId", params: { taskId: taskID } });
        });
      },
      async replaceTask(taskID) {
        await runImmediateNavigation(async () => {
          await navigate({
            to: "/tasks/$taskId",
            params: { taskId: taskID },
            replace: true,
          });
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
      async openSessionChat(target) {
        const sessionChatState: SessionChatHistoryState = {
          sessionChat: {
            catalogOrigin: target.catalogOrigin ?? null,
            projectID: target.projectID,
          },
        };
        await runNavigation(async () => {
          await navigate({
            to: sessionChatRoutePath,
            params: { projectId: target.projectID, sessionId: target.sessionID },
            state: (previous) => ({
              ...previous,
              ...sessionChatState,
            }),
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
    [location.pathname, location.searchStr, navigate, router.history, runImmediateNavigation, runNavigation],
  );
}

function isHomeLocation(pathname: string, searchStr: string, projectID: string | null): boolean {
  return pathname === "/" && searchValue(searchStr, "projectId") === projectID;
}

function searchValue(searchStr: string, key: string): string | null {
  const value = new URLSearchParams(searchStr).get(key);
  return value === null || value.length === 0 ? null : value;
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
