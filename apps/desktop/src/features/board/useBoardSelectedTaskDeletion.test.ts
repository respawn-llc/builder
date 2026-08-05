import { act, renderHook, waitFor } from "@testing-library/react";
import type * as AppFacade from "@/app-facade";

const navigation = vi.hoisted(() => ({
  closeProjectTask: vi.fn(),
}));
const sidebar = vi.hoisted(() => ({
  closeSidebar: vi.fn(),
  invalidateSidebar: vi.fn(),
  openSidebar: vi.fn(),
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
  beforeEach(() => {
    vi.clearAllMocks();
    sidebar.openSidebar.mockResolvedValue({ reason: "replaced", status: "canceled" });
  });

  it("surfaces a failed route navigation after selector absence commits", async () => {
    const error = new Error("navigation failed");
    navigation.closeProjectTask.mockResolvedValueOnce({ error, status: "failed" });
    const onNavigationError = vi.fn();
    const { result, rerender } = renderCoordinator({ onNavigationError });

    await waitFor(() => {
      expect(sidebar.openSidebar).toHaveBeenCalled();
    });
    act(() => {
      result.current.request();
    });
    rerender({ selectedTaskId: "" });

    await waitFor(() => {
      expect(onNavigationError).toHaveBeenCalledWith(error);
      expect(sidebar.closeSidebar).toHaveBeenCalledWith("route_change");
    });
  });

  it("preserves the unrelated survivor after selector absence commits before success", async () => {
    navigation.closeProjectTask.mockResolvedValueOnce({ status: "completed" });
    const { result, rerender } = renderCoordinator();

    await waitFor(() => {
      expect(sidebar.openSidebar).toHaveBeenCalled();
    });
    act(() => {
      result.current.request();
    });
    rerender({ selectedTaskId: "" });

    await waitFor(() => {
      expect(navigation.closeProjectTask).toHaveBeenCalledWith("project-1", "workflow-1");
    });
    expect(sidebar.closeSidebar).not.toHaveBeenCalled();
    expect(sidebar.invalidateSidebar).toHaveBeenCalledWith({ kind: "task", taskID: "task-1" });
  });
});

function renderCoordinator({
  onNavigationError,
}: Readonly<{
  onNavigationError?: ((error: unknown) => void) | undefined;
}> = {}) {
  return renderHook(
    ({ selectedTaskId }) =>
      useBoardSelectedTaskDeletion({
        enabled: true,
        onNavigationError: onNavigationError ?? vi.fn(),
        projectId: "project-1",
        selectedTaskId,
        selectedWorkflowID: "workflow-1",
        workflowId: "workflow-1",
      }),
    { initialProps: { selectedTaskId: "task-1" } },
  );
}
