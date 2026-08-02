import { fireEvent, screen, waitFor } from "@testing-library/react";

import {
  getCallCount,
  mountTaskDetailSurface,
  taskDetailResponse,
  taskUpdateResponse,
} from "@/test-support/task-detail";

describe("Task description checklist", () => {
  it("edits the local description draft and waits for Save before updating the Task", async () => {
    const services = mountTaskDetailSurface(
      {
        task: {
          ...taskDetailResponse.task,
          body: "Completion criteria:\n\n- [ ] Keep the Markdown source",
        },
      },
      {
        routes: [{ method: "workflow.task.update", result: taskUpdateResponse }],
      },
    );

    const checkbox = await screen.findByRole("checkbox");
    expect(checkbox).not.toBeChecked();
    expect(screen.queryByTestId("task-detail-save")).not.toBeInTheDocument();

    fireEvent.click(checkbox);

    expect(screen.getByRole("checkbox")).toBeChecked();
    expect(screen.getByTestId("task-detail-save")).toBeInTheDocument();
    expect(getCallCount(services.transport.calls, "workflow.task.update")).toBe(0);

    fireEvent.click(screen.getByTestId("task-detail-save"));

    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.update")).toBe(1);
    });
    expect(services.transport.calls).toContainEqual({
      method: "workflow.task.update",
      params: {
        task_id: "task-1",
        title: "Resolve blocker",
        body: "Completion criteria:\n\n- [x] Keep the Markdown source",
      },
    });
  });
});
