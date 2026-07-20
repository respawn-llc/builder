import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { vi } from "vitest";

import {
  callParams,
  getCallCount,
  isJsonObject,
  mountTaskDetailSurface,
  pendingAskResponse,
  questionAttention,
  taskAttentionResponse,
  taskDetailResponse,
} from "@/test-support/task-detail";
import { showStatusToast, type StatusNotice } from "@/ui";
import type * as uiModule from "@/ui";

const statusToastHarness = vi.hoisted(() => ({
  notices: new Map<string, StatusNotice>(),
}));

vi.mock("@/ui", async (importOriginal) => {
  const actual = await importOriginal<typeof uiModule>();
  return {
    ...actual,
    showStatusToast: vi.fn((notice: StatusNotice) => {
      statusToastHarness.notices.set(notice.id, notice);
    }),
    Toaster: () => null,
  };
});

describe("Task Question answers", () => {
  beforeEach(() => {
    statusToastHarness.notices.clear();
    vi.mocked(showStatusToast).mockClear();
  });

  it("hides an answered Question and shows Running before the answer request finishes", async () => {
    const answer = deferred<undefined>();
    const waitingQuestionTask = {
      task: {
        ...taskDetailResponse.task,
        status: {
          ...taskDetailResponse.task.status,
          kind: "waiting_question",
          native_state: "waiting_ask",
          attention_types: ["question"],
        },
        attention_count: 1,
        transitions: [],
      },
    };
    const services = mountTaskDetailSurface(waitingQuestionTask, {
      asks: pendingAskResponse,
      attention: {
        ...taskAttentionResponse,
        items: [questionAttention],
      },
      routes: [
        {
          method: "workflow.task.question.answer",
          handler: async () => answer.promise,
        },
      ],
    });

    const question = await screen.findByRole("region", { name: "Question" });
    expect(within(screen.getByRole("region", { name: "Properties" })).getByText("Waiting for question"))
      .toBeInTheDocument();

    fireEvent.click(within(question).getByRole("button", { name: "Submit answer" }));

    expect(screen.queryByRole("region", { name: "Question" })).not.toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "Properties" })).getByText("Running"))
      .toBeInTheDocument();
    await waitFor(() => {
      expect(services.transport.calls.some((call) => call.method === "workflow.task.question.answer")).toBe(
        true,
      );
    });

    await act(async () => {
      answer.resolve(undefined);
      await answer.promise;
    });
  });

  it("keeps the waiting status while another Question still needs an answer", async () => {
    const answer = deferred<undefined>();
    const waitingQuestionTask = {
      task: {
        ...taskDetailResponse.task,
        status: {
          ...taskDetailResponse.task.status,
          kind: "waiting_question",
          native_state: "waiting_ask",
          attention_types: ["question"],
        },
        attention_count: 2,
        transitions: [],
      },
    };
    mountTaskDetailSurface(waitingQuestionTask, {
      asks: { Asks: [] },
      attention: {
        ...taskAttentionResponse,
        items: [
          { ...questionAttention, id: "attention-ask-1", ask_id: "ask-1", message: "First Question" },
          { ...questionAttention, id: "attention-ask-2", ask_id: "ask-2", message: "Second Question" },
        ],
      },
      routes: [
        {
          method: "workflow.task.question.answer",
          handler: async () => answer.promise,
        },
      ],
    });
    const questions = await screen.findAllByRole("region", { name: "Question" });
    const firstQuestion = questions[0];
    if (firstQuestion === undefined) {
      throw new Error("Expected the first pending Question.");
    }

    fireEvent.click(within(firstQuestion).getByRole("button", { name: "Submit answer" }));

    expect(screen.getAllByRole("region", { name: "Question" })).toHaveLength(1);
    expect(screen.getByText("Second Question")).toBeInTheDocument();
    expect(
      within(screen.getByRole("region", { name: "Properties" })).getByText("Waiting for question"),
    ).toBeInTheDocument();

    await act(async () => {
      answer.resolve(undefined);
      await answer.promise;
    });
  });

  it("does not start local reads after an answer succeeds", async () => {
    const services = mountTaskDetailSurface(taskDetailResponse, {
      asks: pendingAskResponse,
      attention: {
        ...taskAttentionResponse,
        items: [questionAttention],
      },
      routes: [{ method: "workflow.task.question.answer", result: {} }],
    });
    const question = await screen.findByRole("region", { name: "Question" });
    const readMethods = [
      "workflow.task.get",
      "workflow.task.attention.list",
      "workflow.task.activity.list",
      "workflow.task.comment.list",
    ] as const;
    const callsBeforeAnswer = new Map(
      readMethods.map((method) => [method, getCallCount(services.transport.calls, method)]),
    );

    fireEvent.click(within(question).getByRole("button", { name: "Submit answer" }));

    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.question.answer")).toBe(1);
    });
    await act(async () => {
      await Promise.resolve();
    });
    for (const method of readMethods) {
      expect(getCallCount(services.transport.calls, method)).toBe(callsBeforeAnswer.get(method));
    }
  });

  it("restores exact choices after failure and safely reuses or rotates the request ID", async () => {
    const services = mountTaskDetailSurface(taskDetailResponse, {
      asks: pendingAskResponse,
      attention: {
        ...taskAttentionResponse,
        items: [questionAttention],
      },
      routes: [
        {
          method: "workflow.task.question.answer",
          handler: (_params, callIndex) => {
            if (callIndex < 2) {
              throw new Error("connection lost");
            }
            return {};
          },
        },
      ],
    });
    let question = await screen.findByRole("region", { name: "Question" });
    fireEvent.click(within(question).getByRole("radio", { name: "Neither" }));
    fireEvent.change(within(question).getByRole("textbox", { name: "Commentary" }), {
      target: { value: "Keep this exact answer." },
    });

    fireEvent.click(within(question).getByRole("button", { name: "Submit answer" }));

    question = await screen.findByRole("region", { name: "Question" });
    expect(within(question).getByRole("radio", { name: "Neither" })).toBeChecked();
    expect(within(question).getByRole("textbox", { name: "Commentary" })).toHaveValue(
      "Keep this exact answer.",
    );
    expect(within(question).getByRole("button", { name: "Submit answer" })).toBeEnabled();
    expect(statusToastHarness.notices.get("task-question-answer-failed:ask-1")?.body).toBe(
      "connection lost",
    );
    const firstRequestID = callParams(
      services.transport.calls,
      "workflow.task.question.answer",
    ).client_request_id;

    fireEvent.click(within(question).getByRole("button", { name: "Submit answer" }));

    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.question.answer")).toBe(2);
    });
    const secondRequest = services.transport.calls.filter(
      (call) => call.method === "workflow.task.question.answer",
    )[1];
    expect(isJsonObject(secondRequest?.params) ? secondRequest.params.client_request_id : null).toBe(
      firstRequestID,
    );

    question = await screen.findByRole("region", { name: "Question" });
    fireEvent.change(within(question).getByRole("textbox", { name: "Commentary" }), {
      target: { value: "Changed answer." },
    });
    fireEvent.click(within(question).getByRole("button", { name: "Submit answer" }));

    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.question.answer")).toBe(3);
    });
    const thirdRequest = services.transport.calls.filter(
      (call) => call.method === "workflow.task.question.answer",
    )[2];
    expect(isJsonObject(thirdRequest?.params) ? thirdRequest.params.client_request_id : null).not.toBe(
      firstRequestID,
    );
  });
});

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  reject: (reason?: unknown) => void;
  resolve: (value: T) => void;
}> {
  let rejectPromise: (reason?: unknown) => void = () => undefined;
  let resolvePromise: (value: T) => void = () => undefined;
  const promise = new Promise<T>((resolve, reject) => {
    rejectPromise = reject;
    resolvePromise = resolve;
  });
  return { promise, reject: rejectPromise, resolve: resolvePromise };
}
