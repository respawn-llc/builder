import {
  decodeWorkflowLabelError,
  decodeWorkflowTaskIntegrityError,
  RpcError,
  WorkflowLabelError,
  WorkflowTaskIntegrityError,
} from "./errors";
import { compactJsonObject } from "./json";

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

describe("workflow task integrity RPC errors", () => {
  const data = {
    type: "workflow_task_integrity_error",
    reason: "agent_session_missing",
    task_id: "task-1",
    placement_id: "placement-1",
    node_id: "node-1",
    node_kind: "agent",
    run_id: "run-1",
    generation: 3,
    status_kind: "running",
    durable: {
      run_present: true,
      started: true,
      completed: false,
      interrupted: false,
      waiting_question: false,
    },
    exact: {
      present: false,
      waiting_question: false,
    },
    actions: {
      can_interrupt: false,
      can_resume: false,
    },
  } as const;

  it("decodes the structured contract without inspecting server display text", () => {
    const rpcError = new RpcError({
      code: -32049,
      message: "display-only backend diagnostics",
      method: "workflow.task.get",
      data,
    });

    const error = decodeWorkflowTaskIntegrityError(rpcError);

    expect(error).toBeInstanceOf(WorkflowTaskIntegrityError);
    expect(error).toMatchObject({
      reason: "agent_session_missing",
      taskID: "task-1",
      placementID: "placement-1",
      nodeID: "node-1",
      nodeKind: "agent",
      runID: "run-1",
      generation: 3,
      statusKind: "running",
      durable: {
        runPresent: true,
        started: true,
        completed: false,
        interrupted: false,
        waitingQuestion: false,
      },
      exact: {
        present: false,
        kind: null,
        sessionID: null,
        waitingQuestion: false,
      },
      actions: {
        canInterrupt: false,
        canResume: false,
      },
    });
    expect(error?.message).toBe("display-only backend diagnostics");
  });

  it.each([
    {
      name: "wrong RPC code",
      code: -32000,
      data,
    },
    {
      name: "padded identity",
      code: -32049,
      data: { ...data, task_id: " task-1" },
    },
    {
      name: "generation without run",
      code: -32049,
      data: compactJsonObject({ ...data, run_id: undefined }),
    },
    {
      name: "absent exact execution with retained facts",
      code: -32049,
      data: { ...data, exact: { present: false, session_id: "session-1", waiting_question: false } },
    },
    {
      name: "present exact execution without kind",
      code: -32049,
      data: { ...data, exact: { present: true, waiting_question: false } },
    },
    {
      name: "unknown status",
      code: -32049,
      data: { ...data, status_kind: "lost" },
    },
  ])("uses the generic RPC path for $name", ({ code, data: malformedData }) => {
    const rpcError = new RpcError({
      code,
      message: "generic",
      method: "workflow.task.get",
      data: malformedData,
    });

    expect(decodeWorkflowTaskIntegrityError(rpcError)).toBeNull();
  });
});
