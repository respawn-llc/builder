import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { initializeI18n } from "../../i18n/setup";
import { WorkflowValidationIssues } from "./WorkflowValidationIssues";

void initializeI18n();

describe("WorkflowValidationIssues", () => {
  it("renders structured validation details next to the server message", () => {
    render(
      <WorkflowValidationIssues
        errors={[
          {
            blocksContext: true,
            code: "workflow.validation.invalid_template_placeholder",
            details: {
              fieldName: "summary",
              inputName: "summary",
              placeholder: ".Params.summary",
              providerEdgeID: "edge-provider",
              role: null,
              requiredTool: null,
            },
            edgeID: "",
            message: "Prompt template references an unknown node input.",
            nodeID: "node-1",
            relatedIDs: [],
            transitionGroupID: "",
            workflowID: "workflow-1",
          },
        ]}
      />,
    );

    expect(screen.getAllByRole("listitem")).toHaveLength(1);
  });

  it("deduplicates exact draft and execution messages for the same validation identity", () => {
    render(
      <WorkflowValidationIssues
        errors={[
          validationError("Node Proof Agent join must have exactly one outgoing transition group"),
          validationError("Node Proof Agent join must have exactly one outgoing transition group"),
        ]}
      />,
    );

    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(
      screen.getByText("Node Proof Agent join must have exactly one outgoing transition group"),
    ).toBeInTheDocument();
  });

  it("preserves distinct messages for the same validation identity", () => {
    render(
      <WorkflowValidationIssues
        errors={[
          validationError("node group must contain at least two branch nodes"),
          validationError("node group must contain exactly one join node"),
        ]}
      />,
    );

    expect(screen.getAllByRole("listitem")).toHaveLength(2);
    expect(screen.getByText("node group must contain at least two branch nodes")).toBeInTheDocument();
    expect(screen.getByText("node group must contain exactly one join node")).toBeInTheDocument();
  });
});

function validationError(message: string) {
  return {
    blocksContext: true,
    code: "workflow.validation.invalid_join_outgoing_shape",
    details: {
      fieldName: "",
      inputName: "",
      placeholder: "",
      providerEdgeID: "",
      role: null,
      requiredTool: null,
    },
    edgeID: "",
    message,
    nodeID: "join",
    relatedIDs: [],
    transitionGroupID: "",
    workflowID: "workflow-1",
  };
}
