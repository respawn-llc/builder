import type {
  SidebarDestination,
  SidebarInvalidationTarget,
  TaskDetailInitialFocus,
} from "@/app-facade";

export type { SidebarInvalidationTarget };

export function sameSidebarDestination(
  current: SidebarDestination,
  requested: SidebarDestination,
): boolean {
  return (
    current.kind === "taskDetail" &&
    requested.kind === "taskDetail" &&
    current.taskID === requested.taskID &&
    current.projectID === requested.projectID
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
  const { initialFocus, ...withoutFocus } = destination;
  void initialFocus;
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
