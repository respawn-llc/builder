import type { WorkflowGraphSaveConfirmation, WorkflowGraphSaveImpact } from "@/api";
import type { useAppServices } from "@/app-facade";
import type {
  WorkflowDeleteConfirmationCounts,
  WorkflowGraphCascadeConfirmationOperation,
} from "./workflowDeleteConfirmationModel";
import {
  deleteWorkflowEdge,
  deleteWorkflowNode,
  deleteWorkflowNodeGroup,
  extractWorkflowNodeFromGroup,
  workflowEditorGraphMutationWarnings,
  type ExtractWorkflowNodeFromGroupInput,
  type WorkflowEditorCascadeSummary,
} from "./workflowEditorGraphMutations";
import type {
  DraftWorkflowDefinition,
  WorkflowEditorDraftAction,
  WorkflowEditorDraftState,
} from "./workflowEditorDraft";
import type { WorkflowGraphSelection } from "./workflowGraphSelection";

export type PendingGraphMutation = Readonly<{
  action: PendingGraphMutationAction;
  counts: WorkflowDeleteConfirmationCounts;
  summary: WorkflowEditorCascadeSummary;
}>;

export type PendingGraphMutationAction =
  | Readonly<{ kind: "delete"; selection: WorkflowGraphSelection }>
  | Readonly<{
      kind: "extract";
      graphVersion: number;
      input: ExtractWorkflowNodeFromGroupInput;
    }>;

export type GraphDeletionPlan =
  | Readonly<{ kind: "blocked"; warning: string }>
  | Readonly<{ kind: "ready"; summary: WorkflowEditorCascadeSummary }>;

export function confirmationFromImpact(impact: WorkflowGraphSaveImpact): WorkflowGraphSaveConfirmation {
  return {
    expectedEdgeTaskReferenceCount: impact.edgeTaskReferenceCount,
    expectedNodeTaskReferenceCount: impact.nodeTaskReferenceCount,
    expectedRemovedEdgeCount: impact.removedEdgeCount,
    expectedRemovedNodeGroupCount: impact.removedNodeGroupCount,
    expectedRemovedNodeCount: impact.removedNodeCount,
    expectedRemovedTransitionGroupCount: impact.removedTransitionGroupCount,
  };
}

export function planGraphDeletion(
  draft: DraftWorkflowDefinition,
  selection: WorkflowGraphSelection,
): GraphDeletionPlan {
  if (selection.kind === "edge") {
    const mutation = deleteWorkflowEdge(draft, selection.edgeID);
    return graphDeletionPlanFromMutation(mutation.warnings, mutation.summary);
  }
  if (selection.kind === "node") {
    const mutation = deleteWorkflowNode(draft, selection.nodeID);
    return graphDeletionPlanFromMutation(mutation.warnings, mutation.summary);
  }
  const mutation = deleteWorkflowNodeGroup(draft, selection.groupID);
  return graphDeletionPlanFromMutation(mutation.warnings, mutation.summary);
}

export function planGraphExtraction(
  draft: DraftWorkflowDefinition,
  input: ExtractWorkflowNodeFromGroupInput,
): GraphDeletionPlan {
  const mutation = extractWorkflowNodeFromGroup(draft, input);
  return graphDeletionPlanFromMutation(mutation.warnings, mutation.summary);
}

export function planPendingGraphMutation(
  state: WorkflowEditorDraftState,
  request: PendingGraphMutation,
): GraphDeletionPlan {
  if (request.action.kind === "delete") {
    return planGraphDeletion(state.draft, request.action.selection);
  }
  if (state.graphVersion !== request.action.graphVersion) {
    return {
      kind: "blocked",
      warning: workflowEditorGraphMutationWarnings.nodeGroupExtractionTopologyFailed,
    };
  }
  return planGraphExtraction(state.draft, request.action.input);
}

function graphDeletionPlanFromMutation(
  warnings: readonly string[],
  summary: WorkflowEditorCascadeSummary,
): GraphDeletionPlan {
  const warning = warnings[0];
  if (warning !== undefined) {
    return { kind: "blocked", warning };
  }
  return { kind: "ready", summary };
}

export function dispatchPendingGraphMutation(
  request: PendingGraphMutation,
  dispatch: (action: WorkflowEditorDraftAction) => void,
): void {
  if (request.action.kind === "delete") {
    dispatchGraphDeletion(request.action.selection, dispatch);
    return;
  }
  dispatch({ input: request.action.input, type: "extractNodeFromGroup" });
}

export function dispatchGraphDeletion(
  selection: WorkflowGraphSelection,
  dispatch: (action: WorkflowEditorDraftAction) => void,
): void {
  if (selection.kind === "edge") {
    dispatch({ edgeID: selection.edgeID, type: "deleteEdge" });
    return;
  }
  if (selection.kind === "node") {
    dispatch({ nodeID: selection.nodeID, type: "deleteNode" });
    return;
  }
  dispatch({ groupID: selection.groupID, type: "deleteNodeGroup" });
}

export function cascadeRowCount(summary: WorkflowEditorCascadeSummary): number {
  return (
    summary.removedNodeIDs.length + summary.removedEdgeIDs.length + summary.removedTransitionGroupIDs.length
  );
}

export function cascadeSummaryEquals(
  left: WorkflowEditorCascadeSummary,
  right: WorkflowEditorCascadeSummary,
): boolean {
  return (
    stringListEquals(left.removedNodeIDs, right.removedNodeIDs) &&
    stringListEquals(left.removedEdgeIDs, right.removedEdgeIDs) &&
    stringListEquals(left.removedTransitionGroupIDs, right.removedTransitionGroupIDs)
  );
}

function stringListEquals(left: readonly string[], right: readonly string[]): boolean {
  if (left.length !== right.length) {
    return false;
  }
  return left.every((value, index) => value === right[index]);
}

export function confirmationOperation(
  request: PendingGraphMutation,
): WorkflowGraphCascadeConfirmationOperation {
  return request.action.kind === "extract" ? "extract" : "delete";
}

export function deleteWarningTranslationKey(warning: string): string {
  if (warning === workflowEditorGraphMutationWarnings.startNodeDelete) {
    return "workflowEditor.startNodeDeleteBlocked";
  }
  if (warning === workflowEditorGraphMutationWarnings.lastTerminalDelete) {
    return "workflowEditor.lastTerminalDeleteBlocked";
  }
  return "workflowEditor.deleteBlockedGeneric";
}

export function graphEditWarningTranslationKey(warning: string): string {
  if (warning === workflowEditorGraphMutationWarnings.nodeGroupTopologyInferenceFailed) {
    return "workflowEditor.nodeGroupTopologyInferenceFailed";
  }
  if (warning === workflowEditorGraphMutationWarnings.nodeGroupExtractionTopologyFailed) {
    return "workflowEditor.nodeGroupExtractionTopologyFailed";
  }
  if (warning === workflowEditorGraphMutationWarnings.nodeGroupRequiresUngroupedNode) {
    return "workflowEditor.nodeGroupRequiresUngroupedNode";
  }
  return "workflowEditor.graphEditBlockedGeneric";
}

export async function copyWorkflowNodeText(
  value: string,
  nativeBridge: ReturnType<typeof useAppServices>["nativeBridge"],
): Promise<void> {
  if (nativeBridge.capabilities.clipboard.writeText) {
    await nativeBridge.clipboard.writeText(value);
    return;
  }
  await navigator.clipboard.writeText(value);
}
