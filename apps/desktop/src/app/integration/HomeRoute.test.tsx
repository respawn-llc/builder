import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { vi } from "vitest";
import { z } from "zod";

import type { JsonValue } from "@/api";
import { App } from "../startup/App";
import { createTestServices, startupRoutes } from "@/test-support/app-services";
import { installTestStorage } from "@/test-support/storage";

const jsonRecordSchema = z.record(z.string(), z.unknown());

describe("HomeRoute", () => {
  beforeEach(() => {
    installTestStorage("localStorage");
    installTestStorage("sessionStorage");
    window.history.pushState(null, "", "/");
  });

  it("reloads project pages from the first page after leaving and revisiting Home", async () => {
    const services = mountHomeServices(createHomeRevisitServices());

    fireEvent.click(await screen.findByRole("button", { name: "Alpha /tmp/project-alpha" }));
    await waitFor(() => {
      expect(services.transport.calls).toContainEqual({
        method: "project.home.list",
        params: { page_size: 40, page_token: "next" },
      });
    });
    await waitFor(() => {
      expect(window.location.pathname).toBe("/projects/project-alpha");
    });

    fireEvent.click(screen.getByLabelText("Home"));

    await screen.findByRole("button", { name: "Beta /tmp/project-beta" });
    await waitFor(() => {
      const projectCalls = services.transport.calls.filter((call) => call.method === "project.home.list");
      expect(projectCalls.at(-1)).toEqual({
        method: "project.home.list",
        params: { page_size: 40, page_token: "" },
      });
    });
  });

  it("reloads project pages from the first page after browser back returns Home", async () => {
    const services = mountHomeServices(createHomeRevisitServices());

    fireEvent.click(await screen.findByRole("button", { name: "Alpha /tmp/project-alpha" }));
    await waitFor(() => {
      expect(window.location.pathname).toBe("/projects/project-alpha");
    });

    fireEvent.click(screen.getByLabelText("Back"));

    await waitFor(() => {
      expect(window.location.pathname).toBe("/");
      const projectCalls = services.transport.calls.filter((call) => call.method === "project.home.list");
      expect(projectCalls.at(-1)).toEqual({
        method: "project.home.list",
        params: { page_size: 40, page_token: "" },
      });
    });
  });

  it("shows project card workspace paths relative to the user's home directory", async () => {
    mountHome({
      homePath: "/Users/nek",
      routes: [
        {
          method: "project.home.list",
          result: projectPage([projectSummary("project-kent", "Kent", 10, "/Users/nek/Developer/kent")], ""),
        },
      ],
    });

    const projectCard = await screen.findByRole("button", { name: "Kent ~/Developer/kent" });
    expect(projectCard).toBeInTheDocument();
    expect(projectCard).toHaveAttribute("title", "/Users/nek/Developer/kent");
  });

  it("keeps Inbox on the right while Workflows replaces Projects in the left tabbed pane", async () => {
    mountHome({
      routes: [
        {
          method: "workflow.list",
          result: {
            workflows: [
              {
                id: "workflow-delivery",
                name: "Delivery",
                description: "Ship changes",
                version: 1,
              },
            ],
            next_page_token: "",
          },
        },
      ],
    });

    expect(await screen.findByRole("tab", { name: "Projects" })).toHaveAttribute("aria-selected", "true");
    expect(await screen.findByRole("heading", { name: "Inbox" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "Workflows" }));

    expect(screen.getByRole("tab", { name: "Workflows" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("heading", { name: "Inbox" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/");
  });

  it("renders Inbox cards without kind chips in the header", async () => {
    mountHome({
      routes: [attentionListRoute(attentionItem({ kind: "question", message: "Pick answer" }))],
    });

    const row = await screen.findByTestId("attention-row");
    expect(within(row).getByText("T-1")).toBeInTheDocument();
    expect(within(row).queryByText("question")).not.toBeInTheDocument();
  });

  it("opens Inbox task cards in the Home task sidebar without navigating away", async () => {
    const services = mountHome({
      routes: [
        attentionListRoute(attentionItem({ kind: "question", message: "Pick answer" })),
        { method: "workflow.task.get", result: taskDetailResponse },
        { method: "workflow.task.attention.list", result: taskAttentionResponse([]) },
        { method: "workflow.task.activity.list", result: emptyActivityResponse },
      ],
    });

    fireEvent.click(await screen.findByTestId("attention-row"));

    expect(window.location.pathname).toBe("/");
    expect(window.location.search).toBe("");
    const sidebar = await screen.findByRole("complementary", { name: "Task" });
    expect(await within(sidebar).findByDisplayValue("Resolve blocker")).toBeInTheDocument();
    expect(services.transport.calls.some((call) => call.method === "workflow.board.get")).toBe(false);
  });

  it("renders core task detail before deferred attention and focuses the requested question when it arrives", async () => {
    const scrollIntoView = vi.fn();
    const attention = deferred<ReturnType<typeof taskAttentionResponse>>();
    const originalScrollIntoView = Object.getOwnPropertyDescriptor(Element.prototype, "scrollIntoView");
    Object.defineProperty(Element.prototype, "scrollIntoView", {
      configurable: true,
      value: scrollIntoView,
    });
    try {
      mountHome({
        routes: [
          attentionListRoute(attentionItem({ kind: "question", message: "Pick answer" })),
          { method: "workflow.task.get", result: taskDetailResponseWithQuestion },
          {
            method: "workflow.task.attention.list",
            handler: async () => attention.promise,
          },
          { method: "workflow.task.activity.list", result: emptyActivityResponse },
          { method: "ask.listPendingBySession", result: { Asks: [] } },
        ],
      });

      fireEvent.click(await screen.findByTestId("attention-row"));

      const sidebar = await screen.findByRole("complementary", { name: "Task" });
      expect(await within(sidebar).findByDisplayValue("Resolve blocker")).toBeInTheDocument();
      expect(within(sidebar).queryByRole("region", { name: "Question" })).not.toBeInTheDocument();

      await act(async () => {
        attention.resolve(
          taskAttentionResponse([attentionItem({ kind: "question", message: "Pick answer" })]),
        );
        await attention.promise;
      });

      expect(await within(sidebar).findByRole("region", { name: "Question" })).toBeInTheDocument();
      await waitFor(() => {
        expect(scrollIntoView).toHaveBeenCalledWith({ block: "start", behavior: "auto" });
      });
      expect(window.location.pathname).toBe("/");
    } finally {
      if (originalScrollIntoView !== undefined) {
        Object.defineProperty(Element.prototype, "scrollIntoView", originalScrollIntoView);
      } else {
        Reflect.deleteProperty(Element.prototype, "scrollIntoView");
      }
    }
  });

  it("keeps core task detail rendered when attention fails and retries the local read", async () => {
    let attentionAvailable = false;
    const services = mountHome({
      routes: [
        attentionListRoute(attentionItem({ kind: "question", message: "Pick answer" })),
        { method: "workflow.task.get", result: taskDetailResponseWithQuestion },
        {
          method: "workflow.task.attention.list",
          handler: () => {
            if (!attentionAvailable) {
              throw new Error("Task attention is temporarily unavailable.");
            }
            return taskAttentionResponse([attentionItem({ kind: "question", message: "Pick answer" })]);
          },
        },
        { method: "workflow.task.activity.list", result: emptyActivityResponse },
        { method: "ask.listPendingBySession", result: { Asks: [] } },
      ],
    });

    fireEvent.click(await screen.findByTestId("attention-row"));

    const sidebar = await screen.findByRole("complementary", { name: "Task" });
    expect(await within(sidebar).findByDisplayValue("Resolve blocker")).toBeInTheDocument();
    const retry = await within(sidebar).findByRole("button", { name: "Try again" });
    const attentionCallsBeforeRetry = services.transport.calls.filter(
      (call) => call.method === "workflow.task.attention.list",
    ).length;
    attentionAvailable = true;
    fireEvent.click(retry);

    expect(await within(sidebar).findByRole("region", { name: "Question" })).toBeInTheDocument();
    expect(within(sidebar).getByDisplayValue("Resolve blocker")).toBeInTheDocument();
    expect(services.transport.calls.filter((call) => call.method === "workflow.task.get")).toHaveLength(1);
    expect(
      services.transport.calls.filter((call) => call.method === "workflow.task.attention.list").length,
    ).toBeGreaterThan(attentionCallsBeforeRetry);
  });

  it("opens workflow-only Inbox cards in the workflow editor", async () => {
    mountHome({
      routes: [
        attentionListRoute(
          attentionItem({
            kind: "validation_blocker",
            message: "Workflow invalid",
            taskID: "",
            taskShortID: "",
            taskTitle: "",
          }),
        ),
        { method: "workflow.listProjectLinks", result: workflowProjectLinksResponse },
        { method: "workflow.get", result: workflowDefinitionResponse },
        { method: "workflow.validate", result: workflowValidationResponse },
      ],
    });

    fireEvent.click(await screen.findByTestId("attention-row"));

    await waitFor(() => {
      expect(window.location.pathname).toBe("/workflows/workflow-1/editor");
      expect(new URLSearchParams(window.location.search).get("projectId")).toBe("project-1");
    });
  });

  it("opens workflow creation from the Workflows tab plus action", async () => {
    mountHome();

    fireEvent.click(await screen.findByRole("button", { name: "Create workflow" }));

    const sidebar = await screen.findByRole("complementary", { name: "Create workflow" });
    expect(within(sidebar).getByLabelText("Workflow name")).toBeInTheDocument();
  });

  it("disables workflow creation from the Workflows tab while disconnected", async () => {
    mountHome({ disconnected: true });

    expect(await screen.findByRole("button", { name: "Create workflow" })).toBeDisabled();
  });
});

type HomeRoutes = Parameters<typeof createTestServices>[0];
type HomeOptions = Readonly<{
  disconnected?: boolean;
  homePath?: string;
  routes?: HomeRoutes;
}>;

function mountHomeServices(services: ReturnType<typeof createTestServices>) {
  render(<App services={services} />);
  return services;
}

function mountHome(options: HomeOptions = {}) {
  const environment = options.homePath === undefined ? undefined : { homePath: options.homePath };
  const services = createTestServices([...startupRoutes, ...(options.routes ?? [])], undefined, environment);
  if (options.disconnected === true) {
    services.transport.connection.set("disconnected", "offline");
  }
  return mountHomeServices(services);
}

function attentionListRoute(...items: readonly ReturnType<typeof attentionItem>[]) {
  return {
    method: "workflow.attention.list",
    result: {
      generated_at_unix_ms: 1,
      items,
      next_page_token: "",
    },
  } as const;
}

function taskAttentionResponse(items: readonly ReturnType<typeof attentionItem>[]) {
  return {
    generated_at_unix_ms: 1,
    items,
  };
}

function deferred<T>(): Readonly<{ promise: Promise<T>; resolve(value: T): void }> {
  let resolvePromise: ((value: T) => void) | undefined;
  const promise = new Promise<T>((resolve) => {
    resolvePromise = resolve;
  });
  return {
    promise,
    resolve(value: T): void {
      resolvePromise?.(value);
    },
  };
}

function createHomeRevisitServices() {
  return createTestServices([
    ...startupRoutes,
    {
      method: "project.home.list",
      handler: (params: JsonValue, callIndex: number) => {
        if (isPageToken(params, "next")) {
          return projectPage([projectSummary("project-beta", "Beta", 20)], "");
        }
        if (callIndex >= 2) {
          return projectPage(
            [projectSummary("project-beta", "Beta", 30), projectSummary("project-alpha", "Alpha", 10)],
            "",
          );
        }
        return projectPage([projectSummary("project-alpha", "Alpha", 10)], "next");
      },
    },
    { method: "workflow.board.get", result: boardResponse },
  ]);
}

function isPageToken(params: JsonValue, token: string): boolean {
  return isJsonRecord(params) && params.page_token === token;
}

function isJsonRecord(value: JsonValue): value is Readonly<Record<string, JsonValue>> {
  return jsonRecordSchema.safeParse(value).success && !Array.isArray(value);
}

function projectPage(projects: readonly ReturnType<typeof projectSummary>[], nextPageToken: string) {
  return {
    projects,
    next_page_token: nextPageToken,
    generated_at_unix_ms: 1,
  };
}

function projectSummary(
  projectID: string,
  name: string,
  updatedAtUnixMs: number,
  rootPath = `/tmp/${projectID}`,
) {
  return {
    project_id: projectID,
    project_key: name.slice(0, 3).toUpperCase(),
    display_name: name,
    primary_workspace: workspaceSummary(`workspace-${projectID}`, rootPath, updatedAtUnixMs),
    default_workflow_id: "workflow-1",
    default_workflow_name: "Default",
    default_workflow_valid: true,
    updated_at_unix_ms: updatedAtUnixMs,
    task_count: 0,
    attention_count: 0,
    workflow_count: 1,
  };
}

function workspaceSummary(workspaceID: string, rootPath: string, updatedAtUnixMs: number) {
  return {
    workspace_id: workspaceID,
    display_name: workspaceID,
    root_path: rootPath,
    availability: "available",
    is_primary: true,
    updated_at_unix_ms: updatedAtUnixMs,
  };
}

function attentionItem({
  kind,
  message,
  taskID = "task-1",
  taskShortID = "T-1",
  taskTitle = "Resolve blocker",
}: Readonly<{
  kind: string;
  message: string;
  taskID?: string;
  taskShortID?: string;
  taskTitle?: string;
}>) {
  return {
    ask_id: kind === "question" ? "ask-1" : "",
    id: `attention-${kind}`,
    kind,
    message,
    occurred_at_unix_ms: 1,
    project_id: "project-1",
    run_id: kind === "question" ? "run-1" : "",
    session_id: kind === "question" ? "session-1" : "",
    task_id: taskID,
    task_short_id: taskShortID,
    task_title: taskTitle,
    task_transition_id: "",
    workflow_id: "workflow-1",
  };
}

const workflow = {
  workflow_id: "workflow-1",
  display_name: "Default",
  description: "",
  version: 1,
  is_project_default: true,
  valid_for_task_creation: true,
  validation_errors: [],
};

const workspace = {
  workspace_id: "workspace-1",
  display_name: "Main",
  root_path: "/tmp/project",
  availability: "available",
  is_primary: true,
  updated_at_unix_ms: 1,
};

const boardResponse = {
  board: {
    project_id: "project-1",
    project: {
      project_key: "PRO",
      display_name: "Project",
      default_workspace_id: "workspace-1",
      attached_workspace_count: 1,
    },
    selected_workflow: workflow,
    workflows: [workflow],
    groups: [],
    columns: [],
    cards: [],
    done_preview: [],
    next_page_token: "",
    generated_at_unix_ms: 1,
  },
};

const taskDetailResponse = {
  task: {
    summary: {
      id: "task-1",
      project_id: "project-1",
      workflow_id: "workflow-1",
      short_id: "T-1",
      title: "Resolve blocker",
      created_at_unix_ms: 1,
      updated_at_unix_ms: 2,
      done: false,
      canceled_at_unix_ms: null,
    },
    project: { display_name: "Project" },
    workflow,
    body: "Need operator input",
    source_workspace: workspace,
    status: {
      kind: "waiting_question",
      native_state: "running",
      node_ids: ["node-1"],
      run_ids: ["run-1"],
      attention_types: ["question"],
    },
    actions: {
      can_start: false,
      can_interrupt: true,
      can_resume: false,
      can_cancel: true,
      manual_move_target_node_ids: [],
    },
    label_ids: [],
    attention_count: 0,
    runs: [],
    transitions: [],
    comments: [],
  },
};

const taskDetailResponseWithQuestion = {
  task: {
    ...taskDetailResponse.task,
    attention_count: 1,
  },
};

const emptyActivityResponse = {
  generated_at_unix_ms: 1,
  items: [],
  next_page_token: "",
};

const workflowProjectLinksResponse = {
  links: [
    {
      default: true,
      id: "link-1",
      project_id: "project-1",
      workflow_id: "workflow-1",
    },
  ],
};

const workflowDefinitionResponse = {
  definition: {
    workflow: {
      id: "workflow-1",
      name: "Default",
      description: "",
      version: 1,
    },
    node_groups: [],
    nodes: [],
    transition_groups: [],
    edges: [],
    derived_wiring: {
      diagnostics: [],
      edges: [],
      nodes: [],
      transition_groups: [],
    },
  },
};

const workflowValidationResponse = {
  errors: [],
  valid: true,
};
