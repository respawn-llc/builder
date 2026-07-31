import { act, screen, waitFor } from "@testing-library/react";

import {
  mountTaskDetailSurface,
  questionAttention,
  taskDetailResponse,
  taskQuestionWaitingEvent,
} from "@/test-support/task-detail";

describe("Task Detail live refresh", () => {
  it("shows the next batch question when its waiting event arrives after an answer", async () => {
    let attention = taskAttention("ask-1", 1);
    const services = mountTaskDetailSurface(taskDetailResponse, {
      routes: [
        {
          method: "workflow.task.attention.list",
          handler: () => attention,
        },
        {
          method: "workflow.task.question.answer",
          handler: () => {
            attention = taskAttention("ask-2", 2);
            return {};
          },
        },
      ],
    });

    await waitForQuestionOptionCount(1);
    await waitForProjectSubscription(() => services.transport.subscriptions);
    await services.api.answerQuestion({
      kind: "ordinary",
      clientRequestID: "request-1",
      taskID: "task-1",
      askID: "ask-1",
      selectedOptionNumber: null,
      freeformAnswer: "answered",
    });

    act(() => {
      services.transport.emit("workflow.project", taskQuestionWaitingEventFor("ask-2"));
    });

    await waitForQuestionOptionCount(2);
  });

  it("revalidates an open Task Detail when a reconnected project subscription opens", async () => {
    let attention = taskAttention("ask-1", 1);
    const services = mountTaskDetailSurface(taskDetailResponse, {
      routes: [
        {
          method: "workflow.task.attention.list",
          handler: () => attention,
        },
      ],
    });

    await waitForQuestionOptionCount(1);
    await waitForProjectSubscription(() => services.transport.subscriptions);
    act(() => {
      services.transport.connection.set("disconnected", "offline");
    });
    await waitFor(() => {
      expect(services.transport.subscriptions).not.toContainEqual({
        method: "workflow.subscribeProject",
        params: { project_id: "project-1" },
      });
    });

    attention = taskAttention("ask-2", 2);
    act(() => {
      services.transport.connection.set("connected");
    });
    await waitForProjectSubscription(() => services.transport.subscriptions);
    act(() => {
      services.transport.open("workflow.subscribeProject");
    });

    await waitForQuestionOptionCount(2);
  });
});

function taskAttention(askID: string, optionCount: number) {
  return {
    items: [
      {
        ...questionAttention,
        id: `attention-${askID}`,
        question_id: askID,
        message: askID,
        suggestions: Array.from({ length: optionCount }, (_, index) => `option-${String(index + 1)}`),
      },
    ],
    generated_at_unix_ms: 3,
  };
}

function taskQuestionWaitingEventFor(askID: string) {
  return {
    event: {
      ...taskQuestionWaitingEvent.event,
      related_ids: ["session-1", askID],
    },
  };
}

async function waitForProjectSubscription(
  subscriptions: () => ReadonlyArray<Readonly<{ method: string; params: unknown }>>,
) {
  await waitFor(() => {
    expect(subscriptions()).toContainEqual({
      method: "workflow.subscribeProject",
      params: { project_id: "project-1" },
    });
  });
}

async function waitForQuestionOptionCount(count: number) {
  await waitFor(() => {
    expect(screen.getAllByRole("radio")).toHaveLength(count + 1);
  });
}
