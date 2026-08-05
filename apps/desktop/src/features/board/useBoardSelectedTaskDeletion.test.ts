import { renderHook, waitFor } from "@testing-library/react";
import type * as AppFacade from "@/app-facade";
import type { BoardTaskDeletionAttempt } from "./boardTaskDeletionCause";
import type { AppNavigationResult } from "@/app-facade";

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

  it("coalesces direct-close-before-subscription requests into one operation", async () => {
    const navigationResult = deferred<AppNavigationResult>();
    navigation.closeProjectTask.mockReturnValueOnce(navigationResult.promise);
    const onSelectedTaskDeleted = vi.fn<(attempt: BoardTaskDeletionAttempt) => void>();
    const onSelectedTaskDeletionNavigationSucceeded = vi.fn<(attempt: BoardTaskDeletionAttempt) => void>();
    const { result } = renderHook(() =>
      useBoardSelectedTaskDeletion({
        onNavigationError: vi.fn(),
        onSelectedTaskDeleted,
        onSelectedTaskDeletionNavigationSucceeded,
        projectId: "project-1",
        selectedTaskId: "task-1",
        workflowId: "workflow-1",
      }),
    );

    result.current();
    result.current();
    expect(navigation.closeProjectTask).toHaveBeenCalledTimes(1);
    expect(onSelectedTaskDeleted).toHaveBeenCalledTimes(1);

    navigationResult.resolve({ status: "completed" });
    await waitFor(() => {
      expect(onSelectedTaskDeletionNavigationSucceeded).toHaveBeenCalledWith(
        onSelectedTaskDeleted.mock.calls[0]?.[0],
      );
    });
  });

  it("coalesces subscription-close-before-direct requests after success", async () => {
    navigation.closeProjectTask.mockResolvedValueOnce({ status: "completed" });
    const onSelectedTaskDeleted = vi.fn<(attempt: BoardTaskDeletionAttempt) => void>();
    const onSelectedTaskDeletionNavigationSucceeded = vi.fn<(attempt: BoardTaskDeletionAttempt) => void>();
    const { result } = renderHook(() =>
      useBoardSelectedTaskDeletion({
        onNavigationError: vi.fn(),
        onSelectedTaskDeleted,
        onSelectedTaskDeletionNavigationSucceeded,
        projectId: "project-1",
        selectedTaskId: "task-1",
        workflowId: "workflow-1",
      }),
    );

    result.current();
    await waitFor(() => {
      expect(onSelectedTaskDeletionNavigationSucceeded).toHaveBeenCalledOnce();
    });
    result.current();

    expect(navigation.closeProjectTask).toHaveBeenCalledTimes(1);
    expect(onSelectedTaskDeleted).toHaveBeenCalledOnce();
  });

  it("allows a failed operation to be retried and then settle successfully", async () => {
    const error = new Error("navigation failed");
    navigation.closeProjectTask
      .mockResolvedValueOnce({ error, status: "failed" })
      .mockResolvedValueOnce({ status: "completed" });
    const onNavigationError = vi.fn();
    const onSelectedTaskDeletionNavigationSucceeded = vi.fn();
    const { result } = renderHook(() =>
      useBoardSelectedTaskDeletion({
        onNavigationError,
        onSelectedTaskDeletionNavigationSucceeded,
        projectId: "project-1",
        selectedTaskId: "task-1",
        workflowId: "workflow-1",
      }),
    );

    result.current();
    await waitFor(() => {
      expect(onNavigationError).toHaveBeenCalledWith(error);
    });
    result.current();
    await waitFor(() => {
      expect(onSelectedTaskDeletionNavigationSucceeded).toHaveBeenCalledOnce();
    });
    expect(navigation.closeProjectTask).toHaveBeenCalledTimes(2);
  });
});

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  resolve(value: T): void;
}> {
  let resolvePromise: ((value: T) => void) | undefined;
  const promise = new Promise<T>((resolve) => {
    resolvePromise = resolve;
  });
  return {
    promise,
    resolve(value) {
      resolvePromise?.(value);
    },
  };
}
