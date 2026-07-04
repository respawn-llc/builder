import { attentionItemSchema } from "./common";

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
