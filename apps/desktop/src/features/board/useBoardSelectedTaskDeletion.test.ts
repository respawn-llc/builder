import { renderHook, waitFor } from "@testing-library/react";
import type * as AppFacade from "@/app-facade";
import type { BoardTaskDeletionAttempt } from "./boardTaskDeletionCause";

const navigation = vi.hoisted(() => ({
  closeProjectTask: vi.fn(),
}));
const sidebar = vi.hoisted(() => ({
  invalidateSidebar: vi.fn(),
}));

vi.mock("@/app-facade", async () => {
  const actual = await vi.importActual<typeof AppFacade>("@/app-facade");
  return {
    ...actual,
    useAppNavigation: () => navigation,
    useSidebar: () => sidebar,
  };
});

import { useBoardSelectedTaskDeletion } from "./useBoardSelectedTaskDeletion";

describe("useBoardSelectedTaskDeletion", () => {
  it("reports a failed navigation for the exact deletion attempt", async () => {
    const error = new Error("navigation failed");
    navigation.closeProjectTask.mockResolvedValueOnce({ error, status: "failed" });
    const onNavigationError = vi.fn<(receivedError: unknown) => void>();
    const onSelectedTaskDeleted = vi.fn<(attempt: BoardTaskDeletionAttempt) => void>();
    const onSelectedTaskDeletionNavigationFailed = vi.fn<(attempt: BoardTaskDeletionAttempt) => void>();
    const { result } = renderHook(() =>
      useBoardSelectedTaskDeletion({
        onNavigationError,
        onSelectedTaskDeleted,
        onSelectedTaskDeletionNavigationFailed,
        projectId: "project-1",
        selectedTaskId: "task-1",
        workflowId: "workflow-1",
      }),
    );

    result.current();

    await waitFor(() => {
      expect(onNavigationError).toHaveBeenCalledWith(error);
    });
    const attempt = onSelectedTaskDeleted.mock.calls[0]?.[0];
    expect(onSelectedTaskDeletionNavigationFailed.mock.calls[0]?.[0]).toBe(attempt);
    expect(sidebar.invalidateSidebar).toHaveBeenCalledWith({ kind: "task", taskID: "task-1" });
  });
});
