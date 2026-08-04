import { createMemoryHistory, createRootRoute, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, useMemo } from "react";
import { z } from "zod";

import { createBrowserNativeBridge } from "@app/native-bridge";
import { useSidebar, type SidebarDestination } from "@/app-facade";
import {
  activityResponse,
  emptyTaskAttentionResponse,
  taskDetailResponse,
} from "@/test-support/task-detail";
import { createTestServices, TestAppProviders } from "@/test-support/app-services";
import { SidebarHost } from "./sidebar";
import { SidebarProvider } from "./sidebarProvider";

describe("SidebarHost navigation controls", () => {
  it("keeps X and Back in one leading control, remounts keyed destinations, and animates direction", async () => {
    const mounts: string[] = [];
    const services = createTestServices([]);
    render(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <ShellScenario mounts={mounts} />
        </SidebarProvider>
      </TestAppProviders>,
    );

    const user = userEvent.setup();
    await waitFor(() => expect(screen.getByTestId("sidebar-content-root")).toBeVisible());
    const leadingControls = screen.getByTestId("app-sidebar-leading-controls");
    expect(leadingControls).toContainElement(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();

    await user.click(screen.getByRole("button", { name: "Push" }));
    await waitFor(() => expect(screen.getByTestId("sidebar-content-child")).toBeVisible());
    expect(screen.getByRole("button", { name: "Back" })).toBeVisible();

    await user.click(screen.getByRole("button", { name: "Back" }));
    await waitFor(() => expect(screen.getByTestId("sidebar-content-root")).toBeVisible());
    expect(mounts).toEqual(["root", "child", "root"]);
  });

  it("keeps the outgoing destination rendered during the close phase", async () => {
    const services = createTestServices([]);
    render(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <ShellScenario mounts={[]} />
        </SidebarProvider>
      </TestAppProviders>,
    );

    const user = userEvent.setup();
    await waitFor(() => expect(screen.getByTestId("sidebar-content-root")).toBeVisible());
    await user.click(screen.getByRole("button", { name: "Close" }));

    expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "closing");
    expect(screen.getByTestId("sidebar-content-root")).toBeVisible();
  });

  it("uses the scoped Pop out completion for the current activation only", async () => {
    const nativeWindow = deferred<undefined>();
    const browserBridge = createBrowserNativeBridge();
    const nativeBridge = {
      ...browserBridge,
      capabilities: { ...browserBridge.capabilities, dialogWindows: true },
      dialogs: { openWindow: async () => nativeWindow.promise },
    };
    const services = createTestServices(taskRoutes(), nativeBridge);
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/tasks/task-a"] }),
      routeTree: createRootRoute(),
    });

    render(
      <RouterContextProvider router={router}>
        <TestAppProviders services={services}>
          <SidebarProvider>
            <TaskShellScenario />
          </SidebarProvider>
        </TestAppProviders>
      </RouterContextProvider>,
    );

    const user = userEvent.setup();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Open in new window" })).toBeVisible(),
    );
    await user.click(screen.getByRole("button", { name: "Open in new window" }));
    await user.click(screen.getByRole("button", { name: "Push task-b" }));
    await act(async () => {
      nativeWindow.resolve(undefined);
      await nativeWindow.promise;
    });

    expect(screen.getByTestId("current-sidebar-task")).toHaveTextContent("task-b");
    expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open");
  });

  it("rejects an old Pop out completion after A-to-B-to-A", async () => {
    const nativeWindow = deferred<undefined>();
    const browserBridge = createBrowserNativeBridge();
    const nativeBridge = {
      ...browserBridge,
      capabilities: { ...browserBridge.capabilities, dialogWindows: true },
      dialogs: { openWindow: async () => nativeWindow.promise },
    };
    const services = createTestServices(taskRoutes(), nativeBridge);
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/tasks/task-a"] }),
      routeTree: createRootRoute(),
    });

    render(
      <RouterContextProvider router={router}>
        <TestAppProviders services={services}>
          <SidebarProvider>
            <TaskShellScenario />
          </SidebarProvider>
        </TestAppProviders>
      </RouterContextProvider>,
    );

    const user = userEvent.setup();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Open in new window" })).toBeVisible(),
    );
    await user.click(screen.getByRole("button", { name: "Open in new window" }));
    await user.click(screen.getByRole("button", { name: "Push task-b" }));
    await user.click(screen.getByRole("button", { name: "Back" }));
    await act(async () => {
      nativeWindow.resolve(undefined);
      await nativeWindow.promise;
    });

    expect(screen.getByTestId("current-sidebar-task")).toHaveTextContent("task-a");
    expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open");
  });
});

function ShellScenario({ mounts }: Readonly<{ mounts: string[] }>) {
  const sidebar = useSidebar();
  const destinations = useMemo(
    () => ({
      root: customDestination("root", mounts),
      child: customDestination("child", mounts),
    }),
    [mounts],
  );

  useEffect(() => {
    void sidebar.openSidebar(destinations.root);
  }, [destinations.root, sidebar.openSidebar]);

  return (
    <>
      <button onClick={() => { sidebar.pushSidebar(destinations.child); }} type="button">
        Push
      </button>
      <SidebarHost />
    </>
  );
}

function customDestination(name: string, mounts: string[]): SidebarDestination {
  return {
    kind: "custom",
    content: <MountProbe mounts={mounts} name={name} />,
    title: name,
  };
}

function MountProbe({ mounts, name }: Readonly<{ mounts: string[]; name: string }>) {
  useEffect(() => {
    mounts.push(name);
  }, [mounts, name]);
  return <div data-testid={`sidebar-content-${name}`}>{name}</div>;
}

function TaskShellScenario() {
  const sidebar = useSidebar();
  const activeTaskID =
    sidebar.activeDestination?.kind === "taskDetail" ? sidebar.activeDestination.taskID : null;
  useEffect(() => {
    void sidebar.openSidebar({ kind: "taskDetail", projectID: "project-1", taskID: "task-a" });
  }, [sidebar.openSidebar]);
  return (
    <>
      <div data-testid="current-sidebar-task">{activeTaskID}</div>
      <button
        onClick={() => {
          sidebar.pushSidebar({ kind: "taskDetail", projectID: "project-1", taskID: "task-b" });
        }}
        type="button"
      >
        Push task-b
      </button>
      <SidebarHost />
    </>
  );
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

function taskRoutes() {
  return [
    {
      method: "workflow.project.label.list",
      result: { catalog: { project_id: "project-1", labels: [] } },
    },
    {
      method: "workflow.task.get",
      handler: (params: unknown) => {
        const taskID = z.object({ task_id: z.string() }).parse(params).task_id;
        return {
          ...taskDetailResponse,
          task: {
            ...taskDetailResponse.task,
            summary: { ...taskDetailResponse.task.summary, id: taskID },
          },
        };
      },
    },
    { method: "workflow.task.attention.list", result: emptyTaskAttentionResponse },
    { method: "workflow.task.activity.list", result: activityResponse },
    { method: "workflow.task.comment.list", result: { comments: [], next_offset: null } },
  ] as const;
}
