type SidebarRouteSearch = Partial<Record<"projectId" | "taskId" | "workflowId", string | undefined>>;
export type SidebarRouteLocation = Readonly<{
  pathname: string;
  routeKind: "board" | "workflowEditor" | "other";
  projectID: string | undefined;
  taskID: string | undefined;
  workflowID: string | undefined;
}>;
export type SidebarRouteTransition = Readonly<{ kind: "none" | "pathname" | "boardWorkflow" | "workflowEditorProject" }> | Readonly<{ kind: "boardTask"; from: string | undefined; to: string | undefined }>;
export type SidebarRouteMatch = Readonly<{ routeId: string }>;
export function sidebarRouteSearchFromQuery(search: string): SidebarRouteSearch {
  const query = new URLSearchParams(search);
  return { projectId: query.get("projectId") ?? undefined, taskId: query.get("taskId") ?? undefined, workflowId: query.get("workflowId") ?? undefined };
}
export function sidebarRouteLocationFromMatches(pathname: string, matches: readonly SidebarRouteMatch[], search: SidebarRouteSearch): SidebarRouteLocation {
  const boardSearch = matches.some((match) => match.routeId === "/projects/$projectId") ? search : undefined;
  const workflowEditorSearch = matches.some((match) => match.routeId === "/workflows/$workflowId/editor")
    ? search
    : undefined;
  return {
    pathname,
    projectID: workflowEditorSearch?.projectId,
    routeKind: boardSearch ? "board" : workflowEditorSearch ? "workflowEditor" : "other",
    taskID: boardSearch?.taskId,
    workflowID: boardSearch?.workflowId,
  };
}
export function classifySidebarRouteTransition(previous: SidebarRouteLocation, next: SidebarRouteLocation): SidebarRouteTransition {
  if (previous.pathname !== next.pathname) return { kind: "pathname" };
  if (previous.routeKind === "board" && next.routeKind === "board") {
    if (previous.workflowID !== next.workflowID) return { kind: "boardWorkflow" };
    return previous.taskID === next.taskID
      ? { kind: "none" }
      : { from: previous.taskID, kind: "boardTask", to: next.taskID };
  }
  if (previous.routeKind === "workflowEditor" && next.routeKind === "workflowEditor") {
    return previous.projectID === next.projectID ? { kind: "none" } : { kind: "workflowEditorProject" };
  }
  return { kind: "none" };
}
