import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => {
  const activeToken = {
    lifecycleID: "lifecycle-1",
    entryID: "entry-task-b",
  };
  const deletedToken = {
    lifecycleID: "lifecycle-1",
    entryID: "entry-task-a",
  };
  return {
    activeToken,
    deletedToken,
    navigation: {
      closeProjectTask: vi.fn(async (): Promise<void> => {
        await Promise.resolve();
      }),
    },
    sidebar: {
      activeToken,
      clearSidebarRouteChangePreservation: vi.fn(),
      preserveSidebarOnNextRouteChange: vi.fn(),
      removeSidebarEntry: vi.fn(),
      stackDestinations: [{ kind: "taskDetail", taskID: "task-b" }],
      stackEntryTokens: [activeToken],
    },
  };
});

vi.mock("@/app-facade", async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return {
    ...actual,
    useAppNavigation: () => mocks.navigation,
    useSidebar: () => mocks.sidebar,
  };
});

import { useBoardSelectedTaskDeletion } from "./useBoardSelectedTaskDeletion";

describe("useBoardSelectedTaskDeletion", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.navigation.closeProjectTask.mockResolvedValue(undefined);
    mocks.sidebar.activeToken = mocks.activeToken;
    mocks.sidebar.stackDestinations = [{ kind: "taskDetail", taskID: "task-b" }];
    mocks.sidebar.stackEntryTokens = [mocks.activeToken];
  });

  it("preserves an unrelated active sidebar stack during selected-task route cleanup", async () => {
    const onNavigationError = vi.fn();
    const { result } = renderHook(() =>
      useBoardSelectedTaskDeletion({
        onNavigationError,
        projectId: "project-1",
        selectedTaskId: "task-a",
        workflowId: "workflow-1",
      }),
    );

    await act(async () => {
      await result.current();
    });

    expect(mocks.sidebar.preserveSidebarOnNextRouteChange).toHaveBeenCalledWith(
      mocks.activeToken,
      {
        kind: "projectTaskCleared",
        projectID: "project-1",
        workflowID: "workflow-1",
      },
    );
    expect(mocks.sidebar.removeSidebarEntry).not.toHaveBeenCalled();
    expect(mocks.navigation.closeProjectTask).toHaveBeenCalledWith("project-1", "workflow-1");
    expect(onNavigationError).not.toHaveBeenCalled();
  });

  it("coalesces concurrent cleanup for the same selected Task", async () => {
    mocks.sidebar.activeToken = mocks.deletedToken;
    mocks.sidebar.stackDestinations = [{ kind: "taskDetail", taskID: "task-a" }];
    mocks.sidebar.stackEntryTokens = [mocks.deletedToken];
    let resolveCloseProjectTask: (() => void) | undefined;
    mocks.navigation.closeProjectTask.mockImplementation(async () => {
      await new Promise<void>((resolve) => {
        resolveCloseProjectTask = resolve;
      });
    });
    const { result } = renderHook(() =>
      useBoardSelectedTaskDeletion({
        onNavigationError: vi.fn(),
        projectId: "project-1",
        selectedTaskId: "task-a",
        workflowId: "workflow-1",
      }),
    );

    const first = result.current();
    const second = result.current();

    expect(mocks.navigation.closeProjectTask).toHaveBeenCalledTimes(1);
    expect(mocks.sidebar.preserveSidebarOnNextRouteChange).toHaveBeenCalledTimes(1);
    expect(mocks.sidebar.removeSidebarEntry).toHaveBeenCalledTimes(1);

    resolveCloseProjectTask?.();
    await Promise.all([first, second]);
    await result.current();
    expect(mocks.navigation.closeProjectTask).toHaveBeenCalledTimes(1);
    expect(mocks.sidebar.removeSidebarEntry).toHaveBeenCalledTimes(1);
  });
});
