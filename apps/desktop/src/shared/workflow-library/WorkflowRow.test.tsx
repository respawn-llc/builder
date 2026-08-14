import { fireEvent, render, screen } from "@testing-library/react";

import type { WorkflowRecord } from "@/api";
import { WorkflowRow } from "./WorkflowRow";

const fixture = vi.hoisted(() => ({
  edit: vi.fn(),
  open: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@/shared/workflow-deletion", () => ({
  useWorkflowDeleteLauncher: () => ({
    dialog: null,
    disabled: false,
    openWorkflowDelete: vi.fn(async () => undefined),
    opening: false,
  }),
}));

beforeEach(() => {
  fixture.edit.mockClear();
  fixture.open.mockClear();
});

it("opens from the whole workflow row while keeping Edit independent", () => {
  render(<WorkflowRow contextActions={{ onEdit: fixture.edit }} onOpen={fixture.open} workflow={workflow} />);

  fireEvent.click(screen.getByRole("button", { name: workflow.name }));
  expect(fixture.open).toHaveBeenCalledOnce();

  fireEvent.click(screen.getByRole("button", { name: /workflowLibrary\.editWorkflow/u }));
  expect(fixture.edit).toHaveBeenCalledOnce();
  expect(fixture.open).toHaveBeenCalledOnce();
});

const workflow: WorkflowRecord = {
  description: "A reusable workflow",
  executionTargetPolicy: { customRef: null, mode: "default_branch" },
  id: "workflow-1",
  name: "Ship it",
  version: 7,
};
