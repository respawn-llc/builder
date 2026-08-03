import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => {
  const activeToken = {
    lifecycleID: "lifecycle-1",
    entryID: "entry-task-b",
  };
  return {
    activeToken,
    navigation: {
      closeProjectTask: vi.fn(async () => undefined),
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
});
