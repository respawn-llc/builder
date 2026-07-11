import { emptyWorkflowDerivedWiring, type WorkflowDefinition, type WorkflowValidation } from "../../api";
import type { WorkflowGraphLayout } from "./workflowGraphLayout";

export type WorkflowLayoutSnapshot = Readonly<{
  graphVersion: number;
  layout: WorkflowGraphLayout | undefined;
  validation: WorkflowValidation | null;
}>;

export const emptyWorkflowValidation: WorkflowValidation = { errors: [], valid: true };

export function emptyWorkflowDefinition(workflowID: string): WorkflowDefinition {
  return {
    edges: [],
    nodeGroups: [],
    nodes: [],
    transitionGroups: [],
    workflow: {
      description: "",
      executionPolicy: { customRef: null, mode: "ask" },
      version: 1,
      id: workflowID,
      name: "",
    },
    derivedWiring: emptyWorkflowDerivedWiring,
  };
}

export function mergeWorkflowValidations(
  draft: WorkflowValidation | null,
  execution: WorkflowValidation | null,
): WorkflowValidation | null {
  if (draft === null) {
    return execution;
  }
  if (execution === null) {
    return draft;
  }
  return {
    valid: draft.valid && execution.valid,
    errors: [...draft.errors, ...execution.errors],
  };
}

export function workflowLayoutSnapshotAfterRender(
  current: WorkflowLayoutSnapshot,
  update: Readonly<{
    cleanGraphVersion: number;
    cleanValidation: WorkflowValidation | null;
    layout: WorkflowGraphLayout | undefined;
  }>,
): WorkflowLayoutSnapshot {
  const graphVersion = update.cleanValidation === null ? current.graphVersion : update.cleanGraphVersion;
  const validation = update.cleanValidation ?? current.validation;
  const layout = update.layout ?? current.layout;
  return graphVersion === current.graphVersion &&
    validation === current.validation &&
    layout === current.layout
    ? current
    : { graphVersion, layout, validation };
}
