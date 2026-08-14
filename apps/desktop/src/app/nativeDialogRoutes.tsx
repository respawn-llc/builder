import { createRoute, type AnyRootRoute } from "@tanstack/react-router";
import { z } from "zod";

import { TaskDeleteWindowRoute, taskDeleteNativeDialogPath } from "@/features/board";
import { ProjectCreateWindowRoute } from "@/features/home";
import { ProjectDeleteWindowRoute, WorkspaceUnlinkWindowRoute } from "@/features/project-edit";
import { TaskDetailWindowRoute } from "@/features/task-detail";
import { InvalidNativeDialogRoute } from "./InvalidNativeDialogRoute";
import { taskDetailNativeDialogPath } from "./sidebarPopOut";
import { useWindowChromeTitle } from "@/app-facade";

export const projectDeleteNativeDialogPath = "/native-dialog/project-delete";
export const workspaceUnlinkNativeDialogPath = "/native-dialog/workspace-unlink";
export { taskDeleteNativeDialogPath };

const optionalSearchString = z.string().catch("");

const projectCreateSearchSchema = z.object({
  key: optionalSearchString,
  name: optionalSearchString,
  workspaceRoot: optionalSearchString,
});

const projectDeleteSearchSchema = z.object({
  projectID: optionalSearchString,
});

const taskDeleteSearchSchema = z.object({
  taskID: optionalSearchString,
});

const taskDetailSearchSchema = z.object({
  taskID: optionalSearchString,
});

const workspaceUnlinkSearchSchema = z.object({
  projectID: optionalSearchString,
  rootPath: optionalSearchString,
  workspaceID: optionalSearchString,
});

export function createNativeDialogRoutes(rootRoute: AnyRootRoute) {
  const projectCreateRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/native-dialog/project-create",
    validateSearch: (search: Record<string, unknown>) => projectCreateSearchSchema.parse(search),
    component: ProjectCreateNativeRoute,
  });

  function ProjectCreateNativeRoute() {
    const search = projectCreateSearchSchema.parse(projectCreateRoute.useSearch());
    return <ProjectCreateWindowRoute draft={search} />;
  }

  const projectDeleteRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: projectDeleteNativeDialogPath,
    validateSearch: (search: Record<string, unknown>) => projectDeleteSearchSchema.parse(search),
    component: ProjectDeleteNativeRoute,
  });

  function ProjectDeleteNativeRoute() {
    const search = projectDeleteSearchSchema.parse(projectDeleteRoute.useSearch());
    const projectID = search.projectID.trim();
    useWindowChromeTitle(null);
    if (projectID.length === 0) {
      return <InvalidNativeDialogRoute />;
    }
    return <ProjectDeleteWindowRoute projectID={projectID} />;
  }

  const taskDeleteWindowRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: taskDeleteNativeDialogPath,
    validateSearch: (search: Record<string, unknown>) => taskDeleteSearchSchema.parse(search),
    component: TaskDeleteNativeRoute,
  });

  function TaskDeleteNativeRoute() {
    const search = taskDeleteSearchSchema.parse(taskDeleteWindowRoute.useSearch());
    const taskID = search.taskID.trim();
    useWindowChromeTitle(null);
    if (taskID.length === 0) {
      return <InvalidNativeDialogRoute />;
    }
    return <TaskDeleteWindowRoute taskID={taskID} />;
  }

  const taskDetailWindowRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: taskDetailNativeDialogPath,
    validateSearch: (search: Record<string, unknown>) => taskDetailSearchSchema.parse(search),
    component: TaskDetailNativeRoute,
  });

  function TaskDetailNativeRoute() {
    const search = taskDetailSearchSchema.parse(taskDetailWindowRoute.useSearch());
    const taskID = search.taskID.trim();
    if (taskID.length === 0) {
      return <InvalidNativeDialogRoute />;
    }
    return <TaskDetailWindowRoute taskID={taskID} />;
  }

  const workspaceUnlinkWindowRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: workspaceUnlinkNativeDialogPath,
    validateSearch: (search: Record<string, unknown>) => workspaceUnlinkSearchSchema.parse(search),
    component: WorkspaceUnlinkNativeRoute,
  });

  function WorkspaceUnlinkNativeRoute() {
    const search = workspaceUnlinkSearchSchema.parse(workspaceUnlinkWindowRoute.useSearch());
    return (
      <WorkspaceUnlinkWindowRoute
        projectID={search.projectID}
        rootPath={search.rootPath}
        workspaceID={search.workspaceID}
      />
    );
  }

  return [
    projectCreateRoute,
    projectDeleteRoute,
    taskDeleteWindowRoute,
    taskDetailWindowRoute,
    workspaceUnlinkWindowRoute,
  ] as const;
}
