import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterContextProvider,
} from "@tanstack/react-router";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, useState } from "react";
import { z } from "zod";

import { useSidebar } from "@/app-facade";
import { TestAppProviders, createTestServices } from "@/test-support/app-services";
import { activityResponse, emptyTaskAttentionResponse, taskDetailResponse } from "@/test-support/task-detail";
import { SidebarDestinationView } from "./sidebarDestinations";
import { SidebarProvider } from "./sidebarProvider";

describe("sidebar Task Detail navigation", () => {
  it("restores dirty title and body drafts after dependency Push and Back", async () => {
    const taskOne = taskWithDependency("task-1", "Resolve blocker", "Need operator input", "task-2");
    const taskTwo = taskWithDependency("task-2", "Prepare", "Preparation", undefined);
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: { catalog: { project_id: "project-1", labels: [] } },
      },
      {
        method: "workflow.task.get",
        handler: (params) => {
          const taskID = z.object({ task_id: z.string() }).parse(params).task_id;
          return taskID === "task-2" ? taskTwo : taskOne;
        },
      },
      { method: "workflow.task.attention.list", result: emptyTaskAttentionResponse },
      { method: "workflow.task.activity.list", result: activityResponse },
      { method: "workflow.task.comment.list", result: { comments: [], next_offset: null } },
    ]);
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/tasks/task-1"] }),
      routeTree: createRootRoute(),
    });

    render(
      <RouterContextProvider router={router}>
        <TestAppProviders services={services}>
          <SidebarProvider>
            <SidebarScenario />
          </SidebarProvider>
        </TestAppProviders>
      </RouterContextProvider>,
    );

    const user = userEvent.setup();
    await waitFor(() => {
      expect(getTaskTitleInput()).toHaveValue("Resolve blocker");
    });
    await user.clear(getTaskTitleInput());
    await user.type(getTaskTitleInput(), "Drafted title");
    await user.click(getTaskDescriptionInput());
    const description = getTaskDescriptionInput();
    await user.clear(description);
    await user.type(description, "Drafted body");
    await user.type(getTaskCommentInput(), "Drafted comment");
    await user.click(getTaskTab("task-tab-activity"));
    const scrollStack = screen.getByTestId("task-detail-island-stack");
    scrollStack.scrollTop = 120;
    fireEvent.scroll(scrollStack);
    await user.click(await screen.findByTestId("dependency-row-task-2"));
    await waitFor(() => expect(getTaskTitleInput()).toHaveValue("Prepare"));

    await user.click(screen.getByTestId("sidebar-back"));
    await waitFor(() => {
      expect(getTaskTitleInput()).toHaveValue("Drafted title");
      expect(getTaskDescriptionInput()).toHaveTextContent("Drafted body");
    });
    const restoredStack = screen.getByTestId("task-detail-island-stack");
    expect(restoredStack).toHaveProperty("scrollTop", 120);
    restoredStack.scrollTop = 0;
    fireEvent.scroll(restoredStack);
    await waitFor(() => {
      expect(getTaskTab("task-tab-activity")).toHaveAttribute("aria-selected", "true");
    });
    await user.click(getTaskTab("task-tab-comments"));
    expect(getTaskCommentInput()).toHaveValue("Drafted comment");

    const restoredScrollStack = screen.getByTestId("task-detail-island-stack");
    restoredScrollStack.scrollTop = 40;
    fireEvent.scroll(restoredScrollStack);
    await waitFor(() => {
      expect(services.transport.subscriptions).toContainEqual({
        method: "workflow.subscribeProject",
        params: { project_id: "project-1" },
      });
    });
    services.transport.connection.set("disconnected", "offline");
    await waitFor(() => {
      expect(services.transport.subscriptions).not.toContainEqual({
        method: "workflow.subscribeProject",
        params: { project_id: "project-1" },
      });
    });
    services.transport.connection.set("connected");
    await waitFor(() => {
      expect(services.transport.subscriptions).toContainEqual({
        method: "workflow.subscribeProject",
        params: { project_id: "project-1" },
      });
    });
    services.transport.open("workflow.subscribeProject");
    await waitFor(() => {
      expect(screen.getByTestId("task-detail-island-stack")).toHaveProperty("scrollTop", 40);
    });
  }, 15000);

  it("keeps one rendered Task Detail and one live project subscription across cycles", async () => {
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: { catalog: { project_id: "project-1", labels: [] } },
      },
      {
        method: "workflow.task.get",
        handler: (params) => {
          const taskID = z.object({ task_id: z.string() }).parse(params).task_id;
          return taskWithDependency(taskID, taskID, "Body", undefined);
        },
      },
      { method: "workflow.task.attention.list", result: emptyTaskAttentionResponse },
      { method: "workflow.task.activity.list", result: activityResponse },
      { method: "workflow.task.comment.list", result: { comments: [], next_offset: null } },
    ]);
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/tasks/task-0"] }),
      routeTree: createRootRoute(),
    });
    render(
      <RouterContextProvider router={router}>
        <TestAppProviders services={services}>
          <SidebarProvider>
            <SidebarTraversalScenario />
          </SidebarProvider>
        </TestAppProviders>
      </RouterContextProvider>,
    );

    const user = userEvent.setup();
    await waitFor(() => {
      expect(getTaskTitleInput()).toHaveValue("task-0");
    });
    const initialActiveSubscriptionCount = services.transport.subscriptions.length;
    expect(initialActiveSubscriptionCount).toBeGreaterThan(0);
    for (let cycle = 0; cycle < 6; cycle += 1) {
      await user.click(screen.getByTestId("sidebar-cycle"));
      await waitFor(() => {
        expect(screen.getAllByTestId("task-detail-island-stack")).toHaveLength(1);
      });
    }
    await waitFor(() => {
      expect(services.transport.subscriptions).toHaveLength(initialActiveSubscriptionCount);
    });
  }, 15_000);
});

