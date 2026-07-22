import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, vi } from "vitest";

import { RpcError } from "@/api";
import { createTestServices, TestAppProviders } from "@/test-support/app-services";
import { appI18n } from "@/test-support/i18n";
import { installTestStorage } from "@/test-support/storage";
import * as uiModule from "@/ui";
import { NewTaskFallbackDialog, NewTaskForm } from "./NewTaskDialog";

describe("NewTaskForm labels", () => {
  afterEach(() => {
    installTestStorage("localStorage");
    vi.restoreAllMocks();
  });

  it("surfaces a persisted label-filter write failure", async () => {
    const showStatusToast = vi.spyOn(uiModule, "showStatusToast");
    const storage = installTestStorage("localStorage");
    storage.setItem(
      'desktop.projectLabelFilter.v1:["browser-endpoint","ws://127.0.0.1:53082/rpc","project-1"]',
      JSON.stringify({
        version: 1,
        kind: "named",
        mode: "any",
        labelIDs: [priorityID],
      }),
    );
    vi.spyOn(storage, "setItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });

    renderNewTask();

    await waitFor(() => {
      expect(showStatusToast).toHaveBeenCalledWith(
        expect.objectContaining({
          id: "new-task-label-load-error",
          tone: "danger",
        }),
      );
    });
  });

  it("places Labels after Details and before Source workspace", async () => {
    const user = userEvent.setup();
    renderNewTask({
      method: "project.workspace.list",
      result: {
        project_id: "project-1",
        workspaces: [workspace("workspace-1", "Main", true), workspace("workspace-2", "Secondary", false)],
        default_workspace_id: "workspace-1",
        next_page_token: "",
      },
    });

    const title = await screen.findByRole("textbox", { name: appI18n.t("task.name") });
    await user.click(title);
    await user.tab();
    expect(screen.getByRole("textbox", { name: appI18n.t("task.body") })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: appI18n.t("labels.editAssignments") })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: appI18n.t("task.sourceWorkspace") })).toHaveFocus();
  });

  it("selects an existing label and submits it atomically with the task", async () => {
    const user = userEvent.setup();
    const { onSubmitted, services } = renderNewTask(
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityID, name: "Priority" }],
          },
        },
      },
      {
        method: "workflow.task.create",
        result: { task: { id: "task-1" } },
      },
    );
    await user.type(await screen.findByRole("textbox", { name: appI18n.t("task.name") }), "Ship labels");
    await user.type(
      screen.getByRole("textbox", { name: appI18n.t("task.body") }),
      "Create with one assignment.",
    );
    await user.click(screen.getByRole("button", { name: appI18n.t("labels.editAssignments") }));
    await user.click(await screen.findByRole("button", { name: "Priority" }));
    await user.click(screen.getByRole("button", { name: appI18n.t("task.create") }));

    await waitFor(() => {
      expect(services.transport.calls).toContainEqual({
        method: "workflow.task.create",
        params: {
          project_id: "project-1",
          workflow_id: "workflow-1",
          title: "Ship labels",
          body: "Create with one assignment.",
          source_workspace_id: "workspace-1",
          label_ids: [priorityID],
        },
      });
    });
    expect(
      services.transport.calls.filter((call) => call.method === "workflow.task.labels.update"),
    ).toHaveLength(0);
    expect(onSubmitted).toHaveBeenCalledTimes(1);
  });

  it("creates a label through the shared chooser and selects it for submission", async () => {
    const user = userEvent.setup();
    const { services } = renderNewTask(
      {
        method: "workflow.project.label.list",
        handler: (_params, callIndex) => ({
          catalog: {
            project_id: "project-1",
            labels:
              callIndex === 0
                ? []
                : [
                    {
                      id: createdLabelID,
                      name: "Customer",
                    },
                  ],
          },
        }),
      },
      {
        method: "workflow.project.label.create",
        result: {
          label: {
            id: createdLabelID,
            name: "Customer",
          },
        },
      },
      {
        method: "workflow.task.create",
        result: { task: { id: "task-1" } },
      },
    );
    await user.type(await screen.findByRole("textbox", { name: appI18n.t("task.name") }), "Customer request");
    await user.click(screen.getByRole("button", { name: appI18n.t("labels.editAssignments") }));
    await user.type(await screen.findByRole("textbox", { name: appI18n.t("labels.search") }), "Customer");
    await user.click(
      screen.getByRole("button", {
        name: appI18n.t("labels.create", { name: "Customer" }),
      }),
    );
    await user.click(screen.getByRole("button", { name: appI18n.t("task.create") }));

    await waitFor(() => {
      expect(services.transport.calls).toContainEqual({
        method: "workflow.task.create",
        params: {
          project_id: "project-1",
          workflow_id: "workflow-1",
          title: "Customer request",
          body: "",
          source_workspace_id: "workspace-1",
          label_ids: [createdLabelID],
        },
      });
    });
  });

  it("keeps a created catalog label after the New Task form is canceled", async () => {
    const user = userEvent.setup();
    const services = createTestServices([
      {
        method: "project.workspace.list",
        result: workspaceList(),
      },
      {
        method: "workflow.project.label.list",
        handler: (_params, callIndex) => ({
          catalog: {
            project_id: "project-1",
            labels:
              callIndex === 0
                ? []
                : [
                    {
                      id: createdLabelID,
                      name: "Customer",
                    },
                  ],
          },
        }),
      },
      {
        method: "workflow.project.label.create",
        result: {
          label: {
            id: createdLabelID,
            name: "Customer",
          },
        },
      },
    ]);
    function CancelableDialog() {
      const [open, setOpen] = useState(true);
      return open ? (
        <NewTaskFallbackDialog
          boardQueryWorkflowID="workflow-1"
          onClose={() => {
            setOpen(false);
          }}
          projectID="project-1"
          workflowID="workflow-1"
        />
      ) : null;
    }
    render(
      <TestAppProviders services={services}>
        <CancelableDialog />
      </TestAppProviders>,
    );
    await user.click(await screen.findByRole("button", { name: appI18n.t("labels.editAssignments") }));
    await user.type(await screen.findByRole("textbox", { name: appI18n.t("labels.search") }), "Customer");
    await user.click(
      screen.getByRole("button", {
        name: appI18n.t("labels.create", { name: "Customer" }),
      }),
    );
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.create"),
      ).toHaveLength(1);
    });
    await user.click(screen.getByRole("button", { name: appI18n.t("app.close") }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    render(
      <TestAppProviders services={services}>
        <NewTaskForm
          boardQueryWorkflowID="workflow-1"
          onSubmitted={() => undefined}
          projectID="project-1"
          workflowID="workflow-1"
        />
      </TestAppProviders>,
    );
    await user.click(await screen.findByRole("button", { name: appI18n.t("labels.editAssignments") }));
    expect(await screen.findByRole("button", { name: "Customer" })).toBeVisible();
    expect(services.transport.calls.filter((call) => call.method === "workflow.task.create")).toHaveLength(0);
  });

  it("keeps catalog failure actionable without clearing New Task input", async () => {
    const user = userEvent.setup();
    const { services } = renderNewTask({
      method: "workflow.project.label.list",
      handler: (_params, callIndex) => {
        if (callIndex === 0) {
          throw new Error("catalog unavailable");
        }
        return {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityID, name: "Priority" }],
          },
        };
      },
    });
    const title = await screen.findByRole("textbox", { name: appI18n.t("task.name") });
    const body = screen.getByRole("textbox", { name: appI18n.t("task.body") });
    await user.type(title, "Keep this title");
    await user.type(body, "Keep this body");
    await user.click(screen.getByRole("button", { name: appI18n.t("labels.editAssignments") }));

    expect(await screen.findByText(appI18n.t("labels.loadFailed"))).toBeVisible();
    await user.click(screen.getByRole("button", { name: appI18n.t("app.retry") }));

    expect(await screen.findByRole("button", { name: "Priority" })).toBeVisible();
    expect(title).toHaveValue("Keep this title");
    expect(body).toHaveValue("Keep this body");
    expect(
      services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
    ).toHaveLength(2);
  });

  it("shows the shared chooser loading state without blocking New Task input", async () => {
    const user = userEvent.setup();
    const catalog = deferred<unknown>();
    renderNewTask({
      method: "workflow.project.label.list",
      handler: async () => catalog.promise,
    });
    const title = await screen.findByRole("textbox", { name: appI18n.t("task.name") });
    await user.type(title, "Editable while loading");
    await user.click(screen.getByRole("button", { name: appI18n.t("labels.editAssignments") }));

    expect(await screen.findByRole("status")).toBeVisible();
    expect(title).toHaveValue("Editable while loading");

    catalog.resolve({
      catalog: {
        project_id: "project-1",
        labels: [{ id: priorityID, name: "Priority" }],
      },
    });
    expect(await screen.findByRole("button", { name: "Priority" })).toBeVisible();
  });

  it("keeps selection available while creation is disabled at the catalog limit", async () => {
    const user = userEvent.setup();
    renderNewTask({
      method: "workflow.project.label.list",
      result: {
        catalog: {
          project_id: "project-1",
          labels: Array.from({ length: 100 }, (_, index) => ({
            id: labelID(index),
            name: `Label ${index.toString().padStart(3, "0")}`,
          })),
        },
      },
    });
    await user.click(await screen.findByRole("button", { name: appI18n.t("labels.editAssignments") }));
    await user.type(await screen.findByRole("textbox", { name: appI18n.t("labels.search") }), "Overflow");

    const create = screen.getByRole("button", {
      name: appI18n.t("labels.create", { name: "Overflow" }),
    });
    expect(create).toBeDisabled();
    expect(screen.getByText(appI18n.t("labels.catalogLimit"))).toBeVisible();

    await user.clear(screen.getByRole("textbox", { name: appI18n.t("labels.search") }));
    await user.click(await screen.findByRole("button", { name: "Label 000" }));
    expect(screen.getByRole("button", { name: appI18n.t("labels.editAssignments") })).toHaveTextContent(
      "Label 000",
    );
  });

  it("retains every form value and selected label after a deleted-label submit failure", async () => {
    const user = userEvent.setup();
    const failureMessage = "The selected label was deleted.";
    const { onSubmitted, services } = renderNewTask(
      {
        method: "project.workspace.list",
        result: {
          project_id: "project-1",
          workspaces: [workspace("workspace-1", "Main", true), workspace("workspace-2", "Secondary", false)],
          default_workspace_id: "workspace-1",
          next_page_token: "",
        },
      },
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityID, name: "Priority" }],
          },
        },
      },
      {
        method: "workflow.task.create",
        handler: (_params, callIndex) => {
          if (callIndex === 0) {
            throw new RpcError({
              code: -32031,
              data: {
                type: "workflow_label_error",
                reason: "label_not_found",
                label_id: priorityID,
              },
              message: failureMessage,
              method: "workflow.task.create",
            });
          }
          return { task: { id: "task-1" } };
        },
      },
    );
    const title = await screen.findByRole("textbox", { name: appI18n.t("task.name") });
    const body = screen.getByRole("textbox", { name: appI18n.t("task.body") });
    await user.type(title, "Keep this title");
    await user.type(body, "Keep this body");
    await user.click(screen.getByRole("button", { name: appI18n.t("labels.editAssignments") }));
    await user.click(await screen.findByRole("button", { name: "Priority" }));
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: appI18n.t("task.sourceWorkspace") }));
    await user.click(await screen.findByRole("menuitemradio", { name: "Secondary" }));
    await user.click(screen.getByRole("button", { name: appI18n.t("task.create") }));

    expect(await screen.findByText(failureMessage)).toBeVisible();
    expect(title).toHaveValue("Keep this title");
    expect(body).toHaveValue("Keep this body");
    expect(screen.getByRole("button", { name: appI18n.t("labels.editAssignments") })).toHaveTextContent(
      "Priority",
    );
    expect(screen.getByRole("button", { name: appI18n.t("task.sourceWorkspace") })).toHaveTextContent(
      "Secondary",
    );
    expect(onSubmitted).not.toHaveBeenCalled();
    expect(services.transport.calls.filter((call) => call.method === "workflow.task.create")).toHaveLength(1);

    await user.click(screen.getByRole("button", { name: appI18n.t("task.create") }));
    await waitFor(() => {
      expect(onSubmitted).toHaveBeenCalledTimes(1);
      expect(services.transport.calls.filter((call) => call.method === "workflow.task.create")).toHaveLength(
        2,
      );
    });
  });
});

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const createdLabelID = "942495c2-5958-4959-8445-94046ad74fbd";

