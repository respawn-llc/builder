import { createMemoryHistory, createRootRoute, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect } from "react";
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
    let restoreTaskOneFromServer = false;
    let restoredTaskOneRead = false;
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: { catalog: { project_id: "project-1", labels: [] } },
      },
      {
        method: "workflow.task.get",
        handler: (params) => {
          const taskID = z.object({ task_id: z.string() }).parse(params).task_id;
          if (taskID === "task-1" && restoreTaskOneFromServer) {
            restoredTaskOneRead = true;
            return taskWithDependency("task-1", "Fresh server title", "Fresh server body", "task-2");
          }
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
      expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Resolve blocker");
    });
    expect(screen.getAllByTestId("task-detail-island-stack")).toHaveLength(1);
    await user.clear(screen.getByRole("textbox", { name: "Title" }));
    await user.type(screen.getByRole("textbox", { name: "Title" }), "Drafted title");
    await user.click(screen.getByRole("textbox", { name: "Description" }));
    const description = screen.getByRole("textbox", { name: "Description" });
    await user.clear(description);
    await user.type(description, "Drafted body");
    await user.type(screen.getByRole("textbox", { name: "Add comment" }), "Drafted comment");
    screen.getByTestId("task-detail-island-stack").scrollTop = 64;
    await user.click(await screen.findByTestId("dependency-row-task-2"));
    await waitFor(() => {
      expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Prepare");
    });
    expect(screen.getAllByTestId("task-detail-island-stack")).toHaveLength(1);

    restoreTaskOneFromServer = true;
    await user.click(screen.getByRole("button", { name: "Back" }));
    await waitFor(() => {
      expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Drafted title");
      expect(screen.getByRole("textbox", { name: "Description" })).toHaveTextContent("Drafted body");
      expect(screen.getByRole("textbox", { name: "Add comment" })).toHaveValue("Drafted comment");
    });
    expect(screen.getByTestId("task-detail-island-stack")).toHaveProperty("scrollTop", 64);
    expect(restoredTaskOneRead).toBe(true);
    expect(screen.getAllByTestId("task-detail-island-stack")).toHaveLength(1);
  });

  it("restores the selected Activity tab after dependency Push and Back", async () => {
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
    await waitFor(() => expect(screen.getByRole("tab", { name: "Activity" })).toBeVisible());
    await user.click(screen.getByRole("tab", { name: "Activity" }));
    await user.click(await screen.findByTestId("dependency-row-task-2"));
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Prepare"));
    await user.click(screen.getByRole("button", { name: "Back" }));
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Activity" })).toHaveAttribute("aria-selected", "true"),
    );
  });

  it("returns to an earlier related Task and truncates the later branch", async () => {
    const taskOne = taskWithDependency("task-1", "First", "First body", "task-2");
    const taskTwo = taskWithDependency("task-2", "Second", "Second body", "task-1");
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
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("First"));
    await user.click(await screen.findByTestId("dependency-row-task-2"));
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Second"));
    await user.click(await screen.findByTestId("dependency-row-task-1"));
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("First"));
    expect(screen.getByTestId("sidebar-can-go-back")).toHaveTextContent("false");
  });
});

function SidebarScenario() {
  const {
    activeDestination,
    backSidebar,
    canGoBack,
    closeSidebar,
    openSidebar,
    resolveSidebar,
  } = useSidebar();
  useEffect(() => {
    void openSidebar({ kind: "taskDetail", projectID: "project-1", taskID: "task-1" });
  }, [openSidebar]);
  if (activeDestination === null) {
    return null;
  }
  return (
    <>
      <button onClick={backSidebar} type="button">
        Back
      </button>
      <output data-testid="sidebar-can-go-back">{canGoBack.toString()}</output>
      <SidebarDestinationView
        closeSidebar={closeSidebar}
        destination={activeDestination}
        resolveSidebar={resolveSidebar}
      />
    </>
  );
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
            short_id: "T-2",
            title: "Prepare",
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
        short_id: taskID === "task-1" ? "T-1" : "T-2",
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
