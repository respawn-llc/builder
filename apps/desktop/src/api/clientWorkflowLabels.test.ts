import { FakeRpcTransport } from "@/test-support/api";
import { ApiClient } from "./client";
import { taskLabelFilterPayload } from "./clientWorkflowLabels";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const urgentID = "942495c2-5958-4959-8445-94046ad74fbd";
const smallID = "11111111-1111-4111-8111-111111111111";

describe("ApiClient workflow labels", () => {
  it("omits an empty excluded partition from a named filter payload", () => {
    expect(
      taskLabelFilterPayload({
        kind: "named",
        mode: "any",
        labelIDs: [priorityID],
        excludedLabelIDs: [],
      }),
    ).toEqual({
      kind: "named",
      named: {
        mode: "any",
        label_ids: [priorityID],
      },
    });
  });

  it("lists the complete bounded Project label catalog", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityID, name: "Priority" }],
          },
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.listProjectLabels("project-1")).resolves.toEqual({
      projectID: "project-1",
      labels: [{ id: priorityID, name: "Priority" }],
    });
    expect(transport.calls).toEqual([
      {
        method: "workflow.project.label.list",
        params: { project_id: "project-1" },
      },
    ]);
  });

  it("creates a Project label and returns the authoritative label", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.project.label.create",
        result: { label: { id: priorityID, name: "Priority" } },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.createProjectLabel("project-1", "Priority")).resolves.toEqual({
      id: priorityID,
      name: "Priority",
    });
    expect(transport.calls).toEqual([
      {
        method: "workflow.project.label.create",
        params: { project_id: "project-1", name: "Priority" },
      },
    ]);
  });

  it("renames a Project label without changing its identity", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.project.label.rename",
        result: { label: { id: priorityID, name: "Urgent" } },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.renameProjectLabel("project-1", priorityID, "Urgent")).resolves.toEqual({
      id: priorityID,
      name: "Urgent",
    });
    expect(transport.calls).toEqual([
      {
        method: "workflow.project.label.rename",
        params: { project_id: "project-1", label_id: priorityID, name: "Urgent" },
      },
    ]);
  });

  it("deletes a Project label and returns its authoritative identity", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.project.label.delete",
        result: { label_id: priorityID },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.deleteProjectLabel("project-1", priorityID)).resolves.toBe(priorityID);
    expect(transport.calls).toEqual([
      {
        method: "workflow.project.label.delete",
        params: { project_id: "project-1", label_id: priorityID },
      },
    ]);
  });

  it("reads and updates the authoritative task label assignment", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.task.labels.get",
        result: { assignment: { task_id: "task-1", label_ids: [priorityID] } },
      },
      {
        method: "workflow.task.labels.update",
        result: { assignment: { task_id: "task-1", label_ids: [urgentID] } },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.getTaskLabels("task-1")).resolves.toEqual({
      taskID: "task-1",
      labelIDs: [priorityID],
    });
    await expect(client.updateTaskLabels("task-1", [urgentID], [priorityID])).resolves.toEqual({
      taskID: "task-1",
      labelIDs: [urgentID],
    });
    expect(transport.calls).toEqual([
      {
        method: "workflow.task.labels.get",
        params: { task_id: "task-1" },
      },
      {
        method: "workflow.task.labels.update",
        params: {
          task_id: "task-1",
          add_label_ids: [urgentID],
          remove_label_ids: [priorityID],
        },
      },
    ]);
  });

  it("creates a task with an explicit atomic label assignment", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.task.create",
        result: { task: { id: "task-1" } },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(
      client.createTask({
        projectID: "project-1",
        workflowID: "workflow-1",
        title: "Ship labels",
        body: "Wire the desktop API.",
        sourceWorkspaceID: "workspace-1",
        labelIDs: [priorityID],
      }),
    ).resolves.toBe("task-1");
    expect(transport.calls).toEqual([
      {
        method: "workflow.task.create",
        params: {
          project_id: "project-1",
          workflow_id: "workflow-1",
          title: "Ship labels",
          body: "Wire the desktop API.",
          source_workspace_id: "workspace-1",
          label_ids: [priorityID],
        },
      },
    ]);
  });

  it("lists label-filtered task projections with label IDs", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.task.list",
        result: {
          scope: { project_id: "project-1", workflow_id: "workflow-1" },
          matching_workflow_cardinality: "one",
          next_page_token: null,
          generated_at_unix_ms: 7,
          tasks: [
            {
              task_id: "task-1",
              short_id: "PROJ-1",
              workflow_id: "workflow-1",
              workflow_name: "Delivery",
              title: "Ship labels",
              created_at_unix_ms: 1,
              updated_at_unix_ms: 2,
              column_keys: ["implement"],
              status: {
                kind: "active",
                native_state: "active",
                node_ids: ["node-1"],
                run_ids: [],
                attention_types: [],
              },
              run_count: 0,
              label_ids: [priorityID],
            },
          ],
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(
      client.listTasks({
        projectID: "project-1",
        workflowID: "workflow-1",
        labelFilter: {
          kind: "named",
          mode: "any",
          labelIDs: [priorityID, urgentID],
          excludedLabelIDs: [smallID],
        },
        pageSize: 25,
      }),
    ).resolves.toMatchObject({
      scope: { projectID: "project-1", workflowID: "workflow-1" },
      matchingWorkflowCardinality: "one",
      tasks: [{ id: "task-1", labelIDs: [priorityID] }],
    });
    expect(transport.calls).toEqual([
      {
        method: "workflow.task.list",
        params: {
          project_id: "project-1",
          workflow_id: "workflow-1",
          column_keys: [],
          status_kinds: [],
          attention_kinds: [],
          label_filter: {
            kind: "named",
            named: {
              mode: "any",
              label_ids: [urgentID, priorityID],
              excluded_label_ids: [smallID],
            },
          },
          sort: [],
          page_size: 25,
        },
      },
    ]);
  });
});
