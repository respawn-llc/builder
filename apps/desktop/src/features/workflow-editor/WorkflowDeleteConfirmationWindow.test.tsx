import { render, screen, within } from "@testing-library/react";
import { vi } from "vitest";

import { initializeI18n } from "@/i18n";
import { WorkflowDeleteConfirmationFallbackDialog } from "./WorkflowDeleteConfirmationWindow";

void initializeI18n();

describe("WorkflowDeleteConfirmationFallbackDialog", () => {
  it("uses branch copy for prompted branch-only deletes", () => {
    render(
      <WorkflowDeleteConfirmationFallbackDialog
        counts={{
          edgeCount: 1,
          nodeCount: 0,
          promptCount: 1,
          transitionGroupCount: 1,
        }}
        onCancel={vi.fn()}
        onConfirm={vi.fn()}
      />,
    );

    const confirmation = screen.getByRole("dialog", { name: "Delete branch?" });
    expect(
      within(confirmation).getByText("This will remove the selected transition branch and its parameters."),
    ).toBeInTheDocument();
    expect(
      within(confirmation).getByText("This will remove prompt text from deleted transition branches."),
    ).toBeInTheDocument();
    expect(within(confirmation).getByText("Nodes: 0")).toBeInTheDocument();
    expect(within(confirmation).getByText("Branches: 1")).toBeInTheDocument();
    expect(within(confirmation).getByText("Prompts with text: 1")).toBeInTheDocument();
    expect(within(confirmation).getByRole("button", { name: "Delete branch" })).toBeInTheDocument();
  });
});
