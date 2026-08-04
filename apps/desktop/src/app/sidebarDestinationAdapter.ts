import type {
  SidebarDestination,
  SidebarDestinationSnapshot,
  SidebarInvalidationTarget,
  SidebarTaskDetailSnapshot,
  TaskDetailInitialFocus,
} from "@/app-facade";

export type SidebarTaskIdentity = Readonly<{
  taskID: string;
  projectID: string;
}>;

export type { SidebarInvalidationTarget };

export function sidebarTaskIdentity(destination: SidebarDestination): SidebarTaskIdentity | null {
  return destination.kind === "taskDetail"
    ? { projectID: destination.projectID, taskID: destination.taskID }
    : null;
}

export function sameSidebarDestination(
  current: SidebarDestination,
  requested: SidebarDestination,
): boolean {
  const currentTask = sidebarTaskIdentity(current);
  const requestedTask = sidebarTaskIdentity(requested);
  return (
    currentTask !== null &&
    requestedTask !== null &&
    currentTask.taskID === requestedTask.taskID &&
    currentTask.projectID === requestedTask.projectID
  );
}

export function sidebarDestinationProjectID(destination: SidebarDestination): string | null {
  switch (destination.kind) {
    case "newTask":
    case "linkWorkflow":
    case "projectEdit":
      return destination.projectID;
    case "taskDetail":
    case "workflowEditor":
      return destination.projectID ?? null;
    case "workflowCreate":
      return destination.projectID ?? null;
    case "workflowInspect":
    case "custom":
      return null;
  }
}

export function sidebarDestinationMatches(
  destination: SidebarDestination,
  target: SidebarInvalidationTarget,
): boolean {
  if (target.kind === "task") {
    return destination.kind === "taskDetail" && destination.taskID === target.taskID;
  }
  return sidebarDestinationProjectID(destination) === target.projectID;
}

export function deactivateSidebarDestination(destination: SidebarDestination): SidebarDestination {
  if (destination.kind !== "taskDetail" || destination.initialFocus === undefined) {
    return destination;
  }
  const { initialFocus: _initialFocus, ...withoutFocus } = destination;
  return withoutFocus;
}

export function taskDetailSidebarDestination(
  taskID: string,
  projectID: string,
  options: Readonly<{
    initialFocus?: TaskDetailInitialFocus | undefined;
    inboxNav?: boolean | undefined;
    mode?: SidebarDestination["mode"];
    onMutated?: (() => void) | undefined;
  }> = {},
): Extract<SidebarDestination, { kind: "taskDetail" }> {
  return {
    kind: "taskDetail",
    projectID,
    taskID,
    ...(options.initialFocus === undefined ? {} : { initialFocus: options.initialFocus }),
    ...(options.inboxNav === undefined ? {} : { inboxNav: options.inboxNav }),
    ...(options.mode === undefined ? {} : { mode: options.mode }),
    ...(options.onMutated === undefined ? {} : { onMutated: options.onMutated }),
  };
}

export function narrowTaskDetailSnapshot(
  snapshot: SidebarDestinationSnapshot | null,
): SidebarTaskDetailSnapshot | null {
  return snapshot?.kind === "taskDetail" ? snapshot : null;
}
