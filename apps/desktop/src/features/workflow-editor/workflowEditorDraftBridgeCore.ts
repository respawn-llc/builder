import { createContext, useContext, useEffect, useSyncExternalStore } from "react";

import type { WorkflowDerivedWiring, WorkflowGraphValidationResults, WorkflowValidation } from "@/api";
import type {
  DraftWorkflowDefinition,
  WorkflowEditorDirtyState,
  WorkflowEditorDraftAction,
  WorkflowEditorDraftState,
} from "./workflowEditorDraft";

export type WorkflowEditorDraftController = Readonly<{
  dispatch: (action: WorkflowEditorDraftAction) => void;
  dirty: WorkflowEditorDirtyState;
  draft: DraftWorkflowDefinition;
  derivedWiring: WorkflowDerivedWiring;
  draftValidation: WorkflowValidation | null;
  executionValidation: WorkflowValidation | null;
  saving: boolean;
  saveError: string;
  saveBlockers: readonly string[];
  // Structured validation captured at the last blocked save attempt, surfaced
  // as per-issue detail while the dirty graph disables background validation.
  saveValidation: WorkflowGraphValidationResults | null;
  save(): void;
  state: WorkflowEditorDraftState;
  workflowID: string;
}>;

export type WorkflowEditorDraftBridge = Readonly<{
  getController(workflowID: string): WorkflowEditorDraftController | null;
  register(controller: WorkflowEditorDraftController): () => void;
  subscribe(listener: () => void): () => void;
}>;

export const WorkflowEditorDraftBridgeContext = createContext<WorkflowEditorDraftBridge | null>(null);

const emptyWorkflowEditorDraftBridge: WorkflowEditorDraftBridge = {
  getController() {
    return null;
  },
  register() {
    return () => {
      return;
    };
  },
  subscribe() {
    return () => {
      return;
    };
  },
};

export function useRegisterWorkflowEditorDraftController(controller: WorkflowEditorDraftController): void {
  const bridge = useWorkflowEditorDraftBridge();
  useEffect(() => bridge.register(controller), [bridge, controller]);
}

export function useWorkflowEditorDraftController(workflowID: string): WorkflowEditorDraftController | null {
  const bridge = useWorkflowEditorDraftBridge();
  return useSyncExternalStore(
    bridge.subscribe,
    () => bridge.getController(workflowID),
    () => null,
  );
}

export function createWorkflowEditorDraftBridge(): WorkflowEditorDraftBridge {
  const controllers = new Map<string, WorkflowEditorDraftController>();
  const listeners = new Set<() => void>();
  return {
    getController(workflowID) {
      return controllers.get(workflowID) ?? null;
    },
    register(controller) {
      controllers.set(controller.workflowID, controller);
      emit(listeners);
      return () => {
        if (controllers.get(controller.workflowID) === controller) {
          controllers.delete(controller.workflowID);
          emit(listeners);
        }
      };
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}

function useWorkflowEditorDraftBridge(): WorkflowEditorDraftBridge {
  return useContext(WorkflowEditorDraftBridgeContext) ?? emptyWorkflowEditorDraftBridge;
}

function emit(listeners: ReadonlySet<() => void>): void {
  for (const listener of listeners) {
    listener();
  }
}
