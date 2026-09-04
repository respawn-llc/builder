import {
  createContext,
  createElement,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import type { CreatedTaskSummary, TaskDependencyDirection, TaskStatus } from "@/api";
import type { ResolvedSidebarWidth, SidebarSizePreference } from "./sidebarSizing";

export type SidebarMode = "overlay" | "shift";
export type SidebarPhase = "closing" | "open";
export type SidebarTransitionDirection = "back" | "push" | "replace";
export type SidebarRootOutcome = "closed" | "released" | "replaced";
export type SidebarNavigationOutcome = "accepted" | "stale" | "unavailable";

export type NewTaskPreparedDependency = Readonly<{
  direction: TaskDependencyDirection;
  taskID: string;
  shortID: string;
  title: string;
  workflowID: string;
  status: TaskStatus;
}>;

export type SidebarBackResult = Readonly<{
  kind: "newTaskCreated";
  direction: TaskDependencyDirection;
  task: CreatedTaskSummary & Readonly<{ status: TaskStatus }>;
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

export type LinkWorkflowCompletion =
  Readonly<{ kind: "created"; workflowID: string }> | Readonly<{ kind: "linked"; workflowID: string }>;

export type SidebarDestination =
  | (Readonly<{
      kind: "newTask";
      mode?: SidebarMode;
      initialSourceWorkspaceID?: string | undefined;
      initialPreparedDependency?: NewTaskPreparedDependency | undefined;
      onCreated?: ((taskID: string) => void | Promise<void>) | undefined;
      parentReturnDirection?: TaskDependencyDirection | undefined;
      projectID: string;
    }> &
      (
        | Readonly<{
            boardQueryWorkflowID: string | undefined;
            workflowID: string;
          }>
        | Readonly<{
            boardQueryWorkflowID: undefined;
            workflowID?: undefined;
          }>
      ))
  | Readonly<{
      kind: "taskDetail";
      mode?: SidebarMode;
      initialFocus?: TaskDetailInitialFocus | undefined;
      taskID: string;
      onMutated?: (() => void) | undefined;
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
      onCompleted: (completion: LinkWorkflowCompletion) => void | Promise<void>;
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
      kind: "workflowSettings";
      mode?: SidebarMode;
      workflowID: string;
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
      kind: "processes";
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

export type SidebarDestinationPolicy = Readonly<{
  applyBackResult(destination: SidebarDestination, state: unknown, result: SidebarBackResult): unknown;
  equals(left: SidebarDestination, right: SidebarDestination): boolean;
  retainedState(destination: SidebarDestination, state: unknown): unknown;
}>;

export type SidebarPageNavigator = Readonly<{
  back(result?: SidebarBackResult): Exclude<SidebarNavigationOutcome, "unavailable">;
  close(): Exclude<SidebarNavigationOutcome, "unavailable">;
  push(destination: SidebarDestination): SidebarNavigationOutcome;
  replace(destination: SidebarDestination): Exclude<SidebarNavigationOutcome, "unavailable">;
  registerAvailability(availability: Readonly<{ back: boolean; close: boolean }>): () => void;
  registerCapture(capture: () => unknown): () => void;
}>;

export type SidebarRootHandle = Readonly<{
  lifecycle: Promise<SidebarRootOutcome>;
  release(): void;
}>;

export type SidebarRootController = Readonly<{
  open(destination: SidebarDestination): SidebarRootHandle;
}>;

export type SidebarShellController = Readonly<{
  activeDestination: SidebarDestination | null;
  back(): SidebarNavigationOutcome;
  backAvailable: boolean;
  canGoBack: boolean;
  close(): SidebarNavigationOutcome;
  closeAvailable: boolean;
  phase: SidebarPhase;
  resize(width: ResolvedSidebarWidth): void;
  sidebarWidthPx: number;
  transitionDirection: SidebarTransitionDirection | null;
}>;

export const SidebarRootContext = createContext<SidebarRootController | null>(null);
export const SidebarShellContext = createContext<SidebarShellController | null>(null);
const SidebarRootOwnerContext = createContext<SidebarRootController | null>(null);

export function useSidebarRoots(): SidebarRootController {
  const value = useContext(SidebarRootContext);
  if (value === null) {
    throw new Error("SidebarProvider is required");
  }
  return value;
}

export function useSidebarShell(): SidebarShellController {
  const value = useContext(SidebarShellContext);
  if (value === null) {
    throw new Error("SidebarProvider is required");
  }
  return value;
}

export function SidebarRootOwner({ children }: Readonly<{ children: ReactNode }>) {
  const roots = useSidebarRoots();
  const [handles] = useState(() => new Set<SidebarRootHandle>());
  const open = useCallback(
    (destination: SidebarDestination) => {
      const handle = roots.open(destination);
      handles.add(handle);
      void handle.lifecycle.finally(() => {
        handles.delete(handle);
      });
      return handle;
    },
    [handles, roots],
  );
  useEffect(
    () => () => {
      for (const handle of handles) handle.release();
      handles.clear();
    },
    [handles],
  );
  const value = useMemo(() => ({ open }), [open]);
  return createElement(SidebarRootOwnerContext.Provider, { value }, children);
}

export function useOwnedSidebarRoots(): SidebarRootController {
  const value = useContext(SidebarRootOwnerContext);
  if (value === null) throw new Error("SidebarRootOwner is required");
  return value;
}

export function useSidebarBackWhen(condition: boolean, navigator: SidebarPageNavigator | undefined): void {
  useEffect(() => {
    if (condition) navigator?.back();
  }, [condition, navigator]);
}
