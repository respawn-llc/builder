import {
  attentionItemSchema,
  runSchema,
  taskStatusSchema,
  transitionSchema,
  validationErrorSchema,
} from "./common";

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

const approvalSnapshot = {
  source_node_display_name: "Review",
  targets: [{ display_name: "Done" }],
  commentary: "",
  output_values: {},
  workflow_revision_seen: 1,
};

const approvalAttentionItem = {
  id: "approval:transition-1",
  kind: "approval",
  project_id: "project-1",
  workflow_id: "workflow-1",
  task_id: "task-1",
  task_short_id: "KT-1",
  task_title: "Task",
  task_transition_id: "transition-1",
  message: "Approval required",
  approval_snapshot: approvalSnapshot,
  occurred_at_unix_ms: 1,
};

const interruptedRunAttentionItem = {
  id: "interrupted_run:run-1",
  kind: "interrupted_run",
  project_id: "project-1",
  workflow_id: "workflow-1",
  task_id: "task-1",
  task_short_id: "KT-1",
  task_title: "Task",
  run_id: "run-1",
  message: "Run interrupted",
  occurred_at_unix_ms: 1,
};

describe("attentionItemSchema", () => {
  it("decodes only the task-scoped discriminated attention variants", () => {
    const question = attentionItemSchema.parse(baseAttentionItem);
    const approval = attentionItemSchema.parse(approvalAttentionItem);
    const interrupted = attentionItemSchema.parse(interruptedRunAttentionItem);
    if (
      question.kind !== "question" ||
      approval.kind !== "approval" ||
      interrupted.kind !== "interrupted_run"
    ) {
      throw new Error("attention variants did not decode to their discriminants");
    }
    expect(question.question).toBeNull();
    expect(question.recommendedOptionIndex).toBeNull();
    expect(approval.approvalSnapshot).not.toBeNull();
    expect(interrupted.runID).toBe("run-1");
    expect(interrupted.sessionID).toBeNull();
    expect(interrupted.detailJSON).toBeNull();

    const rejected = [
      { ...baseAttentionItem, kind: "validation_blocker" },
      { ...baseAttentionItem, kind: "future_attention" },
      { ...baseAttentionItem, id: "" },
      { ...baseAttentionItem, project_id: "" },
      { ...baseAttentionItem, task_id: "" },
      { ...baseAttentionItem, task_short_id: "" },
      { ...baseAttentionItem, task_title: "" },
      { ...baseAttentionItem, workflow_id: "" },
      { ...baseAttentionItem, run_id: "" },
      { ...baseAttentionItem, ask_id: "" },
      { ...baseAttentionItem, task_transition_id: "transition-1" },
      { ...baseAttentionItem, approval_snapshot: approvalSnapshot },
      { ...baseAttentionItem, detail_json: "{}" },
      { ...approvalAttentionItem, task_transition_id: "" },
      (() => {
        const item = { ...approvalAttentionItem };
        Reflect.deleteProperty(item, "approval_snapshot");
        return item;
      })(),
      { ...approvalAttentionItem, ask_id: "ask-1" },
      { ...approvalAttentionItem, run_id: "run-1" },
      { ...approvalAttentionItem, session_id: "session-1" },
      { ...approvalAttentionItem, suggestions: [] },
      { ...approvalAttentionItem, recommended_option_index: 1 },
      { ...approvalAttentionItem, question: { kind: "ordinary" } },
      { ...approvalAttentionItem, detail_json: "{}" },
      { ...interruptedRunAttentionItem, run_id: "" },
      { ...interruptedRunAttentionItem, ask_id: "ask-1" },
      { ...interruptedRunAttentionItem, suggestions: [] },
      { ...interruptedRunAttentionItem, recommended_option_index: 1 },
      { ...interruptedRunAttentionItem, question: { kind: "ordinary" } },
      { ...interruptedRunAttentionItem, task_transition_id: "transition-1" },
      { ...interruptedRunAttentionItem, approval_snapshot: approvalSnapshot },
      { ...baseAttentionItem, session_id: "" },
      { ...interruptedRunAttentionItem, detail_json: "" },
      (() => {
        const item = { ...baseAttentionItem };
        Reflect.deleteProperty(item, "project_id");
        return item;
      })(),
      (() => {
        const item = { ...baseAttentionItem };
        Reflect.deleteProperty(item, "task_short_id");
        return item;
      })(),
      (() => {
        const item = { ...baseAttentionItem };
        Reflect.deleteProperty(item, "task_title");
        return item;
      })(),
    ];

    for (const item of rejected) {
      expect(() => attentionItemSchema.parse(item)).toThrow();
    }
  });

  it("parses runtime approval question prompt metadata", () => {
    const item = attentionItemSchema.parse({
      ...baseAttentionItem,
      question: {
        kind: "approval",
        approval_decisions: ["allow_once", "allow_session", "deny"],
      },
    });
    if (item.kind !== "question") {
      throw new Error("runtime approval prompt did not decode as a question");
    }

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

  it("does not duplicate the server-owned workflow graph node limit", () => {
    const targets = Array.from({ length: 201 }, (_, index) => ({
      display_name: `Target ${String(index)}`,
    }));
    const item = attentionItemSchema.parse({
      ...approvalAttentionItem,
      approval_snapshot: {
        ...approvalSnapshot,
        targets,
      },
    });
    if (item.kind !== "approval") {
      throw new Error("approval target item did not decode as an approval");
    }

    expect(item.approvalSnapshot.targets).toHaveLength(targets.length);
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

describe("validationErrorSchema", () => {
  const base = { code: "code", message: "message", blocks_context: true };

  it("normalizes omitted or null role-tool details to null", () => {
    const omitted = validationErrorSchema.parse(base);
    const present = validationErrorSchema.parse({
      ...base,
      details: { role: "coder", required_tool: "ask_question" },
    });
    expect(omitted.details).toMatchObject({ role: null, requiredTool: null });
    expect(present.details).toMatchObject({ role: "coder", requiredTool: "ask_question" });
  });

  it("rejects present blank role-tool details", () => {
    for (const details of [{ role: "" }, { role: " " }, { required_tool: "" }, { required_tool: " " }]) {
      expect(() => validationErrorSchema.parse({ ...base, details })).toThrow();
    }
  });
});
