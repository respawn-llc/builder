export type SidebarRouteKind = "board" | "workflowEditor" | "other";
type SimpleTransition = "none" | "boardWorkflow" | "workflowEditorProject";

export type SidebarRouteLocation = Readonly<{
  routeKind: SidebarRouteKind;
  projectID: string | undefined;
  taskID: string | undefined;
  workflowID: string | undefined;
}>;

export type SidebarRouteTransition =
  | Readonly<{ kind: SimpleTransition }>
  | Readonly<{ kind: "boardTask"; from: string | undefined; to: string | undefined }>;

export function classifySidebarRouteTransition(
  previous: SidebarRouteLocation,
  next: SidebarRouteLocation,
): SidebarRouteTransition {
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
