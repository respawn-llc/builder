import { ApiClient } from "./client";
import type { AttentionNotificationEvent } from "./attentionNotifications";
import { ContractError } from "./errors";
import { FakeRpcTransport } from "@/test-support/api";

describe("attention notification API", () => {
  it("subscribes to typed attention notifications and rejects malformed events at the API boundary", () => {
    const transport = new FakeRpcTransport([]);
    const client = new ApiClient(transport);
    const events: AttentionNotificationEvent[] = [];
    const errors: Error[] = [];

    client.subscribeAttentionNotifications({
      onEvent(event) {
        events.push(event);
      },
      onComplete() {
        return;
      },
      onError(error) {
        errors.push(error);
      },
    });

    expect(transport.subscriptions).toContainEqual({
      method: "attention.notification.subscribe",
      params: {},
    });
    transport.emit("attention.notification", {
      event: {
        type: "pending",
        sequence: 1,
        source: "live",
        pending: {
          id: { kind: "question", uuid: "batch-1" },
          kind: "question",
          occurred_at: "2026-06-29T12:00:00Z",
          revision: 1,
          question: {
            prepared_ask_ids: ["ask-1", "ask-2"],
            materialized_ask_ids: ["ask-1"],
            current_unresolved_ask_ids: ["ask-1"],
            skipped_ask_ids: [],
            preview: "question from agent",
            display_count: 2,
            materialized_count: 1,
          },
          target: {
            kind: "workflow_task",
            project_id: "project-1",
            workflow_id: "11111111-1111-4111-8111-111111111111",
            task_id: "task-1",
            task_short_id: "KT-1",
            task_title: "Needs answer",
            session_id: "session-1",
            current_node_id: "node-1",
            focus: { kind: "question", ask_ids: ["ask-2", "ask-1"] },
          },
        },
      },
    });

    expect(events).toHaveLength(1);
    const event = events[0];
    if (event?.type !== "pending") {
      throw new Error("Expected parsed attention pending event.");
    }
    expect(event.pending.id).toEqual({ kind: "question", uuid: "batch-1" });
    expect(event.pending.question?.displayCount).toBe(2);
    if (event.pending.target.kind !== "workflow_task") {
      throw new Error("Expected workflow-task attention target.");
    }
    expect(event.pending.target.focus).toEqual({ kind: "question", askIDs: ["ask-2", "ask-1"] });

    transport.emit("attention.notification", {
      event: {
        type: "pending",
        sequence: 2,
        source: "live",
        pending: {
          id: "broken",
          kind: "question",
          occurred_at: "2026-06-29T12:00:00Z",
          revision: 1,
          target: { kind: "workflow_task", task_id: "task-1" },
        },
      },
    });

    expect(errors[0]).toBeInstanceOf(ContractError);

    transport.emit("attention.notification", {
      event: {
        type: "pending",
        sequence: 3,
        source: "live",
        pending: {
          id: { kind: "question", uuid: "prefixed" },
          kind: "question",
          occurred_at: "2026-06-29T12:00:00Z",
          revision: 1,
          question: {
            prepared_ask_ids: ["ask-1"],
            materialized_ask_ids: ["ask-1"],
            current_unresolved_ask_ids: ["ask-1"],
            skipped_ask_ids: [],
            display_count: 1,
            materialized_count: 1,
          },
          target: {
            kind: "workflow_task",
            workflow_id: "workflow-11111111-1111-4111-8111-111111111111",
            task_id: "task-1",
            focus: { kind: "question", ask_ids: ["ask-1"] },
          },
        },
      },
    });
    expect(errors[1]).toBeInstanceOf(ContractError);

    transport.emit("attention.notification", {
      event: {
        type: "pending",
        sequence: 4,
        source: "live",
        pending: {
          id: { kind: "question", uuid: "future" },
          kind: "future_attention",
          occurred_at: "2026-06-29T12:00:00Z",
          revision: 1,
          target: { kind: "future_target" },
        },
      },
    });

    expect(events).toHaveLength(1);
    expect(errors).toHaveLength(3);
    expect(errors[2]).toBeInstanceOf(ContractError);
  });

  it("parses generic and Workflow Approvals as distinct payloads", () => {
    const transport = new FakeRpcTransport([]);
    const client = new ApiClient(transport);
    const events: AttentionNotificationEvent[] = [];

    client.subscribeAttentionNotifications({
      onEvent(event) {
        events.push(event);
      },
      onComplete() {
        return;
      },
      onError(error) {
        throw error;
      },
    });

    transport.emit("attention.notification", {
      event: {
        type: "pending",
        sequence: 1,
        source: "live",
        pending: {
          id: { kind: "approval", uuid: "model-approval-1" },
          kind: "approval",
          occurred_at: "2026-06-29T12:00:00Z",
          revision: 1,
          approval: { access_targets: [{ requested_path: "../outside.txt", resolved_path: "/outside.txt" }] },
          target: {
            kind: "session_prompt",
            session_id: "session-1",
          },
        },
      },
    });
    transport.emit("attention.notification", {
      event: {
        type: "pending",
        sequence: 2,
        source: "live",
        pending: {
          id: { kind: "workflow_approval", uuid: "approval-notification-1" },
          kind: "workflow_approval",
          occurred_at: "2026-06-29T12:00:00Z",
          revision: 1,
          workflow_approval: { approval_id: "approval-1" },
          target: {
            kind: "workflow_task",
            workflow_id: "11111111-1111-4111-8111-111111111111",
            task_id: "task-1",
            focus: { kind: "approval", approval_id: "approval-1" },
          },
        },
      },
    });

    const genericApproval = events[0];
    if (genericApproval?.type !== "pending" || genericApproval.pending.target.kind !== "session_prompt") {
      throw new Error("Expected parsed generic approval attention pending event.");
    }
    expect(genericApproval.pending.approval?.accessTargets).toEqual([
      { requestedPath: "../outside.txt", resolvedPath: "/outside.txt" },
    ]);
    expect(genericApproval.pending.workflowApproval).toBeNull();

    const workflowApproval = events[1];
    if (workflowApproval?.type !== "pending" || workflowApproval.pending.target.kind !== "workflow_task") {
      throw new Error("Expected parsed Workflow Approval attention pending event.");
    }
    expect(workflowApproval.pending.approval).toBeNull();
    expect(workflowApproval.pending.workflowApproval?.approvalID).toBe("approval-1");
    expect(workflowApproval.pending.target.focus).toEqual({ kind: "approval", approvalID: "approval-1" });
  });

  it("parses interrupted-current-node attention notifications", () => {
    const transport = new FakeRpcTransport([]);
    const client = new ApiClient(transport);
    const events: AttentionNotificationEvent[] = [];

    client.subscribeAttentionNotifications({
      onEvent(event) {
        events.push(event);
      },
      onComplete() {
        return;
      },
      onError(error) {
        throw error;
      },
    });

    transport.emit("attention.notification", {
      event: {
        type: "pending",
        sequence: 1,
        source: "live",
        pending: {
          id: { kind: "interrupted_current_node", uuid: "node-1" },
          kind: "interrupted_current_node",
          occurred_at: "2026-06-29T12:00:00Z",
          revision: 1,
          interrupted_current_node: {
            message: "Current Node interrupted",
            reason: "workflow_runtime_failed",
          },
          target: {
            kind: "workflow_task",
            workflow_id: "11111111-1111-4111-8111-111111111111",
            task_id: "task-1",
            current_node_id: "node-1",
            focus: { kind: "interrupted_current_node" },
          },
        },
      },
    });

    const event = events[0];
    if (event?.type !== "pending" || event.pending.target.kind !== "workflow_task") {
      throw new Error("Expected parsed interrupted-current-node attention pending event.");
    }
    expect(event.pending.kind).toBe("interrupted_current_node");
    expect(event.pending.question).toBeNull();
    expect(event.pending.target.focus).toEqual({ kind: "interrupted_current_node" });
  });

  it("rejects incoherent attention payloads and targets", () => {
    const transport = new FakeRpcTransport([]);
    const client = new ApiClient(transport);
    const errors: Error[] = [];

    client.subscribeAttentionNotifications({
      onEvent() {
        throw new Error("Incoherent attention notification must not reach the UI.");
      },
      onComplete() {
        return;
      },
      onError(error) {
        errors.push(error);
      },
    });

    const question = {
      id: { kind: "question", uuid: "question-1" },
      kind: "question",
      occurred_at: "2026-06-29T12:00:00Z",
      revision: 1,
      question: {
        prepared_ask_ids: ["ask-1"],
        materialized_ask_ids: ["ask-1"],
        current_unresolved_ask_ids: ["ask-1"],
        skipped_ask_ids: [],
        display_count: 1,
        materialized_count: 1,
      },
      target: {
        kind: "workflow_task",
        task_id: "task-1",
        focus: { kind: "question", ask_ids: ["ask-1"] },
      },
    };

    const incoherentNotifications = [
      {
        ...question,
        target: {
          ...question.target,
          focus: { kind: "question", ask_ids: ["ask-2"] },
        },
      },
      {
        ...question,
        approval: { message: "Approve?" },
      },
      {
        id: { kind: "workflow_approval", uuid: "approval-notification-1" },
        kind: "workflow_approval",
        occurred_at: "2026-06-29T12:00:00Z",
        revision: 1,
        workflow_approval: { approval_id: "approval-1" },
        target: {
          kind: "workflow_task",
          task_id: "task-1",
          focus: { kind: "approval", approval_id: "approval-2" },
        },
      },
      {
        id: { kind: "interrupted_current_node", uuid: "interrupted-1" },
        kind: "interrupted_current_node",
        occurred_at: "2026-06-29T12:00:00Z",
        revision: 1,
        interrupted_current_node: { message: "Interrupted" },
        target: {
          kind: "workflow_task",
          task_id: "task-1",
          focus: { kind: "interrupted_current_node" },
        },
      },
    ];

    incoherentNotifications.forEach((pending, index) => {
      transport.emit("attention.notification", {
        event: {
          type: "pending",
          sequence: index + 1,
          source: "live",
          pending,
        },
      });
    });

    expect(errors).toHaveLength(incoherentNotifications.length);
    expect(errors.every((error) => error instanceof ContractError)).toBe(true);
  });
});
