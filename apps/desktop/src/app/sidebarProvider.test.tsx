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
});
