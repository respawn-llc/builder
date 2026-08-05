import { act, renderHook, waitFor } from "@testing-library/react";
import type * as AppFacade from "@/app-facade";
import type { AppNavigationResult } from "@/app-facade";

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
    const { result, rerender } = renderCoordinator({ initialTaskId: undefined, onNavigationError });
    rerender({ selectedTaskId: "task-1" });

    await waitFor(() => {
      expect(sidebar.openSidebar).toHaveBeenCalled();
    });
    await act(async () => {
      await Promise.resolve();
    });
    sidebar.closeSidebar.mockClear();
    await act(async () => {
      result.current.request();
      await Promise.resolve();
    });
    rerender({ selectedTaskId: undefined });

    await waitFor(() => {
      expect(onNavigationError).toHaveBeenCalledWith(error);
      expect(sidebar.closeSidebar).toHaveBeenCalledWith("route_change");
    });
  });

  it("preserves the unrelated survivor after selector absence commits before success", async () => {
    let resolveNavigation: ((result: AppNavigationResult) => void) | undefined;
    navigation.closeProjectTask.mockReturnValueOnce(
      new Promise<AppNavigationResult>((resolve) => {
        resolveNavigation = resolve;
      }),
    );
    const { result, rerender } = renderCoordinator({ initialTaskId: undefined });
    rerender({ selectedTaskId: "task-1" });

    await waitFor(() => {
      expect(sidebar.openSidebar).toHaveBeenCalled();
    });
    await act(async () => {
      await Promise.resolve();
    });
    sidebar.closeSidebar.mockClear();
    await act(async () => {
      result.current.request();
      await Promise.resolve();
    });
    rerender({ selectedTaskId: undefined });

    expect(navigation.closeProjectTask).toHaveBeenCalledWith("project-1", "workflow-1");
    expect(sidebar.closeSidebar).not.toHaveBeenCalled();
    if (resolveNavigation === undefined) {
      throw new Error("Navigation resolver is unavailable.");
    }
    resolveNavigation({ status: "completed" });
    await waitFor(() => {
      expect(sidebar.closeSidebar).not.toHaveBeenCalled();
    });
    expect(sidebar.invalidateSidebar).toHaveBeenCalledWith({ kind: "task", taskID: "task-1" });
  });
});

function renderCoordinator({
  initialTaskId = "task-1",
  onNavigationError,
}: Readonly<{
  initialTaskId?: string | undefined;
  onNavigationError?: ((error: unknown) => void) | undefined;
}> = {}) {
  return renderHook<
    ReturnType<typeof useBoardSelectedTaskDeletion>,
    Readonly<{ selectedTaskId: string | undefined }>
  >(
    ({ selectedTaskId }: Readonly<{ selectedTaskId: string | undefined }>) =>
      useBoardSelectedTaskDeletion({
        enabled: true,
        onNavigationError: onNavigationError ?? vi.fn(),
        projectId: "project-1",
        selectedTaskId,
        selectedWorkflowID: "workflow-1",
      }),
    { initialProps: { selectedTaskId: initialTaskId } },
  );
}
