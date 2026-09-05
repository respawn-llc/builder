import { createRoute, createRouter, createRootRoute } from "@tanstack/react-router";
import { z } from "zod";

import { workflowIDSchema } from "@/api";
import { sessionChatRoutePath } from "@/app-facade";
import { desktopChatEnabled } from "@/shared/feature-flags";
import { createNativeDialogRoutes, workspaceUnlinkNativeDialogPath } from "./nativeDialogRoutes";
import {
  HomeShellRoute,
  ChatRoute,
  ProjectRoute,
  ProjectTasksRoute,
  RootRoute,
  TaskRoute,
  WorkflowEditorShellRoute,
  WorkflowLibraryShellRoute,
} from "./routeComponents";

const optionalSearchString = z.string().catch("");
const optionalWorkflowSelector = workflowIDSchema.optional();

const projectSearchSchema = z.object({
  workflowId: optionalWorkflowSelector,
  taskId: optionalSearchString,
});

const workflowEditorSearchSchema = z.object({
  projectId: optionalSearchString,
});

const homeSearchSchema = z.object({
  projectId: z.string().min(1).optional(),
});

const rootRoute = createRootRoute({ component: RootRoute });

const homeRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  validateSearch: (search: Record<string, unknown>) => homeSearchSchema.parse(search),
  component: HomeShellRoute,
});

const projectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/projects/$projectId",
  validateSearch: (search: Record<string, unknown>) => projectSearchSchema.parse(search),
  component: ProjectRoute,
});

const projectTasksRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/projects/$projectId/tasks",
  component: ProjectTasksRoute,
});

const workflowLibraryRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/workflows",
  component: WorkflowLibraryShellRoute,
});

const workflowEditorRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/workflows/$workflowId/editor",
  parseParams: (params) => ({ workflowId: workflowIDSchema.parse(params.workflowId) }),
  validateSearch: (search: Record<string, unknown>) => workflowEditorSearchSchema.parse(search),
  component: WorkflowEditorShellRoute,
});

const taskRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/tasks/$taskId",
  component: TaskRoute,
});

const chatRoute = desktopChatEnabled
  ? createRoute({
      getParentRoute: () => rootRoute,
      path: sessionChatRoutePath,
      component: ChatRoute,
    })
  : undefined;

const nativeDialogRoutes = createNativeDialogRoutes(rootRoute);

const routeTree = rootRoute.addChildren([
  homeRoute,
  projectTasksRoute,
  projectRoute,
  workflowLibraryRoute,
  workflowEditorRoute,
  taskRoute,
  ...(chatRoute === undefined ? [] : [chatRoute]),
  ...nativeDialogRoutes,
]);

export function createAppRouter() {
  return createRouter({ routeTree });
}

export type AppRouter = ReturnType<typeof createAppRouter>;

export function shouldSkipNativeDialogStartupGate(pathname: string): boolean {
  return pathname === workspaceUnlinkNativeDialogPath;
}

declare module "@tanstack/react-router" {
  interface Register {
    router: AppRouter;
  }
}
