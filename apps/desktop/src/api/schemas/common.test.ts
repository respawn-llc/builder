import { attentionItemSchema, runSchema, taskStatusSchema, transitionSchema } from "./common";

const baseAttentionItem = {
  id: "question:run-1:ask-1",
  kind: "question",
  project_id: "project-1",
  workflow_id: "workflow-1",
  task_id: "task-1",
  task_short_id: "KT-1",
  task_title: "Task",
  run_id: "run-1",
  session_id: "session-1",
  ask_id: "ask-1",
  message: "Approve protected path?",
  occurred_at_unix_ms: 1,
};

describe("attentionItemSchema", () => {
  it("parses runtime approval question prompt metadata", () => {
    const item = attentionItemSchema.parse({
      ...baseAttentionItem,
      question: {
        kind: "approval",
        approval_decisions: ["allow_once", "allow_session", "deny"],
      },
    });

    expect(item.question).toEqual({
      kind: "approval",
      approvalDecisions: ["allow_once", "allow_session", "deny"],
    });
    expect(item.suggestions).toEqual([]);
  });

  it("rejects malformed runtime approval question prompt metadata", () => {
    expect(() =>
      attentionItemSchema.parse({
        ...baseAttentionItem,
        question: { kind: "approval" },
      }),
    ).toThrow();
    expect(() =>
      attentionItemSchema.parse({
        ...baseAttentionItem,
        question: { kind: "approval", approval_decisions: ["allow_forever"] },
      }),
    ).toThrow();
  });
});

describe("taskStatusSchema", () => {
  const status = {
    attention_types: [],
    kind: "queued",
    native_state: "queued",
    node_ids: [],
    run_ids: [],
  };

  it("accepts every typed task status kind without a display label", () => {
    for (const kind of [
      "canceled",
      "done",
      "waiting_question",
      "waiting_approval",
      "interrupted",
      "running",
      "queued",
      "backlog",
      "active",
    ] as const) {
      expect(taskStatusSchema.parse({ ...status, kind })).toMatchObject({ kind });
    }
  });

  it("rejects the removed server display label", () => {
    expect(() => taskStatusSchema.parse({ ...status, label: "Queued" })).toThrow();
  });
});

describe("workflow lifecycle schemas", () => {
  it("normalizes omitted and explicit null lifecycle facts to null", () => {
    const baseRun = {
      id: "run-1",
      task_id: "task-1",
      placement_id: "placement-1",
      node_id: "node-1",
      status: "queued",
      generation: 1,
    };
    const omittedRun = runSchema.parse(baseRun);
    const explicitNullRun = runSchema.parse({
      ...baseRun,
      waiting_ask_id: null,
      started_at_unix_ms: null,
      completed_at_unix_ms: null,
      interrupted_at_unix_ms: null,
      interruption_reason: null,
    });
    const baseTransition = {
      id: "transition-1",
      task_id: "task-1",
      source_node_id: "node-1",
      source_node_key: "implement",
      transition_id: "done",
      actor: "agent",
      state: "pending_approval",
      created_at_unix_ms: 1,
    };

    expect(omittedRun).toMatchObject({
      waitingAskID: null,
      startedAt: null,
      completedAt: null,
      interruptedAt: null,
      interruptionReason: null,
    });
    expect(explicitNullRun).toMatchObject({
      waitingAskID: null,
      startedAt: null,
      completedAt: null,
      interruptedAt: null,
      interruptionReason: null,
    });
    expect(transitionSchema.parse(baseTransition).appliedAt).toBeNull();
    expect(transitionSchema.parse({ ...baseTransition, applied_at_unix_ms: null }).appliedAt).toBeNull();
  });
});
