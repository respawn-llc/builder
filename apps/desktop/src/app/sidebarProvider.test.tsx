import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";

import { useSidebar } from "@/app-facade";
import { SidebarProvider } from "./sidebarProvider";

describe("SidebarProvider replacement", () => {
  it("completes only the exact current destination lifecycle", async () => {
    const wrapper = ({ children }: Readonly<{ children: ReactNode }>) => (
      <SidebarProvider>{children}</SidebarProvider>
    );
    const { result } = renderHook(() => useSidebar(), { wrapper });
    const destination = {
      kind: "taskDetail" as const,
      taskID: "task-1",
    };
    let lifecycle: Promise<unknown> | undefined;

    act(() => {
      lifecycle = result.current.openSidebar(destination);
    });
    let completion: "accepted" | "stale" = "stale";
    act(() => {
      completion = result.current.completeCurrent(destination, {
        destination: "taskDetail",
        status: "closed",
      });
    });

    expect(completion).toBe("accepted");
    if (lifecycle === undefined) {
      throw new Error("Sidebar lifecycle was not created.");
    }
    await expect(lifecycle).resolves.toEqual({
      destination: "taskDetail",
      status: "closed",
    });
  });

  it("does not let a released Task Detail owner close its replacement", () => {
    const wrapper = ({ children }: Readonly<{ children: ReactNode }>) => (
      <SidebarProvider>{children}</SidebarProvider>
    );
    const { result } = renderHook(() => useSidebar(), { wrapper });
    const deletedTask = {
      kind: "taskDetail" as const,
      taskID: "task-1",
    };
    const replacement = {
      kind: "taskDetail" as const,
      taskID: "task-2",
    };

    act(() => {
      void result.current.openSidebar(deletedTask);
    });
    act(() => {
      result.current.replaceSidebar(replacement);
    });

    expect(
      result.current.completeCurrent(deletedTask, {
        destination: "taskDetail",
        status: "closed",
      }),
    ).toBe("stale");
    expect(result.current.activeDestination).toBe(replacement);
  });

  it("preserves the original lifecycle promise until the replacement closes", async () => {
    const wrapper = ({ children }: Readonly<{ children: ReactNode }>) => (
      <SidebarProvider>{children}</SidebarProvider>
    );
    const { result } = renderHook(() => useSidebar(), { wrapper });
    let settled = false;
    let lifecycle: Promise<unknown> | undefined;

    act(() => {
      lifecycle = result.current.openSidebar({
        kind: "taskDetail",
        taskID: "task-1",
      });
      void lifecycle.then(() => {
        settled = true;
      });
    });
    act(() => {
      result.current.replaceSidebar({
        kind: "taskDetail",
        initialFocus: { kind: "dependencies" },
        taskID: "task-2",
      });
    });

    expect(result.current.activeDestination).toMatchObject({
      kind: "taskDetail",
      taskID: "task-2",
    });
    expect(settled).toBe(false);

    act(() => {
      result.current.closeSidebar();
    });
    if (lifecycle === undefined) {
      throw new Error("Sidebar lifecycle was not created.");
    }
    await expect(lifecycle).resolves.toEqual({
      status: "canceled",
      reason: "closed",
    });
  });
});
