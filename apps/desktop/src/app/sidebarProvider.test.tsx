import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";

import { useSidebar } from "@/app-facade";
import { SidebarProvider } from "./sidebarProvider";

describe("SidebarProvider replacement", () => {
  it("cancels the lifecycle promise when the provider unmounts", async () => {
    const wrapper = ({ children }: Readonly<{ children: ReactNode }>) => (
      <SidebarProvider>{children}</SidebarProvider>
    );
    const { result, unmount } = renderHook(() => useSidebar(), { wrapper });
    let lifecycle: Promise<unknown> | undefined;

    act(() => {
      lifecycle = result.current.openSidebar({ kind: "taskDetail", taskID: "task-1" });
    });
    unmount();

    if (lifecycle === undefined) {
      throw new Error("Sidebar lifecycle was not created.");
    }
    await expect(lifecycle).resolves.toEqual({
      status: "canceled",
      reason: "closed",
    });
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

  it("captures drafts before refocusing the current Task Detail", () => {
    const wrapper = ({ children }: Readonly<{ children: ReactNode }>) => (
      <SidebarProvider>{children}</SidebarProvider>
    );
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => {
      void result.current.openSidebar({ kind: "taskDetail", taskID: "task-1" });
    });
    const token = result.current.activeToken;
    if (token === null) {
      throw new Error("Sidebar token was not created.");
    }
    const snapshot = {
      kind: "taskDetail",
      scrollTop: 180,
      descriptionExpanded: true,
      selectedTab: "comments",
      titleBodyDraft: { title: "Draft title", body: "Draft body" },
      newCommentDraft: "Draft comment",
    } as const;
    result.current.registerSidebarStateCapture(token, () => snapshot);

    act(() => {
      result.current.replaceSidebar({
        kind: "taskDetail",
        taskID: "task-1",
        initialFocus: { kind: "dependencies" },
      });
    });

    expect(result.current.activeDestination).toEqual({
      kind: "taskDetail",
      taskID: "task-1",
      initialFocus: { kind: "dependencies" },
    });
    expect(result.current.activeSnapshot).toEqual(snapshot);
  });

  it("keeps the active activation stable during the close animation", () => {
    const wrapper = ({ children }: Readonly<{ children: ReactNode }>) => (
      <SidebarProvider>{children}</SidebarProvider>
    );
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => {
      void result.current.openSidebar({ kind: "taskDetail", taskID: "task-1" });
    });
    const activationID = result.current.activeActivationID;

    act(() => {
      result.current.closeSidebar();
    });

    expect(result.current.phase).toBe("closing");
    expect(result.current.activeActivationID).toBe(activationID);
  });

  it("ignores a pop-out completion from an activation superseded by Back", () => {
    const wrapper = ({ children }: Readonly<{ children: ReactNode }>) => (
      <SidebarProvider>{children}</SidebarProvider>
    );
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => {
      void result.current.openSidebar({ kind: "taskDetail", taskID: "task-1" });
    });
    const openedActivationID = result.current.activeActivationID;
    if (openedActivationID === null) {
      throw new Error("Sidebar activation was not created.");
    }

    act(() => {
      result.current.pushSidebar({ kind: "taskDetail", taskID: "task-2" });
    });
    act(() => {
      result.current.backSidebar();
    });

    expect(result.current.activeDestination).toEqual({
      kind: "taskDetail",
      taskID: "task-1",
    });
    expect(result.current.activeActivationID).not.toBe(openedActivationID);

    act(() => {
      result.current.closeSidebarIfCurrentActivation(openedActivationID);
    });

    expect(result.current.activeDestination).toEqual({
      kind: "taskDetail",
      taskID: "task-1",
    });
  });
});

