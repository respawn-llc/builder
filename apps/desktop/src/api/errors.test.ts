import {
  decodeWorktreeSetupRetainedError,
  decodeWorkflowLabelError,
  decodeWorkflowTaskDependencyError,
  isProjectMissingError,
  isTaskMissingError,
  RpcError,
  WorkflowLabelError,
  WorkflowTaskDependencyError,
  WorktreeSetupRetainedError,
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

describe("worktree setup retained RPC errors", () => {
  it("decodes required primary facts and optional previous worktree without text parsing", () => {
    const error = new RpcError({
      code: -32039,
      method: "workflow.task.move",
      message: "human text is not the contract",
      data: {
        type: "worktree_setup_retained",
        worktree: registeredWorktreeWire("/repo/current", "worktree-current"),
        script_path: "/repo/setup.sh",
        diagnostic: "setup failed after retry",
        retained_previous_worktree: {
          worktree: registeredWorktreeWire("/repo/previous", "worktree-previous"),
        },
      },
    });
    const decoded = decodeWorktreeSetupRetainedError(error);
    expect(decoded).toBeInstanceOf(WorktreeSetupRetainedError);
    expect(decoded).toMatchObject({
      scriptPath: "/repo/setup.sh",
      diagnostic: "setup failed after retry",
      worktree: { registered: { kent: { canonicalRoot: "/repo/current" } } },
      retainedPreviousWorktree: {
        worktree: { registered: { kent: { canonicalRoot: "/repo/previous" } } },
      },
    });
  });

  it("rejects malformed topology and blank diagnostics", () => {
    for (const data of [
      {
        type: "worktree_setup_retained",
        worktree: registeredWorktreeWire("/repo/current", "worktree-current"),
        script_path: "/repo/setup.sh",
        diagnostic: " ",
      },
      {
        type: "worktree_setup_retained",
        worktree: { variant: "registered", registered: {} },
        script_path: "/repo/setup.sh",
        diagnostic: "setup failed",
      },
      {
        type: "worktree_setup_retained",
        worktree: registeredWorktreeWire("/repo/current", "worktree-current"),
        script_path: " ",
        diagnostic: "setup failed",
      },
      {
        type: "worktree_setup_retained",
        worktree: registeredWorktreeWire("/repo/current", "worktree-current"),
        diagnostic: "setup failed",
      },
    ]) {
      expect(
        decodeWorktreeSetupRetainedError(
          new RpcError({
            code: -32039,
            method: "workflow.task.move",
            message: "setup failed",
            data,
          }),
        ),
      ).toBeNull();
    }
  });

  it("preserves the exact nonblank setup script identity", () => {
    const scriptPath = " /repo/setup.sh ";
    const decoded = decodeWorktreeSetupRetainedError(
      new RpcError({
        code: -32039,
        method: "workflow.task.move",
        message: "setup failed",
        data: {
          type: "worktree_setup_retained",
          worktree: registeredWorktreeWire("/repo/current", "worktree-current"),
          script_path: scriptPath,
          diagnostic: "setup failed",
        },
      }),
    );
    expect(decoded?.scriptPath).toBe(scriptPath);
  });

  it("does not classify another RPC method as Manual Move recovery", () => {
    expect(
      decodeWorktreeSetupRetainedError(
        new RpcError({
          code: -32039,
          method: "worktree.create",
          message: "setup failed",
          data: {
            type: "worktree_setup_retained",
            worktree: registeredWorktreeWire("/repo/current", "worktree-current"),
            script_path: "/repo/setup.sh",
            diagnostic: "setup failed",
          },
        }),
      ),
    ).toBeNull();
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
