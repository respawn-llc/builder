import { createContext, useContext, type ReactNode } from "react";

import type { ResolvedSidebarWidth, SidebarSizePreference } from "./sidebarSizing";

export type SidebarMode = "overlay" | "shift";
export type SidebarPhase = "closing" | "open";

export type SidebarCancelReason = "closed" | "replaced" | "route_change";

export type SidebarCanceledResult = Readonly<{
  status: "canceled";
  reason: SidebarCancelReason;
}>;

export type SidebarNewTaskResult = Readonly<{
  status: "submitted";
  destination: "newTask";
}>;

export type SidebarTaskDetailResult = Readonly<{
  status: "closed";
  destination: "taskDetail";
}>;

export type SidebarWorkflowResult = Readonly<{
  status: "completed";
  destination: "workflow";
  workflowID: string;
}>;

export type WorkflowInspectorSelection =
  | Readonly<{ kind: "workflow" }>
  | Readonly<{ kind: "node"; nodeID: string }>
  | Readonly<{ kind: "group"; groupID: string }>
  | Readonly<{ kind: "edge"; edgeID: string }>;

export type WorkflowInspectorInitialFocus = "firstEditableControl";

export type TaskDetailInitialFocus =
  | Readonly<{ kind: "question"; askIDs: readonly string[] }>
  | Readonly<{ kind: "approval"; taskTransitionID: string }>
  | Readonly<{ kind: "interrupted_run"; runID: string }>;

export type SidebarResult =
  | SidebarCanceledResult
  | SidebarNewTaskResult
  | SidebarTaskDetailResult
  | SidebarWorkflowResult;

export type SidebarDestination =
  | Readonly<{
      kind: "newTask";
      mode?: SidebarMode;
      boardQueryWorkflowID: string | undefined;
      projectID: string;
      workflowID: string;
    }>
  | Readonly<{
      kind: "taskDetail";
      mode?: SidebarMode;
      initialFocus?: TaskDetailInitialFocus | undefined;
      taskID: string;
      onMutated?: (() => void) | undefined;
      // Set when opened from the Home inbox so the sidebar header exposes live
      // Previous/Next navigation across the attention feed.
      inboxNav?: boolean | undefined;
    }>
  | Readonly<{
      kind: "workflowCreate";
      mode?: SidebarMode;
      projectID?: string | undefined;
    }>
  | Readonly<{
      kind: "linkWorkflow";
      mode?: SidebarMode;
      creating?: boolean | undefined;
      projectID: string;
      selectedWorkflowID?: string | undefined;
    }>
  | Readonly<{
      kind: "workflowInspect";
      mode?: SidebarMode;
      workflowID: string;
      selection: WorkflowInspectorSelection;
      initialFocus?: WorkflowInspectorInitialFocus | undefined;
    }>
  | Readonly<{
      kind: "workflowEditor";
      mode?: SidebarMode;
      projectID?: string | undefined;
      workflowID: string;
    }>
  | Readonly<{
      kind: "projectEdit";
      mode?: SidebarMode;
      projectID: string;
    }>
  | Readonly<{
      kind: "custom";
      mode?: SidebarMode;
      sizing?: SidebarSizePreference | undefined;
      title: string;
      content: ReactNode;
    }>;

export type SidebarController = Readonly<{
  activeDestination: SidebarDestination | null;
  closeSidebar(reason?: SidebarCancelReason): void;
  openSidebar(destination: SidebarDestination): Promise<SidebarResult>;
  phase: SidebarPhase;
  resolveSidebar(result: Exclude<SidebarResult, SidebarCanceledResult>): void;
  resizeSidebar(width: ResolvedSidebarWidth): void;
  sidebarWidthPx: number;
}>;

export const SidebarContext = createContext<SidebarController | null>(null);

export function useSidebar(): SidebarController {
  const value = useContext(SidebarContext);
  if (value === null) {
    throw new Error("SidebarProvider is required");
  }
  return value;
}
