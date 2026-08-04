import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";

import { useSidebar } from "@/app-facade";
import { taskDetailSidebarDestination } from "./sidebarDestinationAdapter";
import { useSidebarHost } from "./sidebarHostContext";
import { SidebarProvider } from "./sidebarProvider";

const wrapper = ({ children }: Readonly<{ children: ReactNode }>) => (
  <SidebarProvider>{children}</SidebarProvider>
);

function task(taskID: string) {
  return taskDetailSidebarDestination(taskID, "project-1");
}

function newTask() {
  return {
    boardQueryWorkflowID: undefined,
    kind: "newTask" as const,
    projectID: "project-1",
    workflowID: "workflow-1",
  };
}

describe("SidebarProvider", () => {
  it("keeps one lifecycle promise until the replacing destination closes", async () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    let lifecycle: Promise<unknown> | undefined;
    act(() => {
      lifecycle = result.current.openSidebar(task("task-1"));
    });
    act(() => result.current.replaceSidebar(task("task-2")));

    expect(result.current.activeDestination).toEqual(task("task-2"));
    act(() => result.current.closeSidebar());
    await expect(lifecycle).resolves.toEqual({ status: "canceled", reason: "closed" });
  });

  it("pushes and backs through the minimal facade without exposing stack entries or keys", () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => void result.current.openSidebar(task("task-1")));
    act(() => result.current.pushSidebar(task("task-2")));

    expect(result.current.activeDestination).toEqual(task("task-2"));
    expect(result.current.canGoBack).toBe(true);
    act(() => result.current.backSidebar());
    expect(result.current.activeDestination).toEqual(task("task-1"));
    expect(result.current.canGoBack).toBe(false);
  });

  it("retains state through the shell-private scoped capture action", () => {
    const { result } = renderHook(
      () => ({ sidebar: useSidebar(), host: useSidebarHost() }),
      { wrapper },
    );
    act(() => void result.current.sidebar.openSidebar(task("task-1")));
    act(() => {
      result.current.host.actions.capture(() => ({
        kind: "taskDetail",
        scrollTop: 42,
        descriptionExpanded: true,
        selectedTab: "activity",
      }));
      result.current.sidebar.pushSidebar(task("task-2"));
    });
    act(() => result.current.sidebar.backSidebar());

    expect(result.current.host.snapshot).toEqual({
      kind: "taskDetail",
      scrollTop: 42,
      descriptionExpanded: true,
      selectedTab: "activity",
    });
  });

  it("blocks Push when the current capture reports pending save", () => {
    const { result } = renderHook(
      () => ({ sidebar: useSidebar(), host: useSidebarHost() }),
      { wrapper },
    );
    act(() => void result.current.sidebar.openSidebar(task("task-1")));
    act(() => {
      result.current.host.actions.capture(() => null);
      result.current.sidebar.pushSidebar(task("task-2"));
    });
    expect(result.current.sidebar.activeDestination).toEqual(task("task-1"));
  });

  it("rejects stale scoped actions after a replacing open", () => {
    const { result } = renderHook(
      () => ({ sidebar: useSidebar(), host: useSidebarHost() }),
      { wrapper },
    );
    act(() => void result.current.sidebar.openSidebar(task("task-1")));
    const stale = result.current.host.actions;
    act(() => void result.current.sidebar.openSidebar(task("task-2")));
    act(() => stale.replace(task("stale")));

    expect(result.current.sidebar.activeDestination).toEqual(task("task-2"));
  });

  it("remounts same-destination replacements with a fresh private activation key", () => {
    const { result } = renderHook(
      () => ({ sidebar: useSidebar(), host: useSidebarHost() }),
      { wrapper },
    );
    act(() => void result.current.sidebar.openSidebar(task("task-1")));
    const firstKey = result.current.host.key;

    act(() => void result.current.sidebar.openSidebar(task("task-1")));

    expect(result.current.host.key).not.toBe(firstKey);
  });

  it("rejects an old A action after an A-to-B-to-A activation", () => {
    const { result } = renderHook(
      () => ({ sidebar: useSidebar(), host: useSidebarHost() }),
      { wrapper },
    );
    act(() => void result.current.sidebar.openSidebar(task("task-a")));
    const stale = result.current.host.actions;
    const firstKey = result.current.host.key;
    act(() => result.current.host.actions.capture(() => ({
      kind: "taskDetail",
      scrollTop: 17,
      descriptionExpanded: true,
      selectedTab: "comments",
    })));
    act(() => result.current.sidebar.pushSidebar(task("task-b")));
    act(() => result.current.sidebar.backSidebar());

    const returned = {
      activeDestination: result.current.sidebar.activeDestination,
      hostKey: result.current.host.key,
      snapshot: result.current.host.snapshot,
    };
    expect(returned.activeDestination).toEqual(task("task-a"));
    expect(returned.hostKey).not.toBe(firstKey);
    act(() => stale.replace(task("stale")));
    expect({
      activeDestination: result.current.sidebar.activeDestination,
      hostKey: result.current.host.key,
      snapshot: result.current.host.snapshot,
    }).toEqual(returned);
  });

  it("invalidates current and inactive typed Task destinations", () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => void result.current.openSidebar(task("task-1")));
    act(() => result.current.pushSidebar(task("task-2")));
    expect(result.current.invalidateSidebar({ kind: "task", taskID: "task-1" })).toEqual({
      kind: "discarded",
    });
    expect(result.current.activeDestination).toEqual(task("task-2"));
    expect(result.current.invalidateSidebar({ kind: "task", taskID: "task-2" })).toEqual({
      kind: "closed",
    });
  });

  it("admits one current New Task mutation and makes stale release inert", () => {
    const { result } = renderHook(
      () => ({ sidebar: useSidebar(), host: useSidebarHost() }),
      { wrapper },
    );
    act(() => void result.current.sidebar.openSidebar(task("task-1")));
    act(() => result.current.sidebar.pushSidebar(newTask()));
    let release: (() => void) | null = null;
    act(() => {
      release = result.current.host.actions.admitMutation();
    });

    expect(release).not.toBeNull();
    expect(result.current.host.mutationAdmitted).toBe(true);
    act(() => result.current.sidebar.backSidebar());
    expect(result.current.sidebar.activeDestination).toEqual(newTask());

    act(() => void result.current.sidebar.openSidebar(task("task-2")));
    act(() => release?.());
    expect(result.current.sidebar.activeDestination).toEqual(task("task-2"));
    expect(result.current.host.mutationAdmitted).toBe(false);
  });
});
