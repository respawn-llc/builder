import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import { TestAppProviders, createTestServices } from "@/test-support/app-services";
import { NewTaskForm } from "./NewTaskDialog";

describe("NewTaskForm sidebar admission", () => {
  it("admits the mutation synchronously and keeps the admission through success", async () => {
    const created = deferred<unknown>();
    const services = createTestServices([
      workspaceRoute,
      labelRoute,
      { method: "workflow.task.create", result: created.promise },
    ]);
    const release = vi.fn();
    const onAdmission = vi.fn(() => release);
    const onSubmitted = vi.fn();
    renderForm(services, onAdmission, onSubmitted);

    const user = userEvent.setup();
    await waitFor(() => expect(screen.getByLabelText("Title")).toBeEnabled());
    await user.type(screen.getByLabelText("Title"), "Related Task");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(onAdmission).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(services.transport.calls.filter((call) => call.method === "workflow.task.create")).toHaveLength(1),
    );
    expect(onSubmitted).not.toHaveBeenCalled();

    created.resolve({ task: { id: "task-created" } });
    await waitFor(() => expect(onSubmitted).toHaveBeenCalledWith("task-created"));
    expect(release).not.toHaveBeenCalled();
  });

  it("releases admission after a failed mutation while preserving the form draft", async () => {
    const failed = deferred<unknown>();
    const release = vi.fn();
    const services = createTestServices([
      workspaceRoute,
      labelRoute,
      { method: "workflow.task.create", result: failed.promise },
    ]);
    const onAdmission = vi.fn(() => release);
    renderForm(services, onAdmission, vi.fn());

    const user = userEvent.setup();
    await waitFor(() => expect(screen.getByLabelText("Title")).toBeEnabled());
    await user.type(screen.getByLabelText("Title"), "Failed Task");
    await user.click(screen.getByRole("button", { name: "Create task" }));
    await waitFor(() =>
      expect(services.transport.calls.filter((call) => call.method === "workflow.task.create")).toHaveLength(1),
    );
    failed.reject(new Error("create failed"));

    await waitFor(() => expect(release).toHaveBeenCalledTimes(1));
    expect(screen.getByLabelText("Title")).toHaveValue("Failed Task");
  });

  it("does not start a request when the scoped admission is stale", async () => {
    const services = createTestServices([workspaceRoute, labelRoute, {
      method: "workflow.task.create",
      result: { task: { id: "unexpected" } },
    }]);
    const onAdmission = vi.fn(() => null);
    renderForm(services, onAdmission, vi.fn());

    const user = userEvent.setup();
    await waitFor(() => expect(screen.getByLabelText("Title")).toBeEnabled());
    await user.type(screen.getByLabelText("Title"), "Stale Task");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(onAdmission).toHaveBeenCalledTimes(1);
    expect(services.transport.calls.filter((call) => call.method === "workflow.task.create")).toHaveLength(0);
  });
});

const workspaceRoute = {
  method: "project.workspace.list",
  result: {
    project_id: "project-1",
    workspaces: [
      {
        workspace_id: "workspace-1",
        display_name: "Workspace",
        root_path: "/workspace",
        availability: "available",
        is_primary: true,
        updated_at_unix_ms: 1,
      },
    ],
    default_workspace_id: "workspace-1",
    next_page_token: "",
  },
} as const;

const labelRoute = {
  method: "workflow.project.label.list",
  result: { catalog: { project_id: "project-1", labels: [] } },
} as const;

function renderForm(
  services: ReturnType<typeof createTestServices>,
  onSubmitAdmission: () => (() => void) | null,
  onSubmitted: (taskID?: string) => void,
) {
  return render(
    <TestAppProviders services={services}>
      <NewTaskForm
        boardQueryWorkflowID="11111111-1111-4111-8111-111111111111"
        onSubmitAdmission={onSubmitAdmission}
        onSubmitted={onSubmitted}
        projectID="project-1"
        workflowID="11111111-1111-4111-8111-111111111111"
      />
    </TestAppProviders>,
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
