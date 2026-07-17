import type { WorkflowEditorCascadeSummary } from "./workflowEditorGraphMutations";

export type WorkflowDeleteConfirmationCounts = Readonly<{
  nodeCount: number;
  edgeCount: number;
  promptCount: number;
  transitionGroupCount: number;
}>;

export type WorkflowGraphCascadeConfirmationOperation = "delete" | "extract";

export type WorkflowDeleteConfirmationWindowTarget = Readonly<{
  counts: WorkflowDeleteConfirmationCounts;
  operation: WorkflowGraphCascadeConfirmationOperation;
  requestID: string;
}>;

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

export function workflowDeleteConfirmationWindowOptions({
  counts,
  operation,
  requestID,
  title,
}: WorkflowDeleteConfirmationWindowTarget & Readonly<{ title: string }>) {
  return {
    initialHeight: 260,
    initialWidth: 420,
    label: `workflow-delete-${requestID}`,
    params: {
      edgeCount: counts.edgeCount.toString(),
      nodeCount: counts.nodeCount.toString(),
      operation,
      promptCount: counts.promptCount.toString(),
      requestID,
      transitionGroupCount: counts.transitionGroupCount.toString(),
    },
    route: "/native-dialog/workflow-delete-confirm",
    title,
  };
}

export function workflowDeleteConfirmationWindowTargetFromSearch(
  search: Readonly<{
    edgeCount: string;
    nodeCount: string;
    operation?: string | undefined;
    promptCount?: string | undefined;
    requestID: string;
    transitionGroupCount: string;
  }>,
): WorkflowDeleteConfirmationWindowTarget {
  return {
    counts: {
      edgeCount: parseSearchCount(search.edgeCount),
      nodeCount: parseSearchCount(search.nodeCount),
      promptCount: parseSearchCount(search.promptCount ?? ""),
      transitionGroupCount: parseSearchCount(search.transitionGroupCount),
    },
    operation: parseOperation(search.operation),
    requestID: search.requestID,
  };
}

function parseSearchCount(value: string): number {
  const count = Number(value);
  if (!Number.isSafeInteger(count) || count < 0) {
    return 0;
  }
  return count;
}

function parseOperation(value: string | undefined): WorkflowGraphCascadeConfirmationOperation {
  return value === "extract" ? "extract" : "delete";
}
