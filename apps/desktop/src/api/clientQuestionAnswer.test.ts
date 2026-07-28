import { ApiClient } from "./client";
import { FakeRpcTransport } from "@/test-support/api";

describe("ApiClient task question answers", () => {
  it("addresses questions by Task and Question without a Run selector", async () => {
    const transport = new FakeRpcTransport([{ method: "workflow.task.question.answer", result: {} }]);
    const client = new ApiClient(transport);

    await client.answerQuestion({
      kind: "ordinary",
      clientRequestID: "request-1",
      taskID: "task-1",
      askID: "question-1",
      selectedOptionNumber: null,
      freeformAnswer: "because",
    });

    expect(transport.calls).toContainEqual({
      method: "workflow.task.question.answer",
      params: {
        client_request_id: "request-1",
        task_id: "task-1",
        ask_id: "question-1",
        selected_option_number: null,
        freeform_answer: "because",
      },
    });
  });
});
