import { attentionItemSchema, taskStatusSchema, validationErrorSchema, workflowIDSchema } from "./common";

const baseAttentionItem = {
  id: "question:node-1:ask-1",
  kind: "question",
  project_id: "project-1",
  workflow_id: "11111111-1111-4111-8111-111111111111",
  task_id: "task-1",
  task_short_id: "KT-1",
  task_title: "Task",
  current_node: { node_id: "node-1", transition_branch_key: null, session_id: null },
  session_name: "Session one",
  message: "Approve protected path?",
  question: {
    session_id: "session-1",
    step_id: "22222222-2222-4222-8222-222222222222",
    tool_call_id: "ask-1",
    kind: "ordinary",
    suggestions: [],
    recommended_option_index: null,
  },
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
  id: "approval:approval-1",
  kind: "approval",
  project_id: "project-1",
  workflow_id: "11111111-1111-4111-8111-111111111111",
  task_id: "task-1",
  task_short_id: "KT-1",
  task_title: "Task",
  approval_id: "approval-1",
  session_name: null,
  message: "Approval required",
  approval_snapshot: approvalSnapshot,
  occurred_at_unix_ms: 1,
};

const interruptedCurrentNodeAttentionItem = {
  id: "interrupted_current_node:node-1",
  kind: "interrupted_current_node",
  project_id: "project-1",
  workflow_id: "11111111-1111-4111-8111-111111111111",
  task_id: "task-1",
  task_short_id: "KT-1",
  task_title: "Task",
  current_node: { node_id: "node-1", transition_branch_key: null, session_id: null },
  session_id: null,
  session_name: null,
  message: "Current Node interrupted",
  occurred_at_unix_ms: 1,
};

