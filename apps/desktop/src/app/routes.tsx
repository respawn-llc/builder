import { createRoute, createRouter, createRootRoute } from "@tanstack/react-router";
import { z } from "zod";

import { workflowIDSchema } from "@/api";
import { createNativeDialogRoutes, workspaceUnlinkNativeDialogPath } from "./nativeDialogRoutes";
import {
  HomeShellRoute,
  ProjectRoute,
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

const rootRoute = createRootRoute({ component: RootRoute });

const homeRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: HomeShellRoute,
});

const projectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/projects/$projectId",
  validateSearch: (search: Record<string, unknown>) => projectSearchSchema.parse(search),
  component: ProjectRoute,
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

const nativeDialogRoutes = createNativeDialogRoutes(rootRoute);

const routeTree = rootRoute.addChildren([
  homeRoute,
  projectRoute,
  workflowLibraryRoute,
  workflowEditorRoute,
  taskRoute,
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
