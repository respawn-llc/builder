import { ApiClient } from "./client";
import { FakeRpcTransport } from "@/test-support/api";

const sessionID = "session-1";
const stepID = "22222222-2222-4222-8222-222222222222";
const batchRequest = {
  sessionID,
  stepID,
  entries: [
    {
      kind: "question" as const,
      promptID: "question-1",
      selectedOptionNumber: 2,
      freeform: "because",
    },
    { kind: "approval" as const, promptID: "approval-1", decision: "allow_once" as const, commentary: null },
    { kind: "declined" as const, promptID: "declined-1" },
  ],
} as const;

describe("ApiClient prompt answer batches", () => {
  it("encodes Question, Approval, and Declined entries and parses identity-keyed outcomes", async () => {
    const results = [
      { prompt_id: "question-1", outcome: "resolved" },
      { prompt_id: "approval-1", outcome: "skipped" },
      { prompt_id: "declined-1", outcome: "resolved" },
    ];
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
            {
              prompt_id: "question-1",
              question_answer: { selected_option_number: 2, freeform: "because" },
            },
            {
              prompt_id: "approval-1",
              approval_answer: { decision: "allow_once" },
            },
            { prompt_id: "declined-1", declined: {} },
          ],
        },
      },
    ]);
    expect(response.results).toEqual([
      { promptID: "question-1", outcome: "resolved" },
      { promptID: "approval-1", outcome: "skipped" },
      { promptID: "declined-1", outcome: "resolved" },
    ]);
  });

  it.each([
    { results: [] },
    { results: [{ prompt_id: "foreign", outcome: "resolved" }] },
    {
      results: [
        { prompt_id: "question-1", outcome: "resolved" },
        { prompt_id: "question-1", outcome: "skipped" },
      ],
    },
  ])("rejects malformed result identity sets", async (result) => {
    const client = new ApiClient(new FakeRpcTransport([{ method: "prompt.answerBatch", result }]));
    await expect(client.answerPromptBatch(batchRequest)).rejects.toThrow();
  });
});
