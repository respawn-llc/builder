import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { initializeI18n } from "@/i18n";
import { DetailSection, ValidationDetails } from "./WorkflowInspectorPrimitives";

void initializeI18n();

describe("ValidationDetails", () => {
  it("exposes titled inspector sections as named regions", () => {
    render(
      <DetailSection title="Route">
        <button type="button">Target node</button>
      </DetailSection>,
    );

    const routeSection = screen.getByRole("region", { name: "Route" });

    expect(within(routeSection).getByRole("heading", { level: 3, name: "Route" })).toBeInTheDocument();
    expect(within(routeSection).getByRole("button", { name: "Target node" })).toBeInTheDocument();
  });

  it("preserves region naming when the section title is hidden", () => {
    render(
      <DetailSection hideTitle title="Route">
        <button type="button">Context mode</button>
      </DetailSection>,
    );

    const routeSection = screen.getByRole("region", { name: "Route" });
    expect(within(routeSection).getByRole("heading", { level: 3, name: "Route" })).toBeInTheDocument();
    expect(within(routeSection).getByRole("button", { name: "Context mode" })).toBeInTheDocument();
  });

  it("shows structured validation details as a plain bullet list", () => {
    render(
      <ValidationDetails
        errors={[
          {
            blocksContext: true,
            code: "workflow.validation.invalid_join_input_provider",
            details: {
              fieldName: "",
              inputName: "summary",
              placeholder: "",
              providerEdgeID: "edge-provider",
              role: null,
              requiredTool: null,
            },
            edgeID: "edge-provider",
            message: "Join input provider is invalid.",
            nodeID: "join",
            relatedIDs: [],
            transitionGroupID: "",
            workflowID: "workflow-1",
          },
        ]}
      />,
    );

    const section = screen.getByRole("region", { name: "Validation errors" });
    const item = within(within(section).getByRole("list")).getByRole("listitem");

    expect(within(item).getByText("Join input provider is invalid.")).toBeInTheDocument();
    expect(within(item).getByText("Parameter: summary · Provider branch: edge-provider")).toBeInTheDocument();
    expect(screen.queryByText("workflow.validation.invalid_join_input_provider")).not.toBeInTheDocument();
  });
});