describe("attentionItemSchema", () => {
  it("requires nullable session names for every attention variant", () => {
    const nullableItems = [
      { ...baseAttentionItem, session_name: null },
      approvalAttentionItem,
      interruptedCurrentNodeAttentionItem,
    ];
    for (const item of nullableItems) {
      expect(() => attentionItemSchema.parse(item)).not.toThrow();
    }

    for (const item of nullableItems) {
      const omitted = { ...item };
      Reflect.deleteProperty(omitted, "session_name");
      expect(() => attentionItemSchema.parse(omitted)).toThrow();
    }
  });

  it("decodes only the task-scoped discriminated attention variants", () => {
    const question = attentionItemSchema.parse(baseAttentionItem);
    const approval = attentionItemSchema.parse(approvalAttentionItem);
    const interrupted = attentionItemSchema.parse(interruptedCurrentNodeAttentionItem);
    if (
      question.kind !== "question" ||
      approval.kind !== "approval" ||
      interrupted.kind !== "interrupted_current_node"
    ) {
      throw new Error("attention variants did not decode to their discriminants");
    }
    expect(question.question.toolCallID).toBe("ask-1");
    expect(question.sessionName).toBe("Session one");
    expect(approval.approvalSnapshot).not.toBeNull();
    expect(interrupted.currentNode.nodeID).toBe("node-1");
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
      { ...baseAttentionItem, current_node: { ...baseAttentionItem.current_node, node_id: "" } },
      { ...baseAttentionItem, question: { ...baseAttentionItem.question, tool_call_id: "" } },
      { ...baseAttentionItem, question: { ...baseAttentionItem.question, tool_call_id: " ask-1 " } },
      { ...baseAttentionItem, question: { ...baseAttentionItem.question, session_id: "" } },
      { ...baseAttentionItem, question: { ...baseAttentionItem.question, step_id: "" } },
      { ...baseAttentionItem, session_name: "" },
      { ...baseAttentionItem, approval_id: "approval-1" },
      { ...baseAttentionItem, approval_snapshot: approvalSnapshot },
      { ...baseAttentionItem, detail_json: "{}" },
      { ...baseAttentionItem, session_id: "session-1" },
      { ...baseAttentionItem, question_id: "ask-1" },
      { ...baseAttentionItem, suggestions: [] },
      { ...baseAttentionItem, recommended_option_index: 1 },
      {
        ...baseAttentionItem,
        current_node: { ...baseAttentionItem.current_node, session_id: "session-1" },
      },
      { ...approvalAttentionItem, approval_id: "" },
      (() => {
        const item = { ...approvalAttentionItem };
        Reflect.deleteProperty(item, "approval_snapshot");
        return item;
      })(),
      { ...approvalAttentionItem, question_id: "ask-1" },
      { ...approvalAttentionItem, current_node: baseAttentionItem.current_node },
      { ...approvalAttentionItem, session_id: "session-1" },
      { ...approvalAttentionItem, session_name: "Session one" },
      { ...approvalAttentionItem, suggestions: [] },
      { ...approvalAttentionItem, recommended_option_index: 1 },
      { ...approvalAttentionItem, question: { kind: "ordinary" } },
      { ...approvalAttentionItem, detail_json: "{}" },
      {
        ...interruptedCurrentNodeAttentionItem,
        current_node: { node_id: "", transition_branch_key: null, session_id: null },
      },
      { ...interruptedCurrentNodeAttentionItem, question_id: "ask-1" },
      { ...interruptedCurrentNodeAttentionItem, session_name: "Session one" },
      { ...interruptedCurrentNodeAttentionItem, suggestions: [] },
      { ...interruptedCurrentNodeAttentionItem, recommended_option_index: 1 },
      { ...interruptedCurrentNodeAttentionItem, question: { kind: "ordinary" } },
      { ...interruptedCurrentNodeAttentionItem, approval_id: "approval-1" },
      { ...interruptedCurrentNodeAttentionItem, approval_snapshot: approvalSnapshot },
      { ...baseAttentionItem, session_id: "" },
      { ...interruptedCurrentNodeAttentionItem, detail_json: "" },
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

  it("decodes an Approval Tool Call ID and ordered access targets", () => {
    const item = attentionItemSchema.parse({
      ...baseAttentionItem,
      question: {
        tool_call_id: baseAttentionItem.question.tool_call_id,
        session_id: baseAttentionItem.question.session_id,
        step_id: baseAttentionItem.question.step_id,
        kind: "approval",
        approval_decisions: ["allow_once", "allow_session", "deny"],
        access_targets: [
          { requested_path: " /alias/a ", resolved_path: " /real/file " },
          { requested_path: "/alias/b", resolved_path: "/real/file" },
          { requested_path: "/real/other", resolved_path: "/real/other" },
        ],
      },
    });
    if (item.kind !== "question") {
      throw new Error("runtime approval prompt did not decode as a question");
    }

    expect(item.question).toEqual({
      toolCallID: "ask-1",
      sessionID: "session-1",
      stepID: "22222222-2222-4222-8222-222222222222",
      kind: "approval",
      approvalDecisions: ["allow_once", "allow_session", "deny"],
      accessTargets: [
        { requestedPath: " /alias/a ", resolvedPath: " /real/file " },
        { requestedPath: "/alias/b", resolvedPath: "/real/file" },
        { requestedPath: "/real/other", resolvedPath: "/real/other" },
      ],
    });
  });

  it("projects an omitted Approval access-target list as empty", () => {
    const item = attentionItemSchema.parse({
      ...baseAttentionItem,
      question: {
        tool_call_id: "ask-1",
        session_id: "session-1",
        step_id: "22222222-2222-4222-8222-222222222222",
        kind: "approval",
        approval_decisions: ["deny"],
      },
    });
    if (item.kind !== "question" || item.question.kind !== "approval") {
      throw new Error("runtime approval prompt did not decode as an Approval");
    }
    expect(item.question.accessTargets).toEqual([]);
  });

  it("accepts omitted client-owned fallback messages", () => {
    const approval = { ...approvalAttentionItem };
    const interrupted = { ...interruptedCurrentNodeAttentionItem };
    Reflect.deleteProperty(approval, "message");
    Reflect.deleteProperty(interrupted, "message");

    const parsedApproval = attentionItemSchema.parse(approval);
    const parsedInterrupted = attentionItemSchema.parse(interrupted);

    expect(parsedApproval.message).toBeNull();
    expect(parsedInterrupted.message).toBeNull();
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

it("rejects legacy prefixed Workflow IDs", () => {
  expect(() => workflowIDSchema.parse("workflow-11111111-1111-4111-8111-111111111111")).toThrow();
  expect(() =>
    attentionItemSchema.parse({
      ...baseAttentionItem,
      workflow_id: "workflow-11111111-1111-4111-8111-111111111111",
    }),
  ).toThrow();
});

describe("taskStatusSchema", () => {
  const status = {
    attention_types: [],
    kind: "queued",
    native_state: "queued",
    node_ids: [],
  };

  it("accepts every typed task status kind without a display label", () => {
    for (const kind of [
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

describe("validationErrorSchema", () => {
  const base = {
    code: "code",
    message: "message",
    blocks_context: true,
    edge_id: null,
    node_id: null,
    transition_group_id: null,
  };

  it("normalizes omitted or null role-tool details to null", () => {
    const omitted = validationErrorSchema.parse(base);
    const present = validationErrorSchema.parse({
      ...base,
      details: { role: "coder", required_tool: "ask_question" },
    });
    expect(omitted.details).toMatchObject({ role: null, requiredTool: null });
    expect(present.details).toMatchObject({ role: "coder", requiredTool: "ask_question" });
  });

  it("preserves absent graph identities as null", () => {
    expect(validationErrorSchema.parse(base)).toMatchObject({
      edgeID: null,
      nodeID: null,
      transitionGroupID: null,
      details: { providerEdgeID: null },
    });
  });

  it("rejects omitted, empty, or blank graph identity fields", () => {
    for (const field of ["edge_id", "node_id", "transition_group_id"] as const) {
      const omitted = { ...base };
      Reflect.deleteProperty(omitted, field);
      expect(() => validationErrorSchema.parse(omitted)).toThrow();
      for (const invalid of ["", " "]) {
        expect(() => validationErrorSchema.parse({ ...base, [field]: invalid })).toThrow();
      }
    }
    for (const invalid of ["", " "]) {
      expect(() =>
        validationErrorSchema.parse({ ...base, details: { provider_edge_id: invalid } }),
      ).toThrow();
    }
  });

  it("rejects present blank role-tool details", () => {
    for (const details of [{ role: "" }, { role: " " }, { required_tool: "" }, { required_tool: " " }]) {
      expect(() => validationErrorSchema.parse({ ...base, details })).toThrow();
    }
  });
});
