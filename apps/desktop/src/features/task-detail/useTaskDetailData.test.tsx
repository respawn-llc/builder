import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { appI18n } from "@/i18n";
import {
  mountTaskDetailSurface,
  questionAttention,
  taskDetailResponse,
  taskQuestionWaitingEvent,
} from "@/test-support/task-detail";

describe("Task Detail live refresh", () => {
  it("preserves progression and rejects an equal-timestamp older overlapping reconciliation", async () => {
    let attention = taskAttentionMany([
      ["ask-1", 1],
      ["ask-2", 1],
    ]);
    const first = deferred<undefined>();
    const second = deferred<undefined>();
    const staleAttention = { ...taskAttention("ask-2", 1), generated_at_unix_ms: 5 };
    const staleRead = deferred<ReturnType<typeof taskAttention>>();
    let answerCount = 0;
    let attentionReadCount = 0;
    mountTaskDetailSurface(taskDetailResponse, {
      routes: [
        {
          method: "workflow.task.attention.list",
          handler: (): ReturnType<typeof taskAttention> | Promise<ReturnType<typeof taskAttention>> => {
            attentionReadCount += 1;
            return attentionReadCount === 2 ? staleRead.promise : attention;
          },
        },
        {
          method: "prompt.answerBatch",
          handler: async () => {
            answerCount += 1;
            if (answerCount === 1) {
              await first.promise;
              attention = staleAttention;
              return { results: [{ prompt_id: "ask-1", outcome: "resolved" }] };
            }
            await second.promise;
            attention = { items: [], generated_at_unix_ms: 5 };
            return { results: [{ prompt_id: "ask-2", outcome: "resolved" }] };
          },
        },
      ],
    });
    const user = userEvent.setup();

    await waitFor(() => {
      expect(screen.getAllByRole("radio")).toHaveLength(4);
    });
    const list = screen.getByTestId("task-detail-island-stack");
    list.scrollTop = 241;
    const submits = screen.getAllByRole("button", { name: appI18n.t("task.submitAnswer") });
    await user.click(submits.reduce((first) => first));

    await waitFor(() => {
      expect(screen.queryByText("ask-1")).not.toBeInTheDocument();
      expect(screen.getByText("ask-2")).toBeInTheDocument();
      expect(screen.getAllByRole("radio")[0]).toHaveFocus();
      expect(list.scrollTop).toBe(241);
    });
    await user.click(screen.getByRole("button", { name: appI18n.t("task.submitAnswer") }));
    expect(answerCount).toBe(2);
    await waitFor(() => {
      expect(screen.queryAllByRole("radio")).toHaveLength(0);
    });

    first.resolve(undefined);
    await waitFor(() => {
      expect(attentionReadCount).toBe(2);
    });
    second.resolve(undefined);
    await waitFor(() => {
      expect(attentionReadCount).toBe(3);
    });
    await act(async () => {
      staleRead.resolve(staleAttention);
    });
    expect(screen.queryAllByRole("radio")).toHaveLength(0);
  });

  it("skips a masked prompt when handing focus to the next actionable question", async () => {
    mountTaskDetailSurface(taskDetailResponse, {
      routes: [
        {
          method: "workflow.task.attention.list",
          handler: () => taskAttentionWithOneOption("ask-1", "ask-2", "ask-3"),
        },
        {
          method: "prompt.answerBatch",
          handler: async () => new Promise(() => undefined),
        },
      ],
    });
    const user = userEvent.setup();
    await waitFor(() => {
      expect(screen.getAllByRole("radio")).toHaveLength(6);
    });

    const submits = screen.getAllByRole("button", { name: appI18n.t("task.submitAnswer") });
    await user.click(requiredElement(submits, 1));
    await waitFor(() => {
      expect(screen.queryByText("ask-2")).not.toBeInTheDocument();
    });
    await user.click(requiredElement(screen.getAllByRole("radio", { name: "option-1 (Recommended)" }), 0));
    await user.click(
      requiredElement(screen.getAllByRole("button", { name: appI18n.t("task.submitAnswer") }), 0),
    );

    await waitFor(() => {
      expect(screen.getByText("ask-3")).toBeInTheDocument();
      expect(screen.getAllByRole("radio")[0]).toHaveFocus();
    });
  });

  it("preserves an equal-timestamp background refresh over an earlier reconciliation", async () => {
    let attention = taskAttention("ask-1", 1);
    const delivery = deferred<undefined>();
    const staleRead = deferred<ReturnType<typeof taskAttention>>();
    const backgroundRead = deferred<ReturnType<typeof taskAttention>>();
    let attentionReadCount = 0;
    const services = mountTaskDetailSurface(taskDetailResponse, {
      routes: [
        {
          method: "workflow.task.attention.list",
          handler: (): ReturnType<typeof taskAttention> | Promise<ReturnType<typeof taskAttention>> => {
            attentionReadCount += 1;
            if (attentionReadCount === 2) return staleRead.promise;
            if (attentionReadCount === 3) return backgroundRead.promise;
            return attention;
          },
        },
        {
          method: "prompt.answerBatch",
          handler: async () => {
            await delivery.promise;
            return { results: [{ prompt_id: "ask-1", outcome: "resolved" }] };
          },
        },
      ],
    });
    const user = userEvent.setup();
    await waitForQuestionOptionCount(1);
    await user.click(screen.getByRole("button", { name: appI18n.t("task.submitAnswer") }));
    delivery.resolve(undefined);
    await waitFor(() => {
      expect(attentionReadCount).toBe(2);
    });

    await waitForProjectSubscription(() => services.transport.subscriptions);
    attention = { items: [], generated_at_unix_ms: 5 };
    act(() => {
      services.transport.emit("workflow.project", {
        event: { ...taskQuestionWaitingEvent.event, action: "question_cleared" },
      });
    });
    await waitFor(() => {
      expect(attentionReadCount).toBe(3);
    });
    await act(async () => {
      backgroundRead.resolve(attention);
    });
    await act(async () => {
      staleRead.resolve({ ...taskAttention("ask-1", 1), generated_at_unix_ms: 5 });
    });
    expect(screen.queryAllByRole("radio")).toHaveLength(0);
  });

  it("cancels a pre-answer background refresh before authoritative reconciliation", async () => {
    const delivery = deferred<undefined>();
    const backgroundRead = deferred<ReturnType<typeof taskAttention>>();
    const directRead = deferred<ReturnType<typeof taskAttention>>();
    let attentionReadCount = 0;
    const services = mountTaskDetailSurface(taskDetailResponse, {
      routes: [
        {
          method: "workflow.task.attention.list",
          handler: (): ReturnType<typeof taskAttention> | Promise<ReturnType<typeof taskAttention>> => {
            attentionReadCount += 1;
            if (attentionReadCount === 2) return backgroundRead.promise;
            if (attentionReadCount === 3) return directRead.promise;
            return taskAttention("ask-1", 1);
          },
        },
        {
          method: "prompt.answerBatch",
          handler: async () => {
            await delivery.promise;
            return { results: [{ prompt_id: "ask-1", outcome: "resolved" }] };
          },
        },
      ],
    });
    const user = userEvent.setup();
    await waitForQuestionOptionCount(1);
    await waitForProjectSubscription(() => services.transport.subscriptions);
    act(() => {
      services.transport.emit("workflow.project", {
        event: { ...taskQuestionWaitingEvent.event, action: "question_cleared" },
      });
    });
    await waitFor(() => {
      expect(attentionReadCount).toBe(2);
    });

    await user.click(screen.getByRole("button", { name: appI18n.t("task.submitAnswer") }));
    delivery.resolve(undefined);
    await waitFor(() => {
      expect(attentionReadCount).toBe(3);
    });
    await act(async () => {
      directRead.resolve({ items: [], generated_at_unix_ms: 5 });
    });
    await act(async () => {
      backgroundRead.resolve({ ...taskAttention("ask-1", 1), generated_at_unix_ms: 4 });
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(screen.queryAllByRole("radio")).toHaveLength(0);
  });

  it("does not jump focus or scroll when the intended next prompt disappears and the earlier prompt restores", async () => {
    let attention = taskAttentionWithOneOption("ask-1", "ask-2", "ask-3");
    const answer = deferred<undefined>();
    mountTaskDetailSurface(taskDetailResponse, {
      routes: [
        { method: "workflow.task.attention.list", handler: () => attention },
        { method: "prompt.answerBatch", handler: async () => answer.promise },
      ],
    });
    const user = userEvent.setup();
    await waitFor(() => {
      expect(screen.getAllByRole("radio")).toHaveLength(6);
    });
    const list = screen.getByTestId("task-detail-island-stack");
    list.scrollTop = 241;
    const submits = screen.getAllByRole("button", { name: appI18n.t("task.submitAnswer") });
    await user.click(submits.reduce((first) => first));
    await waitFor(() => {
      expect(screen.queryByText("ask-1")).not.toBeInTheDocument();
      expect(screen.getByText("ask-2")).toBeInTheDocument();
      expect(screen.getAllByRole("radio")[0]).toHaveFocus();
      expect(list.scrollTop).toBe(241);
    });
    list.scrollTop = 317;
    attention = taskAttentionWithOneOption("ask-1", "ask-3");
    answer.reject(new Error("delivery failed"));
    await waitFor(() => {
      expect(screen.getByText("ask-1")).toBeInTheDocument();
      expect(screen.queryByText("ask-2")).not.toBeInTheDocument();
      const radios = screen.getAllByRole("radio");
      expect(radios).toHaveLength(4);
      expect(radios[0]).not.toHaveFocus();
      expect(radios[2]).not.toHaveFocus();
      expect(list.scrollTop).toBe(317);
    });
  });

  it("shows the next batch question when its waiting event arrives after an answer", async () => {
    let attention = taskAttention("ask-1", 1);
    const services = mountTaskDetailSurface(taskDetailResponse, {
      routes: [
        {
          method: "workflow.task.attention.list",
          handler: () => attention,
        },
        {
          method: "prompt.answerBatch",
          handler: () => {
            attention = taskAttention("ask-2", 2);
            return { results: [{ prompt_id: "ask-1", outcome: "resolved" }] };
          },
        },
      ],
    });

    await waitForQuestionOptionCount(1);
    await waitForProjectSubscription(() => services.transport.subscriptions);
    const subscriptionStarts = projectSubscriptionStartCount(services.transport.subscriptionStarts);
    expect(subscriptionStarts).toBeGreaterThan(0);
    await services.api.answerPromptBatch({
      sessionID: "session-1",
      stepID: "22222222-2222-4222-8222-222222222222",
      entries: [
        {
          kind: "question",
          promptID: "ask-1",
          selectedOptionNumber: null,
          freeform: "answered",
        },
      ],
    });

    act(() => {
      services.transport.emit("workflow.project", taskQuestionWaitingEventFor("ask-2"));
    });

    await waitForQuestionOptionCount(2);
    expect(projectSubscriptionStartCount(services.transport.subscriptionStarts)).toBe(subscriptionStarts);
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
  return taskAttentionMany([[askID, optionCount]]);
}

function taskAttentionMany(prompts: readonly (readonly [string, number])[]) {
  return {
    items: prompts.map(([askID, optionCount]) => ({
      ...questionAttention,
      id: `attention-${askID}`,
      message: askID,
      question: {
        ...questionAttention.question,
        prompt_id: askID,
        suggestions: Array.from({ length: optionCount }, (_, index) => `option-${String(index + 1)}`),
      },
    })),
    generated_at_unix_ms: 3,
  };
}

const taskAttentionWithOneOption = (...askIDs: readonly string[]) =>
  taskAttentionMany(askIDs.map((askID) => [askID, 1] as const));

function projectSubscriptionStartCount(
  subscriptions: readonly Readonly<{ method: string; params: unknown }>[],
): number {
  return subscriptions.filter((subscription) => subscription.method === "workflow.subscribeProject").length;
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
  subscriptions: () => readonly Readonly<{ method: string; params: unknown }>[],
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

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((nextResolve, nextReject) => {
    [resolve, reject] = [nextResolve, nextReject];
  });
  return { promise, reject, resolve };
}

function requiredElement(elements: readonly HTMLElement[], index: number): HTMLElement {
  const element = elements[index];
  if (element === undefined) throw new Error(`Required test element ${index.toString()} is unavailable`);
  return element;
}
