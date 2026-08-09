import {
  decodeWorkflowTaskMovePreparationError,
  decodeWorkflowLabelError,
  decodeWorkflowTaskDependencyError,
  isProjectMissingError,
  isTaskMissingError,
  RpcError,
  WorkflowLabelError,
  WorkflowTaskDependencyError,
  WorkflowTaskMovePreparationError,
} from "./errors";
import { registeredWorktreeWire } from "@/test-support/api";
import { rpcErrorCodes } from "./rpcErrorCodes";

const labelID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";

describe("sidebar missing-entity errors", () => {
  it("recognizes typed Task and Project missing errors without parsing messages", () => {
    const error = (code: number, data?: Readonly<Record<string, string>>) =>
      new RpcError({ code, data, message: "changed", method: "owner.operation" });
    expect(isTaskMissingError(error(rpcErrorCodes.workflowTaskNotFound))).toBe(true);
    expect(isProjectMissingError(error(-32000, { reason: "project_not_found" }))).toBe(true);
    expect(isProjectMissingError(error(rpcErrorCodes.projectNotFound))).toBe(true);
    expect(isProjectMissingError(new Error("project_not_found"))).toBe(false);
  });
});

describe("Workflow Task Move preparation RPC errors", () => {
  it("decodes target preparation with only a retained previous Worktree", () => {
    const decoded = decodeWorkflowTaskMovePreparationError(
      new RpcError({
        code: -32061,
        method: "workflow.task.move",
        message: "human text is not the contract",
        data: {
          type: "workflow_task_move_preparation",
          failure: {
            retry_readiness: "retry_ready",
            cause: { kind: "target_preparation", target_preparation: {} },
            diagnostic: "replacement creation failed",
            script_path: null,
            execution_target: null,
            retained_worktree: null,
            retained_previous_worktree: {
              worktree: registeredWorktreeWire("/repo/previous", "worktree-previous"),
            },
          },
        },
      }),
    );
    expect(decoded).toBeInstanceOf(WorkflowTaskMovePreparationError);
    expect(decoded).toMatchObject({
      failure: {
        cause: { kind: "target_preparation" },
        diagnostic: "replacement creation failed",
        scriptPath: null,
        retainedWorktree: null,
        retainedPreviousWorktree: {
          worktree: { registered: { kent: { canonicalRoot: "/repo/previous" } } },
        },
      },
    });
  });

  it("rejects setup-script failure without its script path", () => {
    expect(
      decodeWorkflowTaskMovePreparationError(
        new RpcError({
          code: -32061,
          method: "workflow.task.move",
          message: "setup failed",
          data: {
            type: "workflow_task_move_preparation",
            failure: {
              retry_readiness: "retry_ready",
              cause: { kind: "operational", operational: {} },
              diagnostic: "setup failed",
              script_path: null,
              execution_target: null,
              retained_worktree: registeredWorktreeWire("/repo/current", "worktree-current"),
              retained_previous_worktree: null,
            },
          },
        }),
      ),
    ).toBeNull();
  });
});

describe("sidebar missing-entity errors", () => {
  it("recognizes typed Task and Project missing errors without parsing messages", () => {
    const error = (code: number, data?: Readonly<Record<string, string>>) =>
      new RpcError({ code, data, message: "changed", method: "owner.operation" });
    expect(isTaskMissingError(error(rpcErrorCodes.workflowTaskNotFound))).toBe(true);
    expect(isProjectMissingError(error(-32000, { reason: "project_not_found" }))).toBe(true);
    expect(isProjectMissingError(error(rpcErrorCodes.projectNotFound))).toBe(true);
    expect(isProjectMissingError(new Error("project_not_found"))).toBe(false);
  });
});

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

describe("workflow task dependency RPC errors", () => {
  it("decodes typed limit metadata without inspecting message copy", () => {
    const error = decodeWorkflowTaskDependencyError(
      new RpcError({
        code: -32049,
        message: "display only",
        method: "workflow.task.dependency.add",
        data: {
          type: "workflow_task_dependency_error",
          reason: "blocker_limit",
          blocker_task_id: "task-1",
          blocked_task_id: "task-2",
          current_count: 7,
          limit: 7,
        },
      }),
    );

    expect(error).toBeInstanceOf(WorkflowTaskDependencyError);
    expect(error).toMatchObject({
      reason: "blocker_limit",
      blockerTaskID: "task-1",
      blockedTaskID: "task-2",
      currentCount: 7,
      limit: 7,
    });
  });
});
