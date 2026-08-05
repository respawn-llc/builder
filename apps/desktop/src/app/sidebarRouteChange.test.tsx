import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, type ReactElement } from "react";
import { z } from "zod";

import { appI18n } from "@/i18n";
import { TestAppProviders, createTestServices } from "@/test-support/app-services";
import { AppChrome } from "./AppChrome";
import { SidebarHost, SidebarRouteChangeCloser } from "./sidebar";
import { SidebarProvider } from "./sidebarProvider";
import { useSidebar } from "@/app-facade";

describe("SidebarRouteChangeCloser", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it.each(["/", "/projects/project-2"])(
    "closes the complete sidebar when the browser pathname changes to %s",
    async (nextPathname) => {
      const services = createTestServices([]);
      const router = createTestRouter(
        <TestAppProviders services={services}>
          <SidebarProvider>
            <SidebarRouteChangeCloser />
            <SidebarHost />
            <OpenSidebar />
          </SidebarProvider>
        </TestAppProviders>,
      );
      render(<RouterProvider router={router} />);

      await waitFor(() =>
        expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open"),
      );
      await act(async () => {
        if (nextPathname === "/") {
          await router.navigate({ to: "/" });
        } else {
          await router.navigate({
            params: { projectId: "project-2" },
            to: "/projects/$projectId",
          });
        }
      });

      await waitFor(() =>
        expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "closing"),
      );
    },
  );

  it("does not close for a search-only route change", async () => {
    const services = createTestServices([]);
    const router = createTestRouter(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <SidebarRouteChangeCloser />
          <SidebarHost />
          <OpenSidebar />
        </SidebarProvider>
      </TestAppProviders>,
    );
    render(<RouterProvider router={router} />);

    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open"),
    );
    await act(async () => {
      router.history.push("/projects/project-1?filter=next");
    });

    expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open");
  });

  it("closes the sidebar through the AppChrome Home navigation", async () => {
    const services = createTestServices([]);
    const router = createTestRouter(
      <TestAppProviders services={services}>
        <AppChrome>
          <OpenSidebar />
        </AppChrome>
      </TestAppProviders>,
    );
    render(<RouterProvider router={router} />);

    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open"),
    );
    const nativeStartViewTransitionDescriptor = Object.getOwnPropertyDescriptor(
      document,
      "startViewTransition",
    );
    Object.defineProperty(document, "startViewTransition", {
      configurable: true,
      value: (update: () => void | Promise<void>) => {
        const updateCallbackDone = Promise.resolve().then(update);
        return {
          finished: updateCallbackDone,
          ready: Promise.resolve(),
          updateCallbackDone,
        };
      },
    });
    try {
      const homeLabel = appI18n.t.bind(appI18n)("app.home");
      await userEvent.click(screen.getByRole("link", { name: homeLabel }));

      await waitFor(() => {
        expect(screen.queryByTestId("app-sidebar-host")).toBeNull();
      });
    } finally {
      if (nativeStartViewTransitionDescriptor === undefined) {
        Reflect.deleteProperty(document, "startViewTransition");
      } else {
        Object.defineProperty(document, "startViewTransition", nativeStartViewTransitionDescriptor);
      }
    }
  });
});

function createTestRouter(children: ReactElement) {
  const rootRoute = createRootRoute({ component: () => children });
  const homeRoute = createRoute({
    component: () => null,
    getParentRoute: () => rootRoute,
    path: "/",
  });
  const projectRoute = createRoute({
    component: () => null,
    getParentRoute: () => rootRoute,
    path: "/projects/$projectId",
    validateSearch: (search: Record<string, unknown>) =>
      z.object({ filter: z.string().optional() }).parse(search),
  });
  return createRouter({
    history: createMemoryHistory({ initialEntries: ["/projects/project-1"] }),
    routeTree: rootRoute.addChildren([homeRoute, projectRoute]),
  });
}

function OpenSidebar() {
  const { openSidebar } = useSidebar();
  useEffect(() => {
    void openSidebar({
      content: <div>Task</div>,
      kind: "custom",
      title: "Task",
    });
  }, [openSidebar]);
  return null;
}
