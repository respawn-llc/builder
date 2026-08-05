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
import { SidebarHost } from "./sidebar";
import { SidebarProvider } from "./sidebarProvider";
import { useSidebar } from "@/app-facade";

describe("Sidebar route transition ownership", () => {
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

  it("closes the complete sidebar when browser history goes back", async () => {
    const services = createTestServices([]);
    const router = createTestRouter(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <SidebarHost />
          <OpenSidebar />
        </SidebarProvider>
      </TestAppProviders>,
      ["/", "/projects/project-1"],
    );
    render(<RouterProvider router={router} />);

    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open"),
    );
    await act(async () => {
      router.history.back();
    });

    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "closing"),
    );
  });

  it("does not close for a search-only route change", async () => {
    const services = createTestServices([]);
    const router = createTestRouter(
      <TestAppProviders services={services}>
        <SidebarProvider>
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

  it("closes for a Board workflow search transition", async () => {
    const services = createTestServices([]);
    const router = createTestRouter(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <SidebarHost />
          <OpenSidebar />
        </SidebarProvider>
      </TestAppProviders>,
      ["/projects/project-1?workflowId=workflow-1"],
    );
    render(<RouterProvider router={router} />);

    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open"),
    );
    await act(async () => {
      await router.navigate({
        params: { projectId: "project-1" },
        search: { workflowId: "workflow-2" },
        to: "/projects/$projectId",
      });
    });

    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "closing"),
    );
  });

  it("closes for a Workflow Editor project search transition", async () => {
    const services = createTestServices([]);
    const router = createTestRouter(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <SidebarHost />
          <OpenSidebar />
        </SidebarProvider>
      </TestAppProviders>,
      ["/workflows/workflow-1/editor?projectId=project-1"],
    );
    render(<RouterProvider router={router} />);

    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open"),
    );
    await act(async () => {
      await router.navigate({
        params: { workflowId: "workflow-1" },
        search: { projectId: "project-2" },
        to: "/workflows/$workflowId/editor",
      });
    });

    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "closing"),
    );
  });

  it("preserves an unrelated sidebar after a deleted Board task route clears", async () => {
    const services = createTestServices([]);
    const router = createTestRouter(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <SidebarHost />
          <OpenSidebar />
          <MarkTaskDeleted />
        </SidebarProvider>
      </TestAppProviders>,
      ["/projects/project-1?taskId=task-1"],
    );
    render(<RouterProvider router={router} />);

    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open"),
    );
    await userEvent.click(screen.getByRole("button", { name: "Mark task deleted" }));
    await act(async () => {
      await router.navigate({
        params: { projectId: "project-1" },
        search: {},
        to: "/projects/$projectId",
      });
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

function createTestRouter(
  children: ReactElement,
  initialEntries: readonly string[] = ["/projects/project-1"],
) {
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
  const workflowEditorRoute = createRoute({
    component: () => null,
    getParentRoute: () => rootRoute,
    path: "/workflows/$workflowId/editor",
    validateSearch: (search: Record<string, unknown>) =>
      z.object({ projectId: z.string().optional() }).parse(search),
  });
  return createRouter({
    history: createMemoryHistory({ initialEntries: [...initialEntries] }),
    routeTree: rootRoute.addChildren([homeRoute, projectRoute, workflowEditorRoute]),
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

function MarkTaskDeleted() {
  const { recordTaskDeletion } = useSidebar();
  return (
    <button onClick={() => { recordTaskDeletion("task-1"); }} type="button">
      Mark task deleted
    </button>
  );
}
