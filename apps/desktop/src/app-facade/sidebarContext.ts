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
  | Readonly<{ kind: "approval"; approvalID: string }>
  | Readonly<{ kind: "interrupted_current_node" }>
  | Readonly<{ kind: "dependencies" }>;

export type SidebarResult =
  SidebarCanceledResult | SidebarNewTaskResult | SidebarTaskDetailResult | SidebarWorkflowResult;

export type SidebarDestination =
  | Readonly<{
      kind: "newTask";
      mode?: SidebarMode;
      boardQueryWorkflowID: string | undefined;
      initialSourceWorkspaceID?: string | undefined;
      pendingRelationship?:
        | Readonly<{
            originTaskID: string;
            newTaskRole: "blocker" | "blocked";
          }>
        | undefined;
      projectID: string;
      workflowID: string;
    }>
  | Readonly<{
      kind: "taskDetail";
      mode?: SidebarMode;
      initialFocus?: TaskDetailInitialFocus | undefined;
      projectID: string;
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

export type SidebarTaskDetailSnapshot = Readonly<{
  kind: "taskDetail";
  scrollTop: number;
  descriptionExpanded: boolean;
  selectedTab: "comments" | "activity";
  titleBodyDraft?: Readonly<{ title: string; body: string }> | undefined;
  newCommentDraft?: string | undefined;
  editedCommentDraft?: Readonly<{ commentID: string; body: string }> | undefined;
}>;

export type SidebarDestinationSnapshot = SidebarTaskDetailSnapshot;
export type SidebarStateCapture = () => SidebarDestinationSnapshot | null;

export type SidebarInvalidationTarget =
  | Readonly<{ kind: "task"; taskID: string }>
  | Readonly<{ kind: "project"; projectID: string }>;

export type SidebarInvalidationResult =
  | Readonly<{ kind: "absent" }>
  | Readonly<{ kind: "discarded" }>
  | Readonly<{ kind: "closed" }>;

export type SidebarController = Readonly<{
  activeDestination: SidebarDestination | null;
  backSidebar(): void;
  canGoBack: boolean;
  closeSidebar(reason?: SidebarCancelReason): void;
  invalidateSidebar(target: SidebarInvalidationTarget): SidebarInvalidationResult;
  clearTaskDeletion(): void;
  recordTaskDeletion(taskID: string): void;
  openSidebar(destination: SidebarDestination): Promise<SidebarResult>;
  pushSidebar(destination: SidebarDestination): void;
  replaceSidebar(destination: SidebarDestination): void;
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