describe("SidebarProvider stack contract", () => {
  const wrapper = ({ children }: Readonly<{ children: ReactNode }>) => (
    <SidebarProvider>{children}</SidebarProvider>
  );

  it("keeps one lifecycle promise while exposing stack tokens and Back", async () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    let lifecycle: Promise<unknown> | undefined;

    act(() => {
      lifecycle = result.current.openSidebar({
        kind: "taskDetail",
        taskID: "task-1",
      });
    });
    const rootToken = result.current.activeToken;
    const rootActivationID = result.current.activeActivationID;

    expect(rootToken).not.toBeNull();
    expect(rootActivationID).not.toBeNull();
    expect(result.current.stackDestinations).toEqual([{ kind: "taskDetail", taskID: "task-1" }]);
    expect(result.current.canGoBack).toBe(false);

    act(() => {
      result.current.pushSidebar({
        kind: "taskDetail",
        taskID: "task-2",
      });
    });

    expect(result.current.stackDestinations).toEqual([
      { kind: "taskDetail", taskID: "task-1" },
      { kind: "taskDetail", taskID: "task-2" },
    ]);
    expect(result.current.activeDestination).toEqual({
      kind: "taskDetail",
      taskID: "task-2",
    });
    const pushedActivationID = result.current.activeActivationID;
    expect(pushedActivationID).not.toBe(rootActivationID);
    expect(result.current.canGoBack).toBe(true);

    act(() => {
      result.current.backSidebar();
    });
    expect(result.current.activeDestination).toEqual({
      kind: "taskDetail",
      taskID: "task-1",
    });
    expect(result.current.activeActivationID).not.toBe(pushedActivationID);
    expect(result.current.canGoBack).toBe(false);

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
    expect(rootToken).not.toBeNull();
  });

  it("ignores stale token operations and only settles the matching lifecycle", async () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    let firstLifecycle: Promise<unknown> | undefined;
    act(() => {
      firstLifecycle = result.current.openSidebar({
        kind: "taskDetail",
        taskID: "task-1",
      });
    });
    const staleToken = result.current.activeToken;
    if (staleToken === null) {
      throw new Error("Sidebar token was not created.");
    }

    act(() => {
      result.current.replaceSidebar({
        kind: "taskDetail",
        taskID: "task-2",
      });
    });
    const currentToken = result.current.activeToken;
    expect(currentToken).not.toEqual(staleToken);
    if (currentToken === null) {
      throw new Error("Current sidebar token was not created.");
    }

    act(() => {
      result.current.replaceSidebarIfCurrent(staleToken, {
        kind: "taskDetail",
        taskID: "stale",
      });
      result.current.closeSidebarIfCurrent(staleToken);
      result.current.resolveSidebarIfCurrent(staleToken, {
        status: "closed",
        destination: "taskDetail",
      });
    });
    expect(result.current.activeDestination).toEqual({
      kind: "taskDetail",
      taskID: "task-2",
    });

    act(() => {
      result.current.resolveSidebarIfCurrent(currentToken, {
        status: "closed",
        destination: "taskDetail",
      });
    });
    if (firstLifecycle === undefined) {
      throw new Error("Sidebar lifecycle was not created.");
    }
    await expect(firstLifecycle).resolves.toEqual({
      status: "closed",
      destination: "taskDetail",
    });
  });

  it("captures current state only for the matching token before Push", () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => {
      void result.current.openSidebar({
        kind: "taskDetail",
        taskID: "task-1",
      });
    });
    const token = result.current.activeToken;
    if (token === null) {
      throw new Error("Sidebar token was not created.");
    }
    const capture = vi.fn(() => ({
      kind: "taskDetail" as const,
      scrollTop: 42,
      descriptionExpanded: true,
      selectedTab: "comments" as const,
    }));
    const cleanup = result.current.registerSidebarStateCapture(token, capture);

    act(() => {
      result.current.pushSidebar({
        kind: "taskDetail",
        taskID: "task-2",
      });
    });

    expect(capture).toHaveBeenCalledOnce();
    act(() => {
      cleanup();
      result.current.backSidebar();
    });
    expect(result.current.activeToken?.entryID).toBe(token.entryID);
  });

  it("restores retained Task Detail drafts and presentation state after Back", () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => {
      void result.current.openSidebar({ kind: "taskDetail", taskID: "task-1" });
    });
    const token = result.current.activeToken;
    if (token === null) {
      throw new Error("Sidebar token was not created.");
    }
    result.current.registerSidebarStateCapture(token, () => ({
      kind: "taskDetail",
      scrollTop: 88,
      descriptionExpanded: true,
      selectedTab: "activity",
      titleBodyDraft: { title: "draft title", body: "draft body" },
      newCommentDraft: "draft comment",
    }));

    act(() => {
      result.current.pushSidebar({ kind: "taskDetail", taskID: "task-2" });
    });
    act(() => {
      result.current.backSidebar();
    });

    expect(result.current.activeSnapshot).toEqual({
      kind: "taskDetail",
      scrollTop: 88,
      descriptionExpanded: true,
      selectedTab: "activity",
      titleBodyDraft: { title: "draft title", body: "draft body" },
      newCommentDraft: "draft comment",
    });
  });

  it("blocks Push when the active capture reports a pending save", () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => {
      void result.current.openSidebar({ kind: "taskDetail", taskID: "task-1" });
    });
    const token = result.current.activeToken;
    if (token === null) {
      throw new Error("Sidebar token was not created.");
    }
    result.current.registerSidebarStateCapture(token, () => null);

    act(() => {
      result.current.pushSidebar({ kind: "taskDetail", taskID: "task-2" });
    });

    expect(result.current.stackDestinations).toEqual([{ kind: "taskDetail", taskID: "task-1" }]);
  });

  it("preserves the exit block when replacement capture reports a pending save", () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => {
      void result.current.openSidebar({ kind: "taskDetail", taskID: "task-1" });
    });
    const token = result.current.activeToken;
    if (token === null) {
      throw new Error("Sidebar token was not created.");
    }
    act(() => {
      result.current.setSidebarExitBlocked(token, true);
    });
    result.current.registerSidebarStateCapture(token, () => null);

    act(() => {
      result.current.replaceSidebar({ kind: "taskDetail", taskID: "task-1" });
    });

    expect(result.current.sidebarExitBlocked).toBe(true);
  });

  it("blocks sidebar exits only for the current destination token", () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => {
      void result.current.openSidebar({
        kind: "newTask",
        projectID: "project-1",
        workflowID: "workflow-1",
        boardQueryWorkflowID: undefined,
      });
    });
    const staleToken = result.current.activeToken;
    if (staleToken === null) {
      throw new Error("Sidebar token was not created.");
    }

    act(() => {
      result.current.setSidebarExitBlocked(staleToken, true);
    });
    expect(result.current.sidebarExitBlocked).toBe(true);

    act(() => {
      result.current.replaceSidebar({ kind: "taskDetail", taskID: "task-1" });
    });
    expect(result.current.sidebarExitBlocked).toBe(false);

    act(() => {
      result.current.setSidebarExitBlocked(staleToken, true);
    });
    expect(result.current.sidebarExitBlocked).toBe(false);
  });

  it("keeps traversal bounded while rendering one current destination", () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => {
      void result.current.openSidebar({ kind: "taskDetail", taskID: "task-0" });
    });
    for (let index = 1; index <= 60; index += 1) {
      act(() => {
        result.current.pushSidebar({ kind: "taskDetail", taskID: `task-${index.toString()}` });
      });
    }

    expect(result.current.stackDestinations).toHaveLength(50);
    expect(result.current.stackDestinations[0]).toEqual({
      kind: "taskDetail",
      taskID: "task-0",
    });
    expect(result.current.activeDestination).toEqual({
      kind: "taskDetail",
      taskID: "task-60",
    });
  });

  it("removes a deleted non-current entry without closing surviving destinations", () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => {
      void result.current.openSidebar({ kind: "taskDetail", taskID: "task-1" });
    });
    const rootToken = result.current.activeToken;
    act(() => {
      result.current.pushSidebar({ kind: "taskDetail", taskID: "task-2" });
    });
    if (rootToken === null) {
      throw new Error("Root token was not created.");
    }

    act(() => {
      result.current.removeSidebarEntry(rootToken);
    });

    expect(result.current.stackDestinations).toEqual([{ kind: "taskDetail", taskID: "task-2" }]);
  });

  it("preserves surviving entries when deletion clears the anchored route", () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => {
      void result.current.openSidebar({ kind: "taskDetail", taskID: "task-1" });
    });
    const rootToken = result.current.activeToken;
    if (rootToken === null) {
      throw new Error("Root token was not created.");
    }
    act(() => {
      result.current.pushSidebar({ kind: "taskDetail", taskID: "task-2" });
    });

    act(() => {
      result.current.preserveSidebarOnNextRouteChange(rootToken, {
        kind: "projectTaskCleared",
        projectID: "project-1",
        workflowID: "workflow-1",
      });
      result.current.removeSidebarEntry(rootToken);
    });

    expect(result.current.stackDestinations).toEqual([{ kind: "taskDetail", taskID: "task-2" }]);
    expect(
      result.current.consumeSidebarRouteChangePreservation({
        pathname: "/projects/project-1",
        searchStr: "workflowId=workflow-1",
      }),
    ).toBe(true);
    expect(
      result.current.consumeSidebarRouteChangePreservation({
        pathname: "/projects/project-1",
        searchStr: "workflowId=workflow-1",
      }),
    ).toBe(false);
  });

  it("does not preserve a mismatched route and clears failed deletion intent", () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => {
      void result.current.openSidebar({ kind: "taskDetail", taskID: "task-1" });
    });
    const token = result.current.activeToken;
    if (token === null) {
      throw new Error("Token was not created.");
    }
    act(() => {
      result.current.preserveSidebarOnNextRouteChange(token, {
        kind: "projectTaskCleared",
        projectID: "project-1",
        workflowID: "workflow-1",
      });
      result.current.clearSidebarRouteChangePreservation(token);
    });

    expect(
      result.current.consumeSidebarRouteChangePreservation({
        pathname: "/projects/project-1",
        searchStr: "workflowId=workflow-1",
      }),
    ).toBe(false);
  });

  it("clears route preservation when the final entry closes or a lifecycle is replaced", () => {
    const { result } = renderHook(() => useSidebar(), { wrapper });
    act(() => {
      void result.current.openSidebar({ kind: "taskDetail", taskID: "task-1" });
    });
    const token = result.current.activeToken;
    if (token === null) {
      throw new Error("Token was not created.");
    }
    act(() => {
      result.current.preserveSidebarOnNextRouteChange(token, {
        kind: "projectTaskCleared",
        projectID: "project-1",
        workflowID: "workflow-1",
      });
      result.current.removeSidebarEntry(token);
    });
    expect(
      result.current.consumeSidebarRouteChangePreservation({
        pathname: "/projects/project-1",
        searchStr: "workflowId=workflow-1",
      }),
    ).toBe(false);

    act(() => {
      void result.current.openSidebar({ kind: "taskDetail", taskID: "task-2" });
    });
    const replacementToken = result.current.activeToken;
    if (replacementToken === null) {
      throw new Error("Replacement token was not created.");
    }
    act(() => {
      result.current.preserveSidebarOnNextRouteChange(replacementToken, {
        kind: "projectTaskCleared",
        projectID: "project-1",
        workflowID: "workflow-1",
      });
      result.current.replaceSidebar({ kind: "taskDetail", taskID: "task-3" });
    });
    expect(
      result.current.consumeSidebarRouteChangePreservation({
        pathname: "/projects/project-1",
        searchStr: "workflowId=workflow-1",
      }),
    ).toBe(false);
  });
});
