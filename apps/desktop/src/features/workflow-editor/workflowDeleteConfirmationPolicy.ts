import {
  workflowDeleteConfirmationCountsFromSummary,
  type WorkflowDeleteConfirmationCounts,
} from "./workflowDeleteConfirmationModel";
import type { DraftWorkflowDefinition } from "./workflowEditorDraft";
import type { WorkflowEditorCascadeSummary } from "./workflowEditorGraphMutations";

export function workflowDeleteNeedsConfirmation(counts: WorkflowDeleteConfirmationCounts): boolean {
  return counts.nodeCount + counts.edgeCount + counts.transitionGroupCount > 1 || counts.promptCount > 0;
}

export function workflowDeletionConfirmationCounts(
  draft: DraftWorkflowDefinition,
  summary: WorkflowEditorCascadeSummary,
): WorkflowDeleteConfirmationCounts {
  return workflowDeleteConfirmationCountsFromSummary(summary, deletedPromptCount(draft, summary));
}

function deletedPromptCount(draft: DraftWorkflowDefinition, summary: WorkflowEditorCascadeSummary): number {
  const removedEdgeIDs = new Set(summary.removedEdgeIDs);
  return draft.edges.filter((edge) => removedEdgeIDs.has(edge.id) && edge.promptTemplate.trim().length > 0)
    .length;
}
