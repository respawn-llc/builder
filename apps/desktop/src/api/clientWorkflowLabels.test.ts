import { FakeRpcTransport } from "@/test-support/api";
import { ApiClient } from "./client";
import { ContractError } from "./errors";
import { taskLabelFilterPayload } from "./clientWorkflowLabels";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const urgentID = "942495c2-5958-4959-8445-94046ad74fbd";
const smallID = "11111111-1111-4111-8111-111111111111";

describe("ApiClient workflow labels", () => {
  it("reorders a Project label catalog and preserves the authoritative response order", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.project.label.reorder",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [
              { id: urgentID, name: "Urgent" },
              { id: priorityID, name: "Priority" },
            ],
          },
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.reorderProjectLabels("project-1", [urgentID, priorityID])).resolves.toEqual({
      projectID: "project-1",
      labels: [
        { id: urgentID, name: "Urgent" },
        { id: priorityID, name: "Priority" },
      ],
    });
    expect(transport.calls).toEqual([
      {
        method: "workflow.project.label.reorder",
        params: {
          project_id: "project-1",
          label_ids: [urgentID, priorityID],
        },
      },
    ]);
  });

  it("creates a related task through one atomic relationship intent", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.task.create",
        result: { task: { id: "task-new" } },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(
      client.createTask({
        projectID: "project-1",
        workflowID: smallID,
        title: "New blocker",
        body: "",
        sourceWorkspaceID: "workspace-origin",
        labelIDs: [],
        dependencyIntent: {
          relatedTaskID: "task-origin",
          newTaskRole: "blocker",
        },
      }),
    ).resolves.toBe("task-new");

    expect(transport.calls).toEqual([
      {
        method: "workflow.task.create",
        params: {
          project_id: "project-1",
          workflow_id: smallID,
          title: "New blocker",
          body: "",
          source_workspace_id: "workspace-origin",
          label_ids: [],
          dependency_intent: {
            related_task_id: "task-origin",
            new_task_role: "blocker",
          },
        },
      },
    ]);
  });

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
        workflowID: "11111111-1111-4111-8111-111111111111",
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
          workflow_id: "11111111-1111-4111-8111-111111111111",
          title: "Ship labels",
          body: "Wire the desktop API.",
          source_workspace_id: "workspace-1",
          label_ids: [priorityID],
        },
      },
    ]);
  });

  it("rejects malformed and prefixed Workflow IDs before task RPCs", async () => {
    const transport = new FakeRpcTransport([]);
    const client = new ApiClient(transport);

    await expect(
      client.createTask({
        projectID: "project-1",
        workflowID: "not-a-workflow-id",
        title: "Ship labels",
        body: "",
        sourceWorkspaceID: "workspace-1",
        labelIDs: [],
      }),
    ).rejects.toThrow();
    await expect(
      client.listTasks({
        projectID: "project-1",
        workflowID: "workflow-11111111-1111-4111-8111-111111111111",
        labelFilter: { kind: "none" },
        limit: 25,
      }),
    ).rejects.toThrow();

    expect(transport.calls).toEqual([]);
  });

  it("lists label-filtered task projections with ordered Label display data", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.task.list",
        result: {
          scope: { project_id: "project-1", workflow_id: "11111111-1111-4111-8111-111111111111" },
          matching_workflow_cardinality: "one",
          next_offset: null,
          generated_at_unix_ms: 7,
          tasks: [
            {
              task_id: "task-1",
              short_id: "PROJ-1",
              workflow_id: "11111111-1111-4111-8111-111111111111",
              workflow_name: "Delivery",
              title: "Ship labels",
              created_at_unix_ms: 1,
              updated_at_unix_ms: 2,
              column_keys: ["implement"],
              status: {
                kind: "active",
                native_state: "active",
                node_ids: ["node-1"],
                attention_types: [],
              },
              labels: [{ id: priorityID, name: "Priority" }],
              dependency_progress: { satisfied_count: 1, total_count: 2 },
            },
          ],
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(
      client.listTasks({
        projectID: "project-1",
        workflowID: "11111111-1111-4111-8111-111111111111",
        labelFilter: {
          kind: "named",
          mode: "any",
          labelIDs: [priorityID, urgentID],
          excludedLabelIDs: [smallID],
        },
        limit: 25,
      }),
    ).resolves.toMatchObject({
      scope: { projectID: "project-1", workflowID: "11111111-1111-4111-8111-111111111111" },
      matchingWorkflowCardinality: "one",
      tasks: [
        {
          id: "task-1",
          labels: [{ id: priorityID, name: "Priority" }],
          dependencyProgress: { satisfiedCount: 1, totalCount: 2 },
        },
      ],
    });
    expect(transport.calls).toEqual([
      {
        method: "workflow.task.list",
        params: {
          project_id: "project-1",
          workflow_id: "11111111-1111-4111-8111-111111111111",
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
          offset: 0,
          limit: 25,
        },
      },
    ]);
  });

  it("rejects a zero task-list continuation offset", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([
        {
          method: "workflow.task.list",
          result: {
            scope: { project_id: "project-1" },
            matching_workflow_cardinality: "none",
            next_offset: 0,
            generated_at_unix_ms: 7,
            tasks: [],
          },
        },
      ]),
    );

    await expect(
      client.listTasks({
        projectID: "project-1",
        labelFilter: { kind: "none" },
      }),
    ).rejects.toBeInstanceOf(ContractError);
  });
});
