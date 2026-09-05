import { ApiClient } from "./client";
import { FakeRpcTransport } from "@/test-support/api";
const sessionID = "session-1";
const stepID = "22222222-2222-4222-8222-222222222222";
const batchRequest = {
  sessionID,
  stepID,
  entries: [
    { kind: "question" as const, toolCallID: "q", selectedOptionNumber: 2, freeform: "because" },
    { kind: "approval" as const, toolCallID: "a", decision: "allow_once" as const, commentary: null },
    { kind: "declined" as const, toolCallID: "d" },
  ],
} as const;
const resolvedQuestion = { tool_call_id: "q", outcome: "resolved" } as const;
const skippedApproval = { tool_call_id: "a", outcome: "skipped" };
const resolvedDeclined = { tool_call_id: "d", outcome: "resolved" };
const results = [resolvedQuestion, skippedApproval, resolvedDeclined];
describe("ApiClient prompt answer batches", () => {
  it("encodes Tool Call keyed entries and parses Tool Call keyed outcomes", async () => {
    const transport = new FakeRpcTransport([{ method: "prompt.answerBatch", result: { results } }]);
    const client = new ApiClient(transport);
    const response = await client.answerPromptBatch(batchRequest);
    expect(transport.calls).toEqual([]);
    expect(transport.attachedSessionCalls).toEqual([
      {
        sessionID,
        method: "prompt.answerBatch",
        params: {
          session_id: sessionID,
          step_id: stepID,
          entries: [
            { tool_call_id: "q", question_answer: { selected_option_number: 2, freeform: "because" } },
            { tool_call_id: "a", approval_answer: { decision: "allow_once" } },
            { tool_call_id: "d", declined: {} },
          ],
        },
      },
    ]);
    expect(response.results).toEqual([
      { toolCallID: "q", outcome: "resolved" },
      { toolCallID: "a", outcome: "skipped" },
      { toolCallID: "d", outcome: "resolved" },
    ]);
  });
  it.each([
    ["missing", { results: [] }],
    ["foreign", { results: [{ tool_call_id: "foreign", outcome: "resolved" }] }],
    ["duplicate", { results: [resolvedQuestion, { ...resolvedQuestion, outcome: "skipped" }] }],
    [
      "whitespace-padded",
      { results: [{ ...resolvedQuestion, tool_call_id: " q " }, skippedApproval, resolvedDeclined] },
    ],
  ])("rejects %s result identity sets", async (_case, result) => {
    const transport = new FakeRpcTransport([{ method: "prompt.answerBatch", result }]);
    const client = new ApiClient(transport);
    await expect(client.answerPromptBatch(batchRequest)).rejects.toThrow();
    expect(transport.attachedSessionCalls).toHaveLength(1);
  });
});
