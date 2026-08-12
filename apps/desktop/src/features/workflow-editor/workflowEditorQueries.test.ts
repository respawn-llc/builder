import {
  defaultWorkflowExecutionTargetPolicy,
  emptyWorkflowDerivedWiring,
  type WorkflowDefinition,
} from "@/api";

import { initializeWorkflowEditorDraft, workflowEditorDraftReducer } from "./workflowEditorDraft";
import { workflowSaveConfirmationPreviewKey } from "./workflowEditorQueries";

function workflowDefinition(version: number, name = "Workflow"): WorkflowDefinition {
  return {
    derivedWiring: emptyWorkflowDerivedWiring,
    edges: [],
    nodeGroups: [],
    nodes: [],
    transitionGroups: [],
    workflow: {
      description: "",
      executionTargetPolicy: defaultWorkflowExecutionTargetPolicy,
      id: "11111111-1111-4111-8111-111111111111",
      name,
      version,
    },
  };
}

describe("Workflow Editor remote conflict state", () => {
  it("retains the local draft and invalidates destructive confirmation after a remote save", () => {
    const initial = initializeWorkflowEditorDraft(workflowDefinition(1));
    const edited = workflowEditorDraftReducer(initial, {
      description: "",
      name: "Local edit",
      type: "editWorkflowMetadata",
    });
    const beforeConflictKey = workflowSaveConfirmationPreviewKey(edited);

    const conflicted = workflowEditorDraftReducer(edited, {
      source: workflowDefinition(2, "Remote edit"),
      type: "conflict",
    });
    const conflictKey = workflowSaveConfirmationPreviewKey(conflicted);

    expect(conflicted.draft.workflow.name).toBe("Local edit");
    expect(conflicted.source.workflow.version).toBe(1);
    expect(conflicted.conflict?.workflow.version).toBe(2);
    expect(conflictKey).not.toBe(beforeConflictKey);

    const kept = workflowEditorDraftReducer(conflicted, { type: "keepEditing" });
    expect(kept.draft.workflow.name).toBe("Local edit");
    expect(kept.source.workflow.version).toBe(1);
    expect(kept.conflict).toBeNull();
    expect(kept.acknowledgedConflictVersion).toBe(2);
    expect(workflowSaveConfirmationPreviewKey(kept)).toBe(conflictKey);
  });
});
