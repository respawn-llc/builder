import { createRoute, createRouter, createRootRoute } from "@tanstack/react-router";
import { z } from "zod";

import { createNativeDialogRoutes, workspaceUnlinkNativeDialogPath } from "./nativeDialogRoutes";
import {
  HomeShellRoute,
  ProjectRoute,
  RootRoute,
  TaskRoute,
  WorkflowEditorShellRoute,
  WorkflowLibraryShellRoute,
} from "./routeComponents";
import {
  createWorkflowDeleteConfirmWindowRoute,
  workflowDeleteConfirmNativeDialogPath,
} from "./workflowDeleteConfirmRoute";
import { createWorkflowDeleteWindowRoute } from "./workflowDeleteRoute";

const optionalSearchString = z.string().catch("");

const projectSearchSchema = z.object({
  workflowId: optionalSearchString,
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
  validateSearch: (search: Record<string, unknown>) => workflowEditorSearchSchema.parse(search),
  component: WorkflowEditorShellRoute,
});

const taskRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/tasks/$taskId",
  component: TaskRoute,
});

const nativeDialogRoutes = createNativeDialogRoutes(rootRoute);
const workflowDeleteConfirmWindowRoute = createWorkflowDeleteConfirmWindowRoute(rootRoute);
const workflowDeleteWindowRoute = createWorkflowDeleteWindowRoute(rootRoute);

const routeTree = rootRoute.addChildren([
  homeRoute,
  projectRoute,
  workflowLibraryRoute,
  workflowEditorRoute,
  taskRoute,
  ...nativeDialogRoutes,
  workflowDeleteWindowRoute,
  workflowDeleteConfirmWindowRoute,
]);

export function createAppRouter() {
  return createRouter({ routeTree });
}

export type AppRouter = ReturnType<typeof createAppRouter>;

export function shouldSkipNativeDialogStartupGate(pathname: string): boolean {
  return (
    pathname === workspaceUnlinkNativeDialogPath ||
    pathname === workflowDeleteConfirmNativeDialogPath
  );
}

declare module "@tanstack/react-router" {
  interface Register {
    router: AppRouter;
  }
}