function SidebarScenario() {
  const { activeDestination, activeSnapshot, backSidebar, closeSidebar, openSidebar, resolveSidebar } =
    useSidebar();
  useEffect(() => {
    void openSidebar({ kind: "taskDetail", taskID: "task-1" });
  }, [openSidebar]);
  if (activeDestination === null) {
    return null;
  }
  return (
    <>
      <button data-testid="sidebar-back" onClick={backSidebar} type="button">
        Back
      </button>
      <SidebarDestinationView
        activeSnapshot={activeSnapshot}
        closeSidebar={closeSidebar}
        destination={activeDestination}
        resolveSidebar={resolveSidebar}
      />
    </>
  );
}

function SidebarTraversalScenario() {
  const {
    activeDestination,
    activeSnapshot,
    backSidebar,
    canGoBack,
    closeSidebar,
    openSidebar,
    pushSidebar,
    resolveSidebar,
  } = useSidebar();
  const [nextTaskNumber, setNextTaskNumber] = useState(1);
  useEffect(() => {
    void openSidebar({ kind: "taskDetail", taskID: "task-0" });
  }, [openSidebar]);
  if (activeDestination === null) {
    return null;
  }
  return (
    <>
      <button
        onClick={() => {
          if (canGoBack) {
            backSidebar();
            return;
          }
          const taskID = `task-${nextTaskNumber.toString()}`;
          setNextTaskNumber((current) => current + 1);
          pushSidebar({ kind: "taskDetail", taskID });
        }}
        type="button"
        data-testid="sidebar-cycle"
      >
        Cycle
      </button>
      <SidebarDestinationView
        activeSnapshot={activeSnapshot}
        closeSidebar={closeSidebar}
        destination={activeDestination}
        resolveSidebar={resolveSidebar}
      />
    </>
  );
}

function getTaskTitleInput() {
  return within(screen.getByTestId("task-detail-title-island")).getByRole("textbox");
}

function getTaskDescriptionInput() {
  return within(screen.getByTestId("task-description-input-frame")).getByRole("textbox");
}

function getTaskCommentInput() {
  return within(screen.getByTestId("task-comment-input-frame")).getByRole("textbox");
}

function getTaskTab(testID: string) {
  return within(screen.getByTestId(testID)).getByRole("tab");
}

function taskWithDependency(
  taskID: string,
  title: string,
  body: string,
  dependencyTaskID: string | undefined,
) {
  const base = taskDetailResponse.task;
  const blockedBy = base.dependencies.directions[0];
  const blocks = base.dependencies.directions[1];
  if (blockedBy === undefined || blocks === undefined) {
    throw new Error("Task fixture is missing dependency directions.");
  }
  const dependencyItems =
    dependencyTaskID === undefined
      ? []
      : [
          {
            task_id: dependencyTaskID,
            short_id: `T-${dependencyTaskID}`,
            title: `Dependency ${dependencyTaskID}`,
            workflow_id: base.workflow.workflow_id,
            status: {
              kind: "backlog",
              native_state: "active",
              node_ids: [],
              attention_types: [],
            },
            satisfaction: "unsatisfied",
          },
        ];
  return {
    task: {
      ...base,
      summary: {
        ...base.summary,
        id: taskID,
        short_id: `T-${taskID}`,
        title,
      },
      body,
      dependencies: {
        ...base.dependencies,
        blocker_count: dependencyTaskID === undefined ? 0 : 1,
        unsatisfied_blocker_count: dependencyTaskID === undefined ? 0 : 1,
        directions: [
          {
            ...blockedBy,
            items: dependencyItems,
            total_count: dependencyTaskID === undefined ? 0 : 1,
            unsatisfied_count: dependencyTaskID === undefined ? 0 : 1,
          },
          blocks,
        ],
      },
    },
  };
}