function renderNewTask(...routes: Parameters<typeof createTestServices>[0]) {
  const services = createTestServices([
    {
      method: "project.workspace.list",
      result: workspaceList(),
    },
    {
      method: "workflow.project.label.list",
      result: {
        catalog: {
          project_id: "project-1",
          labels: [],
        },
      },
    },
    ...routes,
  ]);
  const onSubmitted = vi.fn();
  render(
    <TestAppProviders services={services}>
      <NewTaskForm
        boardQueryWorkflowID="workflow-1"
        onSubmitted={onSubmitted}
        projectID="project-1"
        workflowID="workflow-1"
      />
    </TestAppProviders>,
  );
  return { onSubmitted, services };
}

function workspaceList() {
  return {
    project_id: "project-1",
    workspaces: [workspace("workspace-1", "Main", true)],
    default_workspace_id: "workspace-1",
    next_page_token: "",
  } as const;
}

function workspace(id: string, name: string, primary: boolean) {
  return {
    workspace_id: id,
    display_name: name,
    root_path: `/tmp/${id}`,
    availability: "available",
    is_primary: primary,
    updated_at_unix_ms: 1,
  } as const;
}

function labelID(index: number): string {
  return `00000000-0000-4000-8000-${index.toString().padStart(12, "0")}`;
}

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  resolve(value: T): void;
}> {
  let resolve: ((value: T) => void) | null = null;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return {
    promise,
    resolve(value: T): void {
      resolve?.(value);
    },
  };
}
