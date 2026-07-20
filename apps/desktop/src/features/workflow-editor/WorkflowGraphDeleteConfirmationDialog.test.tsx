import { render, screen, within } from "@testing-library/react";
import { vi } from "vitest";

import { appI18n, initializeI18n } from "@/i18n";
import { WorkflowGraphDeleteConfirmationDialog } from "./WorkflowGraphDeleteConfirmationDialog";
import { workflowDeleteConfirmationTextKeys } from "./workflowDeleteConfirmationModel";

void initializeI18n();

describe("WorkflowGraphDeleteConfirmationDialog", () => {
  it("uses branch copy for prompted branch-only deletes", () => {
    const counts = {
      edgeCount: 1,
      nodeCount: 0,
      promptCount: 1,
      transitionGroupCount: 1,
    };
    const textKeys = workflowDeleteConfirmationTextKeys(counts, "delete");
    render(<WorkflowGraphDeleteConfirmationDialog counts={counts} onCancel={vi.fn()} onConfirm={vi.fn()} />);

    const confirmation = screen.getByRole("dialog", { name: appI18n.t(textKeys.titleKey) });
    expect(
      within(confirmation).getByRole("button", { name: appI18n.t(textKeys.confirmKey) }),
    ).toBeInTheDocument();
  });
});
