import { ApiClient } from "./client";
import { FakeRpcTransport } from "@/test-support/api";

const sessionID = "session-1";
const stepID = "22222222-2222-4222-8222-222222222222";
const batchRequest = {
  sessionID,
  stepID,
  entries: [
    { kind: "question" as const, promptID: "q", selectedOptionNumber: 2, freeform: "because" },
    { kind: "approval" as const, promptID: "a", decision: "allow_once" as const, commentary: null },
    { kind: "declined" as const, promptID: "d" },
  ],
} as const;

describe("ApiClient prompt answer batches", () => {
  it("encodes Question, Approval, and Declined entries and parses identity-keyed outcomes", async () => {
    const results = [
      { prompt_id: "q", outcome: "resolved" },
      { prompt_id: "a", outcome: "skipped" },
      { prompt_id: "d", outcome: "resolved" },
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
            { prompt_id: "q", question_answer: { selected_option_number: 2, freeform: "because" } },
            { prompt_id: "a", approval_answer: { decision: "allow_once" } },
            { prompt_id: "d", declined: {} },
          ],
        },
      },
    ]);
    expect(response.results).toEqual([
      { promptID: "q", outcome: "resolved" },
      { promptID: "a", outcome: "skipped" },
      { promptID: "d", outcome: "resolved" },
    ]);
  });

  it.each([
    { results: [] },
    { results: [{ prompt_id: "foreign", outcome: "resolved" }] },
    {
      results: [
        { prompt_id: "q", outcome: "resolved" },
        { prompt_id: "q", outcome: "skipped" },
      ],
    },
  ])("rejects malformed result identity sets", async (result) => {
    const client = new ApiClient(new FakeRpcTransport([{ method: "prompt.answerBatch", result }]));
    await expect(client.answerPromptBatch(batchRequest)).rejects.toThrow();
  });
});
