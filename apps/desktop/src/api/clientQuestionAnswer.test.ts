import { ApiClient } from "./client";
import { FakeRpcTransport } from "./fakeTransport";

describe("ApiClient task question answers", () => {
  it("serializes ordinary and approval payloads", async () => {
    const transport = new FakeRpcTransport([{ method: "workflow.task.question.answer", result: {} }]);
    const client = new ApiClient(transport);

    await client.answerQuestion({
      kind: "ordinary",
      clientRequestID: "req-ordinary",
      taskID: "task-1",
      runID: "run-1",
      askID: "ask-1",
      selectedOptionNumber: 2,
      freeformAnswer: "because",
    });
    await client.answerQuestion({
      kind: "approval",
      clientRequestID: "req-approval",
      taskID: "task-1",
      runID: "run-1",
      askID: "approval-1",
      decision: "allow_session",
      commentary: "trusted",
    });

    expect(transport.calls).toContainEqual({
      method: "workflow.task.question.answer",
      params: {
        client_request_id: "req-ordinary",
        task_id: "task-1",
        run_id: "run-1",
        ask_id: "ask-1",
        selected_option_number: 2,
        freeform_answer: "because",
      },
    });
    expect(transport.calls).toContainEqual({
      method: "workflow.task.question.answer",
      params: {
        client_request_id: "req-approval",
        task_id: "task-1",
        run_id: "run-1",
        ask_id: "approval-1",
        approval: { decision: "allow_session", commentary: "trusted" },
      },
    });
  });
});
