import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { WorkflowDeleteButton } from "./WorkflowDeleteButton";

const fixture = vi.hoisted(() => ({
  deleteWorkflow: vi.fn(async () => ({ blockers: [], deleted: true })),
  previewWorkflowDelete: vi.fn(async () => ({
    linkCount: 1,
    projectCount: 1,
    taskCount: 2,
    version: 7,
    workflowID: "workflow-1",
  })),
  push: vi.fn(),
}));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal()),
  useMatchRoute: () => () => false,
}));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppNavigation: () => ({ openWorkflowLibrary: vi.fn(async () => "completed") }),
  useAppServices: () => ({
    api: {
      deleteWorkflow: fixture.deleteWorkflow,
      previewWorkflowDelete: fixture.previewWorkflowDelete,
    },
  }),
  useConnectionSnapshot: () => ({ phase: "connected" }),
  useStatusController: () => ({ push: fixture.push }),
}));

describe("WorkflowDeleteButton completion", () => {
  beforeEach(() => {
    fixture.deleteWorkflow.mockClear();
    fixture.previewWorkflowDelete.mockClear();
    fixture.push.mockClear();
  });

  it("notifies its mounted owner only after deletion invalidation finishes", async () => {
    const queryClient = new QueryClient();
    let finishInvalidation: () => void = () => {
      return;
    };
    const invalidation = new Promise<void>((resolve) => {
      finishInvalidation = resolve;
    });
    vi.spyOn(queryClient, "invalidateQueries").mockImplementation(async () => invalidation);
    const onDeleted = vi.fn();
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <WorkflowDeleteButton onDeleted={onDeleted} workflowID="workflow-1" />
      </QueryClientProvider>,
    );

    await user.click(screen.getByRole("button", { name: "workflowEditor.workflowDelete" }));
    await user.click(await screen.findByRole("button", { name: "workflowEditor.workflowDeleteConfirm" }));
    await waitFor(() => {
      expect(fixture.deleteWorkflow).toHaveBeenCalledOnce();
    });
    expect(onDeleted).not.toHaveBeenCalled();

    finishInvalidation();
    await waitFor(() => {
      expect(onDeleted).toHaveBeenCalledOnce();
    });
    queryClient.clear();
  });
});
