export type SidebarRouteKind = "board" | "workflowEditor" | "other";
type SimpleTransition = "none" | "pathname" | "boardWorkflow" | "workflowEditorProject";
type SidebarRouteSearch = Readonly<Partial<Record<"projectId" | "taskId" | "workflowId", string>>>;

export type SidebarRouteLocation = Readonly<{
  pathname: string;
  routeKind: SidebarRouteKind;
  projectID: string | undefined;
  taskID: string | undefined;
  workflowID: string | undefined;
}>;

export type SidebarRouteTransition =
  | Readonly<{ kind: "none" | "pathname" | "boardWorkflow" | "workflowEditorProject" }>
  | Readonly<{ kind: "boardTask"; from: string | undefined; to: string | undefined }>;

export type SidebarRouteMatch = Readonly<{ routeId: string; search: SidebarRouteSearch }>;

export function sidebarRouteLocationFromMatches(
  pathname: string,
  matches: readonly SidebarRouteMatch[],
): SidebarRouteLocation {
  const searchFor = (routeId: string) => matches.find((match) => match.routeId === routeId)?.search;
  const boardSearch = searchFor("/projects/$projectId");
  const workflowEditorSearch = searchFor("/workflows/$workflowId/editor");
  return {
    pathname,
    projectID: workflowEditorSearch?.projectId,
    routeKind:
      boardSearch === undefined ? (workflowEditorSearch === undefined ? "other" : "workflowEditor") : "board",
    taskID: boardSearch?.taskId,
    workflowID: boardSearch?.workflowId,
  };
}

export function classifySidebarRouteTransition(
  previous: SidebarRouteLocation,
  next: SidebarRouteLocation,
): SidebarRouteTransition {
  if (previous.pathname !== next.pathname) return { kind: "pathname" };
  if (previous.routeKind === "board" && next.routeKind === "board") {
    if (previous.workflowID !== next.workflowID) return { kind: "boardWorkflow" };
    if (previous.taskID !== next.taskID) {
      return { from: previous.taskID, kind: "boardTask", to: next.taskID };
    }
  }
  if (
    previous.routeKind === "workflowEditor" &&
    next.routeKind === "workflowEditor" &&
    previous.projectID !== next.projectID
  ) {
    return { kind: "workflowEditorProject" };
  }
  return { kind: "none" };
}
