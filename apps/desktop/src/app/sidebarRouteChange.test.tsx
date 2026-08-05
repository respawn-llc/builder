import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
  useMatch,
} from "@tanstack/react-router";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, type ReactElement } from "react";
import { z } from "zod";

import { appI18n } from "@/i18n";
import type { SidebarController } from "@/app-facade";
import { useSidebar } from "@/app-facade";
import { TestAppProviders, createTestServices } from "@/test-support/app-services";
import { AppChrome } from "./AppChrome";
import { SidebarHost } from "./sidebar";
import { SidebarProvider } from "./sidebarProvider";

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
    const selectors: BoardSelectorSnapshot[] = [];
    const router = createTestRouter(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <SidebarHost />
          <OpenSidebar />
          <BoardSelectorObserver onChange={(selector) => selectors.push(selector)} />
        </SidebarProvider>
      </TestAppProviders>,
      ["/projects/project-1?workflowId=workflow-1"],
    );
    render(<RouterProvider router={router} />);

    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open"),
    );
    await waitFor(() => {
      expect(selectors).toContainEqual({ taskID: undefined, workflowID: "workflow-1" });
    });
    await act(async () => {
      await router.navigate({
        params: { projectId: "project-1" },
        search: { workflowId: "workflow-2" },
        to: "/projects/$projectId",
      });
    });

    await waitFor(() => {
      expect(selectors).toContainEqual({ taskID: undefined, workflowID: "workflow-2" });
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "closing");
    });
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
    const selectors: BoardSelectorSnapshot[] = [];
    let harness: TaskDeletionHarness | undefined;
    const router = createTestRouter(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <SidebarHost />
          <OpenSidebar />
          <BoardSelectorObserver onChange={(selector) => selectors.push(selector)} />
          <DeletionHarness onReady={(value) => { harness = value; }} />
        </SidebarProvider>
      </TestAppProviders>,
      ["/projects/project-1?taskId=task-1"],
    );
    render(<RouterProvider router={router} />);

    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open"),
    );
    await waitFor(() => {
      expect(selectors).toContainEqual({ taskID: "task-1", workflowID: undefined });
    });
    await waitFor(() => {
      expect(harness).toBeDefined();
    });
    const deletionHarness = requireDeletionHarness(harness);
    deletionHarness.recordTaskDeletion("task-1");
    await act(async () => {
      await router.navigate({
        params: { projectId: "project-1" },
        search: {},
        to: "/projects/$projectId",
      });
    });

    await waitFor(() => {
      expect(selectors).toContainEqual({ taskID: undefined, workflowID: undefined });
    });
    await act(async () => {
      deletionHarness.settleTaskDeletion("task-1", "completed");
    });
    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open"),
    );
  });

  it("defers deleted Board task route reconciliation until delayed success", async () => {
    const services = createTestServices([]);
    const selectors: BoardSelectorSnapshot[] = [];
    let harness: TaskDeletionHarness | undefined;
    const router = createTestRouter(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <SidebarHost />
          <OpenSidebar />
          <BoardSelectorObserver onChange={(selector) => selectors.push(selector)} />
          <DeletionHarness onReady={(value) => { harness = value; }} />
        </SidebarProvider>
      </TestAppProviders>,
      ["/projects/project-1?taskId=task-1"],
    );
    render(<RouterProvider router={router} />);

    await waitFor(() => {
      expect(selectors).toContainEqual({ taskID: "task-1", workflowID: undefined });
    });
    await waitFor(() => {
      expect(harness).toBeDefined();
    });
    const deletionHarness = requireDeletionHarness(harness);
    deletionHarness.recordTaskDeletion("task-1");
    await act(async () => {
      await router.navigate({
        params: { projectId: "project-1" },
        search: {},
        to: "/projects/$projectId",
      });
    });

    await waitFor(() => {
      expect(selectors).toContainEqual({ taskID: undefined, workflowID: undefined });
    });
    expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open");
    await act(async () => {
      deletionHarness.settleTaskDeletion("task-1", "completed");
    });
    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open"),
    );
  });

  it("closes after a delayed failed deleted Board task route navigation", async () => {
    const services = createTestServices([]);
    const selectors: BoardSelectorSnapshot[] = [];
    let harness: TaskDeletionHarness | undefined;
    const router = createTestRouter(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <SidebarHost />
          <OpenSidebar />
          <BoardSelectorObserver onChange={(selector) => selectors.push(selector)} />
          <DeletionHarness onReady={(value) => { harness = value; }} />
        </SidebarProvider>
      </TestAppProviders>,
      ["/projects/project-1?taskId=task-1"],
    );
    render(<RouterProvider router={router} />);

    await waitFor(() => {
      expect(selectors).toContainEqual({ taskID: "task-1", workflowID: undefined });
    });
    await waitFor(() => {
      expect(harness).toBeDefined();
    });
    const deletionHarness = requireDeletionHarness(harness);
    deletionHarness.recordTaskDeletion("task-1");
    await act(async () => {
      await router.navigate({
        params: { projectId: "project-1" },
        search: {},
        to: "/projects/$projectId",
      });
    });

    await waitFor(() => {
      expect(selectors).toContainEqual({ taskID: undefined, workflowID: undefined });
    });
    expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open");
    await act(async () => {
      deletionHarness.settleTaskDeletion("task-1", "failed");
    });
    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "closing"),
    );
  });

  it("does not close a replacement activation after a stale deletion failure", async () => {
    const services = createTestServices([]);
    const selectors: BoardSelectorSnapshot[] = [];
    let harness: TaskDeletionHarness | undefined;
    const router = createTestRouter(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <SidebarHost />
          <OpenSidebar />
          <BoardSelectorObserver onChange={(selector) => selectors.push(selector)} />
          <DeletionHarness onReady={(value) => { harness = value; }} />
        </SidebarProvider>
      </TestAppProviders>,
      ["/projects/project-1?taskId=task-1"],
    );
    render(<RouterProvider router={router} />);

    await waitFor(() => {
      expect(selectors).toContainEqual({ taskID: "task-1", workflowID: undefined });
    });
    await waitFor(() => {
      expect(harness).toBeDefined();
    });
    const deletionHarness = requireDeletionHarness(harness);
    deletionHarness.recordTaskDeletion("task-1");
    await act(async () => {
      await router.navigate({
        params: { projectId: "project-1" },
        search: {},
        to: "/projects/$projectId",
      });
    });

    await waitFor(() => {
      expect(selectors).toContainEqual({ taskID: undefined, workflowID: undefined });
    });
    await act(async () => {
      deletionHarness.replaceSidebar({
        content: <div />,
        kind: "custom",
        title: "Replacement",
      });
    });
    await act(async () => {
      deletionHarness.settleTaskDeletion("task-1", "failed");
    });

    expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open");
  });

  it("keeps a replacement activation after stale failure and later same-history invalidation", async () => {
    const services = createTestServices([]);
    const selectors: BoardSelectorSnapshot[] = [];
    let harness: TaskDeletionHarness | undefined;
    const router = createTestRouter(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <SidebarHost />
          <OpenSidebar />
          <BoardSelectorObserver onChange={(selector) => selectors.push(selector)} />
          <DeletionHarness onReady={(value) => { harness = value; }} />
        </SidebarProvider>
      </TestAppProviders>,
      ["/projects/project-1?taskId=task-1"],
    );
    render(<RouterProvider router={router} />);

    await waitFor(() => {
      expect(selectors).toContainEqual({ taskID: "task-1", workflowID: undefined });
    });
    await waitFor(() => {
      expect(harness).toBeDefined();
    });
    const deletionHarness = requireDeletionHarness(harness);
    deletionHarness.recordTaskDeletion("task-1");
    await act(async () => {
      await router.navigate({
        params: { projectId: "project-1" },
        search: {},
        to: "/projects/$projectId",
      });
    });
    await waitFor(() => {
      expect(selectors).toContainEqual({ taskID: undefined, workflowID: undefined });
    });

    await act(async () => {
      deletionHarness.replaceSidebar({
        content: <div />,
        kind: "custom",
        title: "Replacement",
      });
      deletionHarness.pushSidebar({
        kind: "taskDetail",
        mode: "overlay",
        projectID: "project-1",
        taskID: "task-2",
      });
      const result = deletionHarness.invalidateSidebar({ kind: "task", taskID: "task-2" });
      expect(result).toEqual({ kind: "discarded" });
    });
    await act(async () => {
      deletionHarness.settleTaskDeletion("task-1", "failed");
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
      z
        .object({
          filter: z.string().optional(),
          taskId: z.string().min(1).optional(),
          workflowId: z.string().min(1).optional(),
        })
        .parse(search),
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

type BoardSelectorSnapshot = Readonly<{
  taskID: string | undefined;
  workflowID: string | undefined;
}>;

function BoardSelectorObserver({
  onChange,
}: Readonly<{ onChange(selector: BoardSelectorSnapshot): void }>) {
  const match = useMatch({ from: "/projects/$projectId", shouldThrow: false });
  useEffect(() => {
    onChange({
      taskID: match?.search.taskId,
      workflowID: match?.search.workflowId,
    });
  }, [match?.search.taskId, match?.search.workflowId, onChange]);
  return null;
}

type TaskDeletionHarness = Pick<
  SidebarController,
  "invalidateSidebar" | "pushSidebar" | "recordTaskDeletion" | "replaceSidebar" | "settleTaskDeletion"
>;

function DeletionHarness({
  onReady,
}: Readonly<{ onReady(harness: TaskDeletionHarness): void }>) {
  const { invalidateSidebar, pushSidebar, recordTaskDeletion, replaceSidebar, settleTaskDeletion } = useSidebar();
  useEffect(() => {
    onReady({ invalidateSidebar, pushSidebar, recordTaskDeletion, replaceSidebar, settleTaskDeletion });
  }, [invalidateSidebar, onReady, pushSidebar, recordTaskDeletion, replaceSidebar, settleTaskDeletion]);
  return null;
}

function requireDeletionHarness(harness: TaskDeletionHarness | undefined): TaskDeletionHarness {
  if (harness === undefined) throw new Error("Deletion harness is unavailable.");
  return harness;
}
