import { describe, expect, it } from "vitest";

import type { WorkflowValidationError } from "@/api";
import { normalizeWorkflowValidationErrors } from "./workflowValidationIssueNormalization";

describe("normalizeWorkflowValidationErrors", () => {
  it("preserves errors with distinct role and tool details", () => {
    const base: WorkflowValidationError = {
      blocksContext: true,
      code: "workflow.validation.role_tool",
      details: {
        fieldName: "",
        inputName: "",
        placeholder: "",
        providerEdgeID: "",
        role: null,
        requiredTool: null,
      },
      edgeID: "",
      message: "Role and tool requirements are invalid.",
      nodeID: "node-1",
      relatedIDs: [],
      transitionGroupID: "",
      workflowID: "workflow-1",
    };

    expect(
      normalizeWorkflowValidationErrors([
        base,
        {
          ...base,
          details: {
            ...base.details,
            role: "coder",
            requiredTool: "ask_question",
          },
        },
      ]),
    ).toHaveLength(2);
  });
});
