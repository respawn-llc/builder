import { act, renderHook, waitFor } from "@testing-library/react";
import type * as AppFacade from "@/app-facade";
import {
  boardTaskDeletionCauseMatches,
  boardTaskDeletionCauseShouldDefer,
} from "./boardTaskDeletionCause";

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
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("reports a failed navigation for the exact deletion attempt", async () => {
    const error = new Error("navigation failed");
    navigation.closeProjectTask.mockResolvedValueOnce({ error, status: "failed" });
    const onNavigationError = vi.fn<(receivedError: unknown) => void>();
    const { result } = renderHook(() =>
      useBoardSelectedTaskDeletion({
        onNavigationError,
        projectId: "project-1",
        selectedTaskId: "task-1",
        workflowId: "workflow-1",
      }),
    );

    act(() => {
      result.current.request();
    });

    await waitFor(() => {
      expect(onNavigationError).toHaveBeenCalledWith(error);
    });
    expect(result.current.deletionCauseRef.current).toBeNull();
    expect(sidebar.invalidateSidebar).toHaveBeenCalledWith({ kind: "task", taskID: "task-1" });
  });

  it("coalesces direct-close-before-subscription requests into one operation", async () => {
    navigation.closeProjectTask.mockReturnValueOnce(new Promise(() => undefined));
    const { result } = renderHook(() =>
      useBoardSelectedTaskDeletion({
        onNavigationError: vi.fn(),
        projectId: "project-1",
        selectedTaskId: "task-1",
        workflowId: "workflow-1",
      }),
    );

    act(() => {
      result.current.request();
      result.current.request();
    });
    expect(navigation.closeProjectTask).toHaveBeenCalledTimes(1);
  });

  it("coalesces subscription-close-before-direct requests after success", async () => {
    navigation.closeProjectTask.mockResolvedValueOnce({ status: "completed" });
    const { result } = renderHook(() =>
      useBoardSelectedTaskDeletion({
        onNavigationError: vi.fn(),
        projectId: "project-1",
        selectedTaskId: "task-1",
        workflowId: "workflow-1",
      }),
    );

    act(() => {
      result.current.request();
    });
    await waitFor(() => {
      expect(result.current.deletionCauseRef.current?.succeeded).toBe(true);
    });
    act(() => {
      result.current.request();
    });

    expect(navigation.closeProjectTask).toHaveBeenCalledTimes(1);
  });

  it("allows a failed operation to be retried and then settle successfully", async () => {
    const error = new Error("navigation failed");
    navigation.closeProjectTask
      .mockResolvedValueOnce({ error, status: "failed" })
      .mockResolvedValueOnce({ status: "completed" });
    const onNavigationError = vi.fn();
    const { result } = renderHook(() =>
      useBoardSelectedTaskDeletion({
        onNavigationError,
        projectId: "project-1",
        selectedTaskId: "task-1",
        workflowId: "workflow-1",
      }),
    );

    act(() => {
      result.current.request();
    });
    await waitFor(() => {
      expect(onNavigationError).toHaveBeenCalledWith(error);
    });
    act(() => {
      result.current.request();
    });
    await waitFor(() => {
      expect(result.current.deletionCauseRef.current?.succeeded).toBe(true);
    });
    expect(navigation.closeProjectTask).toHaveBeenCalledTimes(2);
  });

  it("defers selector absence before navigation resolves and preserves the survivor on success", async () => {
    navigation.closeProjectTask.mockResolvedValueOnce({ status: "completed" });
    const { result } = renderHook(() =>
      useBoardSelectedTaskDeletion({
        onNavigationError: vi.fn(),
        projectId: "project-1",
        selectedTaskId: "task-1",
        workflowId: "workflow-1",
      }),
    );

    act(() => {
      result.current.request();
    });
    expect(boardTaskDeletionCauseShouldDefer(result.current.deletionCauseRef.current, "task-1", null)).toBe(true);
    await waitFor(() => {
      expect(boardTaskDeletionCauseMatches(result.current.deletionCauseRef.current, "task-1", null)).toBe(true);
    });
  });
});
