import { decodeWorkflowLabelError, RpcError, WorkflowLabelError } from "./errors";

const labelID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";

describe("workflow label RPC errors", () => {
  it.each([
    {
      reason: "invalid_name",
      data: { project_id: "project-1", field: "name" },
      expected: { projectID: "project-1", field: "name" },
    },
    {
      reason: "name_conflict",
      data: { project_id: "project-1" },
      expected: { projectID: "project-1" },
    },
    {
      reason: "catalog_limit",
      data: { project_id: "project-1", limit: 100 },
      expected: { projectID: "project-1", limit: 100 },
    },
    {
      reason: "project_not_found",
      data: { project_id: "project-1" },
      expected: { projectID: "project-1" },
    },
    {
      reason: "label_not_found",
      data: { label_id: labelID },
      expected: { labelID },
    },
    {
      reason: "task_not_found",
      data: { task_id: "task-1" },
      expected: { taskID: "task-1" },
    },
    {
      reason: "wrong_project",
      data: { project_id: "project-1", label_id: labelID },
      expected: { projectID: "project-1", labelID },
    },
    {
      reason: "invalid_filter",
      data: { field: "label_filter.label_ids" },
      expected: { field: "label_filter.label_ids" },
    },
    {
      reason: "invalid_mutation",
      data: { field: "add_label_ids" },
      expected: { field: "add_label_ids" },
    },
  ] as const)("decodes $reason without inspecting the rendered message", ({ data, expected, reason }) => {
    const rpcError = new RpcError({
      code: -32031,
      message: "the same display-only message",
      method: "workflow.project.label.create",
      data: {
        type: "workflow_label_error",
        reason,
        ...data,
      },
    });

    const error = decodeWorkflowLabelError(rpcError);

    expect(error).toBeInstanceOf(WorkflowLabelError);
    expect(error).toMatchObject({ reason, ...expected });
    expect(error?.message).toBe("the same display-only message");
  });

  it("uses the generic RPC error path for missing or malformed structured data", () => {
    const missing = new RpcError({
      code: -32031,
      message: "generic",
      method: "workflow.project.label.create",
    });
    const malformed = new RpcError({
      code: -32031,
      message: "generic",
      method: "workflow.project.label.create",
      data: {
        type: "workflow_label_error",
        reason: "catalog_limit",
        project_id: "project-1",
        limit: 99,
      },
    });

    expect(decodeWorkflowLabelError(missing)).toBeNull();
    expect(decodeWorkflowLabelError(malformed)).toBeNull();
  });
});
