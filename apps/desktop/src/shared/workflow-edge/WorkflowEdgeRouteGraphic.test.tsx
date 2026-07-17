import { render, screen, within } from "@testing-library/react";

import { initializeI18n } from "@/test-support/i18n";
import { WorkflowEdgeRouteGraphic } from "./WorkflowEdgeRouteGraphic";

void initializeI18n();

describe("WorkflowEdgeRouteGraphic", () => {
  it("renders an accessible edge route summary", () => {
    render(
      <WorkflowEdgeRouteGraphic
        contextMode="compact_and_continue_session"
        sourceLabel="Backlog"
        targetLabel="Planning Preparation"
      />,
    );

    const graphic = screen.getByTestId("workflow-edge-route-graphic");
    expect(graphic).toHaveAccessibleName("Backlog to Planning Preparation");
    expect(within(graphic).getByText("Backlog")).toBeInTheDocument();
    expect(within(graphic).getByText("Planning Preparation")).toBeInTheDocument();
  });
});
