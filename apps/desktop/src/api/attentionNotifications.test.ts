import { ApiClient } from "./client";
import type { AttentionNotificationEvent } from "./attentionNotifications";
import { ContractError } from "./errors";
import { FakeRpcTransport } from "./fakeTransport";

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
            workflow_id: "workflow-1",
            task_id: "task-1",
            task_short_id: "KT-1",
            task_title: "Needs answer",
            session_id: "session-1",
            run_id: "run-1",
            focus: { kind: "question", ask_ids: ["ask-1", "ask-2"] },
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
    expect(event.pending.target.focus).toEqual({ kind: "question", askIDs: ["ask-1", "ask-2"] });

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
          id: { kind: "question", uuid: "future" },
          kind: "future_attention",
          occurred_at: "2026-06-29T12:00:00Z",
          revision: 1,
          target: { kind: "future_target" },
        },
      },
    });

    expect(events).toHaveLength(1);
    expect(errors).toHaveLength(1);
  });

  it("parses interrupted-run attention notifications", () => {
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
          id: { kind: "interrupted_run", uuid: "run-1" },
          kind: "interrupted_run",
          occurred_at: "2026-06-29T12:00:00Z",
          revision: 1,
          interrupted_run: {
            run_id: "run-1",
            message: "Run interrupted",
            reason: "workflow_runtime_failed",
          },
          target: {
            kind: "workflow_task",
            task_id: "task-1",
            run_id: "run-1",
            focus: { kind: "interrupted_run", run_id: "run-1" },
          },
        },
      },
    });

    const event = events[0];
    if (event?.type !== "pending" || event.pending.target.kind !== "workflow_task") {
      throw new Error("Expected parsed interrupted-run attention pending event.");
    }
    expect(event.pending.kind).toBe("interrupted_run");
    expect(event.pending.question).toBeNull();
    expect(event.pending.target.focus).toEqual({ kind: "interrupted_run", runID: "run-1" });
  });
});
