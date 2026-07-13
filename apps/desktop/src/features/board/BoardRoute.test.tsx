import { act, render, screen, waitFor } from "@testing-library/react";

import { App } from "../../App";
import { appI18n, initializeI18n } from "../../i18n/setup";
import { createTestServices, startupRoutes } from "../../testSupport/appServices";

void initializeI18n();

describe("BoardRoute live refresh", () => {
  it("refreshes a board without a selected workflow after a workflow-link event", async () => {
    window.history.pushState(null, "", "/projects/project-1");
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.board.get",
        result: boardWithoutWorkflowResponse,
      },
    ]);

    render(<App services={services} />);

    await waitFor(() => {
      expect(boardCalls(services).length).toBe(1);
      expect(services.transport.subscriptions).toContainEqual({
        method: "workflow.subscribeProject",
        params: { project_id: "project-1" },
      });
    });
    expect(
      await screen.findByRole("button", { name: appI18n.t("workflowLibrary.linkWorkflow") }),
    ).toBeEnabled();
    expect(
      screen.getByRole("button", { name: appI18n.t("workflowLibrary.createWorkflow") }),
    ).toBeEnabled();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: appI18n.t("board.newTask") }),
    ).not.toBeInTheDocument();

    act(() => {
      services.transport.emit("workflow.event", {
        event: {
          action: "linked",
          changed_ids: ["workflow-link-1"],
          resource: "workflow_link",
          workflow_id: "workflow-1",
        },
      });
    });

    await waitFor(() => {
      expect(boardCalls(services).length).toBe(2);
    });
    expect(
      services.transport.calls.filter((call) => call.method === "workflow.board.nodeCards.list"),
    ).toEqual([]);
  });

  it("mounts board content only when the server selects a workflow", async () => {
    window.history.pushState(null, "", "/projects/project-1");
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.board.get",
        result: boardWithWorkflowResponse,
      },
    ]);

    render(<App services={services} />);

    expect(await screen.findByRole("list")).toBeInTheDocument();
    expect(screen.getByRole("navigation")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: appI18n.t("board.newTask") }),
    ).toBeEnabled();
    expect(
      screen.queryByRole("button", { name: appI18n.t("workflowLibrary.createWorkflow") }),
    ).not.toBeInTheDocument();
  });
});

function boardCalls(services: ReturnType<typeof createTestServices>) {
  return services.transport.calls.filter((call) => call.method === "workflow.board.get");
}

const boardWithoutWorkflowResponse = {
  board: {
    project_id: "project-1",
    project: {
      project_key: "PRO",
      display_name: "Project",
      default_workspace_id: "workspace-1",
      attached_workspace_count: 1,
    },
    workflows: [],
    groups: [],
    columns: [],
    cards: [],
    done_preview: [],
    next_page_token: "",
    generated_at_unix_ms: 1,
  },
};

const selectedWorkflow = {
  workflow_id: "workflow-1",
  display_name: "Workflow",
  description: "",
  version: 1,
  is_project_default: true,
  valid_for_task_creation: true,
  validation_errors: [],
};

const boardWithWorkflowResponse = {
  board: {
    ...boardWithoutWorkflowResponse.board,
    selected_workflow: selectedWorkflow,
    workflows: [selectedWorkflow],
  },
};
