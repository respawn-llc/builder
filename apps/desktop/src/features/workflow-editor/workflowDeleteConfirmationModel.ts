import type { WorkflowEditorCascadeSummary } from "./workflowEditorGraphMutations";

export type WorkflowDeleteConfirmationCounts = Readonly<{
  nodeCount: number;
  edgeCount: number;
  promptCount: number;
  transitionGroupCount: number;
}>;

export type WorkflowGraphCascadeConfirmationOperation = "delete" | "extract";

export type WorkflowDeleteConfirmationTextKeys = Readonly<{
  bodyKey: string;
  confirmKey: string;
  titleKey: string;
}>;

export function workflowDeleteConfirmationCountsFromSummary(
  summary: WorkflowEditorCascadeSummary,
  promptCount = 0,
): WorkflowDeleteConfirmationCounts {
  return {
    edgeCount: summary.removedEdgeIDs.length,
    nodeCount: summary.removedNodeIDs.length,
    promptCount,
    transitionGroupCount: summary.removedTransitionGroupIDs.length,
  };
}

export function workflowDeleteConfirmationTextKeys(
  counts: WorkflowDeleteConfirmationCounts,
  operation: WorkflowGraphCascadeConfirmationOperation,
): WorkflowDeleteConfirmationTextKeys {
  if (operation === "extract") {
    return {
      bodyKey: "workflowEditor.extractNodeCascadeBody",
      confirmKey: "workflowEditor.extractNodeCascadeConfirm",
      titleKey: "workflowEditor.extractNodeCascadeTitle",
    };
  }
  if (counts.nodeCount === 0 && counts.edgeCount > 0) {
    return {
      bodyKey: "workflowEditor.deleteBranchCascadeBody",
      confirmKey: "workflowEditor.deleteBranchCascadeConfirm",
      titleKey: "workflowEditor.deleteBranchCascadeTitle",
    };
  }
  return {
    bodyKey: "workflowEditor.deleteCascadeBody",
    confirmKey: "workflowEditor.deleteCascadeConfirm",
    titleKey: "workflowEditor.deleteCascadeTitle",
  };
}
