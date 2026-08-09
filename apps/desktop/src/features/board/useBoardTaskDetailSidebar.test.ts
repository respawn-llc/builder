import { renderHook } from "@testing-library/react";

import type { AppNavigation, SidebarRootHandle, SidebarRootOutcome } from "@/app-facade";
import { useBoardTaskDetailSidebar } from "./useBoardTaskDetailSidebar";

const navigation: AppNavigation = {
  back: vi.fn(async () => undefined),
  closeProjectTask: vi.fn(async () => undefined),
  forward: vi.fn(async () => undefined),
  openHome: vi.fn(async () => undefined),
  openProject: vi.fn(async () => undefined),
  openProjectTask: vi.fn(async () => undefined),
  openTask: vi.fn(async () => undefined),
  openWorkflowEditor: vi.fn(async () => undefined),
  openWorkflowLibrary: vi.fn(async () => "completed" as const),
  replaceTask: vi.fn(async () => undefined),
};

describe("Board Task Detail sidebar route owner", () => {
  it("pushes route changes through one root so browser Back reuses the prior entry", () => {
    const push = vi.fn(() => "accepted" as const);
    let resolveLifecycle: ((outcome: SidebarRootOutcome) => void) | undefined;
    const root: SidebarRootHandle = {
      lifecycle: new Promise((resolve) => {
        resolveLifecycle = resolve;
      }),
      push,
      release: vi.fn(),
    };
    const openSidebar = vi.fn(() => root);
    const onNavigationError = vi.fn();
    const { rerender } = renderHook(
      ({ selectedTaskID }: Readonly<{ selectedTaskID: string }>) => {
        useBoardTaskDetailSidebar({
          navigation,
          onNavigationError,
          openSidebar,
          projectID: "project-1",
          selectedTaskID,
          workflowID: "workflow-1",
        });
      },
      { initialProps: { selectedTaskID: "task-1" } },
    );

    expect(openSidebar).toHaveBeenCalledWith({
      kind: "taskDetail",
      mode: "overlay",
      onMutated: undefined,
      taskID: "task-1",
    });

    rerender({ selectedTaskID: "task-2" });
    expect(push).toHaveBeenCalledWith({
      kind: "taskDetail",
      mode: "overlay",
      onMutated: undefined,
      taskID: "task-2",
    });

    rerender({ selectedTaskID: "task-1" });
    expect(push).toHaveBeenLastCalledWith({
      kind: "taskDetail",
      mode: "overlay",
      onMutated: undefined,
      taskID: "task-1",
    });
    expect(openSidebar).toHaveBeenCalledOnce();
    expect(onNavigationError).not.toHaveBeenCalled();
    resolveLifecycle?.("released");
  });
});
