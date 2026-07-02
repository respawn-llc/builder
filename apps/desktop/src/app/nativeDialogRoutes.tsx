import { createRoute, type AnyRootRoute } from "@tanstack/react-router";
import { z } from "zod";

import { TaskDeleteWindowRoute } from "../features/board/TaskDeleteConfirmation";
import { taskDeleteNativeDialogPath } from "../features/board/taskDeleteConfirmationModel";
import { ProjectCreateWindowRoute } from "../features/home/ProjectCreateForm";
import { ProjectDeleteWindowRoute } from "../features/project-edit/ProjectDeleteButton";
import { WorkspaceUnlinkWindowRoute } from "../features/project-edit/ProjectEditParts";
import { TaskDetailWindowRoute } from "../features/task-detail/TaskDetailWindowRoute";
import { NewTaskWindowRoute } from "../features/tasks/NewTaskDialog";
import { InvalidNativeDialogRoute } from "./InvalidNativeDialogRoute";
import { taskDetailNativeDialogPath } from "./sidebarPopOut";
import { useWindowChromeTitle } from "./windowChromeTitle";

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

const newTaskSearchSchema = z.object({
  projectID: optionalSearchString,
  workflowID: optionalSearchString,
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

  const newTaskWindowRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/native-dialog/new-task",
    validateSearch: (search: Record<string, unknown>) => newTaskSearchSchema.parse(search),
    component: NewTaskNativeRoute,
  });

  function NewTaskNativeRoute() {
    const search = newTaskSearchSchema.parse(newTaskWindowRoute.useSearch());
    return <NewTaskWindowRoute projectID={search.projectID} workflowID={search.workflowID} />;
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
    newTaskWindowRoute,
    workspaceUnlinkWindowRoute,
  ] as const;
}
