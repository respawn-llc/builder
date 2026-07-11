import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createElement, useEffect, useState } from "react";
import { afterEach, vi } from "vitest";

import { AppProviders } from "./AppProviders";
import { nextReachableHistoryIndex } from "./navigation";
import { useAppNavigation, type AppNavigation } from "./navigation";
import { createTestServices, startupRoutes } from "../testSupport/appServices";

describe("navigation stack state", () => {
  const originalMatchMedia = globalThis.matchMedia;
  const originalStartViewTransitionDescriptor = Object.getOwnPropertyDescriptor(
    document,
    "startViewTransition",
  );

  afterEach(() => {
    Object.defineProperty(globalThis, "matchMedia", {
      configurable: true,
      value: originalMatchMedia,
    });
    restoreStartViewTransition(originalStartViewTransitionDescriptor);
  });

  it("preserves forward availability after back and truncates it on push", () => {
    const afterPushes = nextReachableHistoryIndex(nextReachableHistoryIndex(0, "PUSH", 1), "PUSH", 2);
    const afterBack = nextReachableHistoryIndex(afterPushes, "BACK", 1);
    const canGoForwardAfterBack = 1 < afterBack;
    const afterPushFromBack = nextReachableHistoryIndex(afterBack, "PUSH", 2);
    const canGoForwardAfterPush = 2 < afterPushFromBack;

    expect(canGoForwardAfterBack).toBe(true);
    expect(canGoForwardAfterPush).toBe(false);
  });

  it("keeps the navigation API stable across component rerenders", async () => {
    const observed: AppNavigation[] = [];
    const rootRoute = createRootRoute({ component: Outlet });
    const indexRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: "/",
      component: () => createElement(NavigationIdentityProbe, { observed }),
    });
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/"] }),
      routeTree: rootRoute.addChildren([indexRoute]),
    });

    render(
      createElement(AppProviders, {
        children: createElement(RouterProvider, { router }),
        services: createTestServices(startupRoutes),
      }),
    );

    await waitFor(() => {
      expect(observed).toHaveLength(1);
    });
    fireEvent.click(screen.getByRole("button", { name: /rerender/u }));

    await waitFor(() => {
      expect(observed).toHaveLength(2);
    });
    expect(observed[1]).toBe(observed[0]);
    expect(observed[1]?.openProjectTask).toBe(observed[0]?.openProjectTask);
  });

  it("runs default transitions for page-level navigation and immediate updates for project task selection", async () => {
    installMatchMedia(false);
    const startViewTransition = vi.fn((update: () => void | Promise<void>): ViewTransitionTestHandle => {
      const updateCallbackDone = Promise.resolve(update());
      return {
        finished: updateCallbackDone,
        ready: Promise.resolve(),
        updateCallbackDone,
      };
    });
    installStartViewTransition(startViewTransition);
    const observed: AppNavigation[] = [];
    const rootRoute = createRootRoute({ component: Outlet });
    const indexRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: "/",
      component: () => createElement(NavigationIdentityProbe, { observed }),
    });
    const projectRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: "/projects/$projectId",
      component: () => createElement(NavigationIdentityProbe, { observed }),
    });
    const workflowRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: "/workflows",
      component: () => createElement(NavigationIdentityProbe, { observed }),
    });
    const workflowEditorRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: "/workflows/$workflowId/editor",
      component: () => createElement(NavigationIdentityProbe, { observed }),
    });
    const taskRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: "/tasks/$taskId",
      component: () => createElement(NavigationIdentityProbe, { observed }),
    });
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/"] }),
      routeTree: rootRoute.addChildren([
        indexRoute,
        projectRoute,
        workflowRoute,
        workflowEditorRoute,
        taskRoute,
      ]),
    });

    render(
      createElement(AppProviders, {
        children: createElement(RouterProvider, { router }),
        services: createTestServices(startupRoutes),
      }),
    );

    await waitFor(() => {
      expect(observed).toHaveLength(1);
    });
    const navigation = observed[0];
    if (navigation === undefined) {
      throw new Error("navigation probe did not render");
    }
    await act(async () => {
      await navigation.openWorkflowEditor({ workflowID: "workflow-1", projectID: "project-1" });
    });
    expectRoute(router, "/workflows/workflow-1/editor", { projectId: "project-1" });
    await act(async () => {
      await navigation.openWorkflowLibrary();
    });
    expectRoute(router, "/workflows", {});
    await act(async () => {
      await navigation.openTask("task-1");
    });
    expectRoute(router, "/tasks/task-1", {});
    await act(async () => {
      await navigation.openProject("project-1", "workflow-1");
    });
    expectRoute(router, "/projects/project-1", { taskId: "", workflowId: "workflow-1" });
    await act(async () => {
      await navigation.openProjectTask("project-1", "workflow-1", "task-1");
    });
    expectRoute(router, "/projects/project-1", {
      taskId: "task-1",
      workflowId: "workflow-1",
    });
    await act(async () => {
      await navigation.closeProjectTask("project-1", "workflow-1");
    });
    expectRoute(router, "/projects/project-1", { taskId: "", workflowId: "workflow-1" });
    await act(async () => {
      await navigation.openHome();
    });
    expectRoute(router, "/", {});

    expect(startViewTransition).toHaveBeenCalledTimes(5);
  });
});

function NavigationIdentityProbe({ observed }: Readonly<{ observed: AppNavigation[] }>) {
  const [renderCount, setRenderCount] = useState(0);
  const navigation = useAppNavigation();

  useEffect(() => {
    observed.push(navigation);
  });

  return createElement(
    "button",
    {
      onClick: () => {
        setRenderCount((current) => current + 1);
      },
      type: "button",
    },
    `rerender ${renderCount.toString()}`,
  );
}

function expectRoute(
  router: Readonly<{ state: Readonly<{ location: Readonly<{ pathname: string; searchStr: string }> }> }>,
  pathname: string,
  search: Readonly<Record<string, string>>,
): void {
  expect(router.state.location.pathname).toBe(pathname);
  expect(Object.fromEntries(new URLSearchParams(router.state.location.searchStr))).toEqual(search);
}

type ViewTransitionTestHandle = Readonly<{
  finished: Promise<void>;
  ready: Promise<void>;
  updateCallbackDone: Promise<void>;
}>;

function installMatchMedia(matches: boolean): void {
  Object.defineProperty(globalThis, "matchMedia", {
    configurable: true,
    value: vi.fn(() => ({
      matches,
      media: "(prefers-reduced-motion: reduce)",
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });
}

function installStartViewTransition(
  startViewTransition: (update: () => void | Promise<void>) => ViewTransitionTestHandle,
): void {
  Object.defineProperty(document, "startViewTransition", {
    configurable: true,
    value: startViewTransition,
  });
}

function restoreStartViewTransition(descriptor: PropertyDescriptor | undefined): void {
  if (descriptor === undefined) {
    Reflect.deleteProperty(document, "startViewTransition");
    return;
  }
  Object.defineProperty(document, "startViewTransition", descriptor);
}
