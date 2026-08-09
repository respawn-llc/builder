import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ComponentProps, useState } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeAll, describe, expect, it, vi } from "vitest";

import type { PendingAsk, QuestionAnswerInput, QuestionAttentionItem } from "@/api";
import { AppServicesProvider, queryKeys } from "@/app-facade";
import { appI18n, initializeI18n } from "@/i18n";
import { createTestServices } from "@/test-support/app-services";
import { QuestionBox } from "./TaskDetailQuestionForm";
import {
  anchorQuestionSelection,
  emptyQuestionSelection,
  questionPresentation,
  withApprovalQuestionDecision,
  withOrdinaryQuestionOption,
} from "./TaskDetailQuestionState";
import { QuestionFormView } from "./TaskDetailQuestionFormView";

type QuestionAnswerMutation = ComponentProps<typeof QuestionFormView>["answerQuestion"];
type FixtureQuestionAttention = QuestionAttentionItem & Readonly<{ sessionID: string }>;

let questionAnswerMutation: QuestionAnswerMutation;
let listPendingAsks: (sessionID: string) => Promise<readonly PendingAsk[]>;

vi.mock("@/app-facade", async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return {
    ...actual,
    queryKeys: {
      pendingAsks: (sessionID: string | null) => ["pending-asks", sessionID],
    },
    useAppServices: () => ({
      api: { listPendingAsks },
    }),
  };
});

const shortcutServices = createTestServices([], undefined, { platform: "macos" });

beforeAll(async () => {
  await initializeI18n();
});

describe("questionPresentation", () => {
  it("anchors the valid recommendation and falls back to option one for absent or malformed metadata", () => {
    expect(
      anchorQuestionSelection(
        emptyQuestionSelection("ask-1"),
        questionPresentation(ordinaryAttention(["one", "two"], 2), undefined, false).defaultSelection,
      ),
    ).toMatchObject({
      provenance: "anchored-default",
      selectedOption: 2,
    });

    for (const recommendation of [0, 3, 1.5, -1]) {
      expect(
        anchorQuestionSelection(
          emptyQuestionSelection("ask-1"),
          questionPresentation(ordinaryAttention(["one", "two"], recommendation), undefined, false)
            .defaultSelection,
        ),
      ).toMatchObject({
        provenance: "anchored-default",
        selectedOption: 1,
      });
    }
  });

  it("waits for pending-ask hydration before anchoring an empty ordinary question", () => {
    const attention = ordinaryAttention([], 0);
    const uninitialized = emptyQuestionSelection(attention.questionID);
    const pendingPresentation = questionPresentation(attention, undefined, false);

    expect(pendingPresentation.defaultSelection).toBeNull();
    expect(anchorQuestionSelection(uninitialized, pendingPresentation.defaultSelection)).toBe(uninitialized);

    const pendingAsk: PendingAsk = {
      askID: attention.questionID,
      createdAt: "2026-07-23T00:00:00Z",
      question: attention.message,
      recommendedOptionIndex: 2,
      sessionID: attention.sessionID,
      suggestions: ["one", "two"],
    };
    expect(
      anchorQuestionSelection(
        uninitialized,
        questionPresentation(attention, pendingAsk, true).defaultSelection,
      ),
    ).toMatchObject({
      provenance: "anchored-default",
      selectedOption: 2,
    });
  });

  it("refetches a fresh cached no-match before anchoring", async () => {
    await expectPendingAskHydrationFromFreshCache();
  });

  it("does not look up pending asks when attention already supplies options", async () => {
    await expectCompleteAttentionDoesNotLookupPendingAsks();
  });

  it("anchors a settled freeform-only question without an option", () => {
    const attention = ordinaryAttention([], 0);
    expect(
      anchorQuestionSelection(
        emptyQuestionSelection(attention.questionID),
        questionPresentation(attention, undefined, true).defaultSelection,
      ),
    ).toMatchObject({
      provenance: "anchored-default",
      selectedOption: null,
    });
  });

  it("submits the displayed anchored option without a radio change", async () => {
    const attention = ordinaryAttention(["one", "two"], 2);
    const presentation = questionPresentation(attention, undefined, false);
    const selection = anchorQuestionSelection(
      emptyQuestionSelection(attention.questionID),
      presentation.defaultSelection,
    );
    const inputs: QuestionAnswerInput[] = [];
    const answerQuestion = {
      isPending: false,
      async mutateAsync(input: QuestionAnswerInput): Promise<void> {
        inputs.push(input);
      },
    } satisfies QuestionAnswerMutation;
    const user = userEvent.setup();

    renderQuestionForm(attention, presentation, selection, answerQuestion);

    const radios = screen.getAllByRole("radio");
    expect(radios[1]).toBeChecked();
    await user.click(screen.getByRole("button"));
    await waitFor(() => {
      expect(inputs).toHaveLength(1);
    });
    expect(inputs[0]).toMatchObject({
      kind: "ordinary",
      selectedOptionNumber: 2,
    });
  });

  it("activates an option but not Streamdown link descendants", async () => {
    const attention = ordinaryAttention(["ordinary option", "[safe](https://example.com)"], 0);
    const presentation = questionPresentation(attention, undefined, false);
    const user = userEvent.setup();

    renderQuestionForm(
      attention,
      presentation,
      emptyQuestionSelection(attention.questionID),
      recordingQuestionAnswerMutation([]),
    );

    const plainOption = screen.getByRole("radio", { name: "ordinary option" });
    await user.click(plainOption);
    expect(plainOption).toBeChecked();

    await user.click(screen.getByRole("button", { name: "safe" }));
    expect(screen.getAllByRole("radio")[1]).not.toBeChecked();
  });

  it.each([0, 3, 1.5, -1])(
    "submits option one when recommendation metadata is %s",
    async (recommendation) => {
      const attention = ordinaryAttention(["one", "two"], recommendation);
      const presentation = questionPresentation(attention, undefined, false);
      const selection = anchorQuestionSelection(
        emptyQuestionSelection(attention.questionID),
        presentation.defaultSelection,
      );
      const inputs: QuestionAnswerInput[] = [];
      const answerQuestion = recordingQuestionAnswerMutation(inputs);
      const user = userEvent.setup();

      renderQuestionForm(attention, presentation, selection, answerQuestion);

      expect(screen.getAllByRole("radio")[0]).toBeChecked();
      await user.click(screen.getByRole("button"));
      await waitFor(() => {
        expect(inputs).toHaveLength(1);
      });
      expect(inputs[0]).toMatchObject({
        kind: "ordinary",
        selectedOptionNumber: 1,
      });
    },
  );

  it("submits a settled freeform-only answer without an option", async () => {
    const attention = ordinaryAttention([], 0);
    const presentation = questionPresentation(attention, undefined, true);
    const selection = anchorQuestionSelection(
      emptyQuestionSelection(attention.questionID),
      presentation.defaultSelection,
    );
    const inputs: QuestionAnswerInput[] = [];
    const answerQuestion = recordingQuestionAnswerMutation(inputs);
    const user = userEvent.setup();

    renderQuestionForm(attention, presentation, selection, answerQuestion);

    expect(screen.queryByRole("radio")).not.toBeInTheDocument();
    await user.type(screen.getByRole("textbox"), "freeform");
    await user.click(screen.getByRole("button"));
    await waitFor(() => {
      expect(inputs).toHaveLength(1);
    });
    expect(inputs[0]).toMatchObject({
      kind: "ordinary",
      freeformAnswer: "freeform",
      selectedOptionNumber: null,
    });
  });

  it("anchors reordered approval decisions to allow once or the first decision", () => {
    expect(
      anchorQuestionSelection(
        emptyQuestionSelection("ask-1"),
        questionPresentation(approvalAttention(["deny", "allow_session", "allow_once"]), undefined, false)
          .defaultSelection,
      ),
    ).toMatchObject({
      approvalDecision: "allow_once",
      provenance: "anchored-default",
    });
    expect(
      anchorQuestionSelection(
        emptyQuestionSelection("ask-1"),
        questionPresentation(approvalAttention(["allow_session", "deny"]), undefined, false).defaultSelection,
      ),
    ).toMatchObject({
      approvalDecision: "allow_session",
      provenance: "anchored-default",
    });
  });

  it("does not rederive an anchored or explicit choice on refresh", () => {
    const ordinarySelection = anchorQuestionSelection(
      emptyQuestionSelection("ask-1"),
      questionPresentation(ordinaryAttention(["one", "two", "three"], 2), undefined, false).defaultSelection,
    );
    const refreshedOrdinary = questionPresentation(
      ordinaryAttention(["three", "one", "two"], 1),
      undefined,
      false,
    ).defaultSelection;
    expect(anchorQuestionSelection(ordinarySelection, refreshedOrdinary)).toBe(ordinarySelection);
    expect(
      anchorQuestionSelection(withOrdinaryQuestionOption(ordinarySelection, 3), refreshedOrdinary),
    ).toMatchObject({
      provenance: "explicit",
      selectedOption: 3,
    });

    const approvalSelection = anchorQuestionSelection(
      emptyQuestionSelection("ask-1"),
      questionPresentation(approvalAttention(["deny", "allow_session", "allow_once"]), undefined, false)
        .defaultSelection,
    );
    const refreshedApproval = questionPresentation(
      approvalAttention(["deny", "allow_once", "allow_session"]),
      undefined,
      false,
    ).defaultSelection;
    expect(anchorQuestionSelection(approvalSelection, refreshedApproval)).toBe(approvalSelection);
    expect(
      anchorQuestionSelection(withApprovalQuestionDecision(approvalSelection, "deny"), refreshedApproval),
    ).toMatchObject({
      approvalDecision: "deny",
      provenance: "explicit",
    });
  });

  it("retains an ordinary anchored choice across refresh, failure, and retry", async () => {
    const initialAttention = ordinaryAttention(["one", "two", "three"], 2);
    const initialPresentation = questionPresentation(initialAttention, undefined, false);
    const selection = anchorQuestionSelection(
      emptyQuestionSelection(initialAttention.questionID),
      initialPresentation.defaultSelection,
    );
    const inputs: QuestionAnswerInput[] = [];
    const answerQuestion = failingOnceQuestionAnswerMutation(inputs);
    const user = userEvent.setup();
    const view = renderQuestionForm(initialAttention, initialPresentation, selection, answerQuestion);

    await user.type(screen.getByRole("textbox"), "commentary");
    const refreshedAttention = ordinaryAttention(["three", "one", "two"], 1);
    view.rerender(
      questionFormTree(
        refreshedAttention,
        questionPresentation(refreshedAttention, undefined, false),
        selection,
        answerQuestion,
      ),
    );
    expect(screen.getAllByRole("radio")[1]).toBeChecked();

    const submit = screen.getByRole("button");
    await user.click(submit);
    await waitFor(() => {
      expect(inputs).toHaveLength(1);
      expect(submit).toBeEnabled();
    });

    const retriedAttention = ordinaryAttention(["two", "three", "one"], 3);
    view.rerender(
      questionFormTree(
        retriedAttention,
        questionPresentation(retriedAttention, undefined, false),
        selection,
        answerQuestion,
      ),
    );
    expect(screen.getAllByRole("radio")[1]).toBeChecked();
    await user.click(submit);
    await waitFor(() => {
      expect(inputs).toHaveLength(2);
    });

    expect(inputs).toEqual([
      expect.objectContaining({
        freeformAnswer: "commentary",
        kind: "ordinary",
        selectedOptionNumber: 2,
      }),
      expect.objectContaining({
        freeformAnswer: "commentary",
        kind: "ordinary",
        selectedOptionNumber: 2,
      }),
    ]);
  });

  it("retains an anchored approval decision across refresh, failure, and retry", async () => {
    const initialAttention = approvalAttention(["deny", "allow_session", "allow_once"]);
    const initialPresentation = questionPresentation(initialAttention, undefined, false);
    const selection = anchorQuestionSelection(
      emptyQuestionSelection(initialAttention.questionID),
      initialPresentation.defaultSelection,
    );
    const inputs: QuestionAnswerInput[] = [];
    const answerQuestion = failingOnceQuestionAnswerMutation(inputs);
    const user = userEvent.setup();
    const view = renderQuestionForm(initialAttention, initialPresentation, selection, answerQuestion);

    expect(screen.getAllByRole("radio")[2]).toBeChecked();
    await user.type(screen.getByRole("textbox"), "commentary");
    const refreshedAttention = approvalAttention(["allow_once", "deny", "allow_session"]);
    view.rerender(
      questionFormTree(
        refreshedAttention,
        questionPresentation(refreshedAttention, undefined, false),
        selection,
        answerQuestion,
      ),
    );
    expect(screen.getAllByRole("radio")[0]).toBeChecked();

    const submit = screen.getByRole("button");
    await user.click(submit);
    await waitFor(() => {
      expect(inputs).toHaveLength(1);
      expect(submit).toBeEnabled();
    });

    const retriedAttention = approvalAttention(["deny", "allow_once", "allow_session"]);
    view.rerender(
      questionFormTree(
        retriedAttention,
        questionPresentation(retriedAttention, undefined, false),
        selection,
        answerQuestion,
      ),
    );
    expect(screen.getAllByRole("radio")[1]).toBeChecked();
    await user.click(submit);
    await waitFor(() => {
      expect(inputs).toHaveLength(2);
    });

    expect(inputs).toEqual([
      expect.objectContaining({
        commentary: "commentary",
        decision: "allow_once",
        kind: "approval",
      }),
      expect.objectContaining({
        commentary: "commentary",
        decision: "allow_once",
        kind: "approval",
      }),
    ]);
  });
});

async function expectPendingAskHydrationFromFreshCache(): Promise<void> {
  const attention = ordinaryAttention([], 0);
  const hydratedAsk: PendingAsk = {
    askID: attention.questionID,
    createdAt: "2026-07-23T00:00:00Z",
    question: attention.message,
    recommendedOptionIndex: 2,
    sessionID: attention.sessionID,
    suggestions: ["one", "two"],
  };
  const inputs: QuestionAnswerInput[] = [];
  const selections: ReturnType<typeof emptyQuestionSelection>[] = [];
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        staleTime: 4_000,
      },
    },
  });
  queryClient.setQueryData(queryKeys.pendingAsks(attention.sessionID), []);
  const lookup = deferred<readonly PendingAsk[]>();
  const requestedSessionIDs: string[] = [];
  listPendingAsks = async (sessionID) => {
    requestedSessionIDs.push(sessionID);
    return lookup.promise;
  };
  questionAnswerMutation = recordingQuestionAnswerMutation(inputs);
  const user = userEvent.setup();

  const view = render(
    questionBoxTree(attention, queryClient, (selection) => {
      selections.push(selection);
    }),
  );

  await waitFor(() => {
    expect(requestedSessionIDs).toEqual([attention.sessionID]);
  });
  expect(selections).toHaveLength(0);
  expect(screen.queryByRole("radio")).toBeNull();

  lookup.resolve([hydratedAsk]);
  view.rerender(
    questionBoxTree(attention, queryClient, (selection) => {
      selections.push(selection);
    }),
  );

  await waitFor(() => {
    expect(screen.getAllByRole("radio")[1]).toBeChecked();
    expect(selections).toContainEqual(
      expect.objectContaining({
        provenance: "anchored-default",
        selectedOption: 2,
      }),
    );
  });

  await user.click(screen.getByRole("button"));
  await waitFor(() => {
    expect(inputs).toEqual([
      expect.objectContaining({
        kind: "ordinary",
        selectedOptionNumber: 2,
      }),
    ]);
  });
  queryClient.clear();
}

async function expectCompleteAttentionDoesNotLookupPendingAsks(): Promise<void> {
  const attention = ordinaryAttention(["one", "two"], 2);
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        staleTime: 4_000,
      },
    },
  });
  const lookup = deferred<readonly PendingAsk[]>();
  const requestedSessionIDs: string[] = [];
  listPendingAsks = async (sessionID) => {
    requestedSessionIDs.push(sessionID);
    return lookup.promise;
  };
  questionAnswerMutation = recordingQuestionAnswerMutation([]);

  const view = render(
    questionBoxTree(attention, queryClient, () => {
      return;
    }),
  );
  try {
    await waitFor(() => {
      expect(screen.getAllByRole("radio")[1]).toBeChecked();
    });
    await waitFor(() => {
      expect(queryClient.isFetching()).toBe(0);
    });
    expect(requestedSessionIDs).toEqual([]);
  } finally {
    lookup.resolve([]);
    view.unmount();
    queryClient.clear();
  }
}

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  resolve(value: T): void;
}> {
  let resolve: ((value: T) => void) | null = null;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return {
    promise,
    resolve(value): void {
      resolve?.(value);
    },
  };
}

function ordinaryAttention(
  suggestions: readonly string[],
  recommendedOptionIndex: number,
): FixtureQuestionAttention {
  return {
    questionID: "ask-1",
    id: "attention-1",
    kind: "question",
    message: "Choose an option",
    occurredAt: 0,
    projectID: "project-1",
    question: {
      kind: "ordinary",
      recommendedOptionIndex,
      suggestions,
    },
    recommendedOptionIndex,
    currentNode: {
      effectiveAssignee: null,
      effectiveThinking: null,
      nodeID: "node-1",
      transitionBranchKey: null,
      sessionID: "session-1",
    },
    sessionID: "session-1",
    sessionName: "Session one",
    suggestions,
    taskID: "task-1",
    taskShortID: "TASK-1",
    taskTitle: "Task",
    workflowID: "11111111-1111-4111-8111-111111111111",
  };
}

function approvalAttention(
  decisions: readonly ("allow_once" | "allow_session" | "deny")[],
): FixtureQuestionAttention {
  return {
    ...ordinaryAttention([], 0),
    question: {
      approvalDecisions: decisions,
      kind: "approval",
    },
  };
}

function QuestionFormHarness({
  answerQuestion,
  attention,
  initialSelection,
  presentation,
}: Readonly<{
  answerQuestion: QuestionAnswerMutation;
  attention: QuestionAttentionItem;
  initialSelection: ReturnType<typeof emptyQuestionSelection>;
  presentation: ReturnType<typeof questionPresentation>;
}>) {
  const [selection, setSelection] = useState(initialSelection);
  return (
    <QuestionFormView
      answerQuestion={answerQuestion}
      attention={attention}
      disabled={false}
      onSelectionStateChange={setSelection}
      presentation={presentation}
      selectionState={selection}
      taskId={attention.taskID}
    />
  );
}

function recordingQuestionAnswerMutation(inputs: QuestionAnswerInput[]): QuestionAnswerMutation {
  return {
    isPending: false,
    async mutateAsync(input: QuestionAnswerInput): Promise<void> {
      inputs.push(input);
    },
  };
}

function failingOnceQuestionAnswerMutation(inputs: QuestionAnswerInput[]): QuestionAnswerMutation {
  return {
    isPending: false,
    async mutateAsync(input: QuestionAnswerInput): Promise<void> {
      inputs.push(input);
      if (inputs.length === 1) {
        throw new Error("delivery failed");
      }
    },
  };
}

function renderQuestionForm(
  attention: QuestionAttentionItem,
  presentation: ReturnType<typeof questionPresentation>,
  selection: ReturnType<typeof emptyQuestionSelection>,
  answerQuestion: QuestionAnswerMutation,
): ReturnType<typeof render> {
  return render(questionFormTree(attention, presentation, selection, answerQuestion));
}

function questionFormTree(
  attention: QuestionAttentionItem,
  presentation: ReturnType<typeof questionPresentation>,
  selection: ReturnType<typeof emptyQuestionSelection>,
  answerQuestion: QuestionAnswerMutation,
) {
  return (
    <I18nextProvider i18n={appI18n}>
      <AppServicesProvider services={shortcutServices}>
        <QuestionFormHarness
          answerQuestion={answerQuestion}
          attention={attention}
          initialSelection={selection}
          presentation={presentation}
        />
      </AppServicesProvider>
    </I18nextProvider>
  );
}

function questionBoxTree(
  attention: QuestionAttentionItem,
  queryClient: QueryClient,
  onSelectionChange: (selection: ReturnType<typeof emptyQuestionSelection>) => void,
) {
  return (
    <I18nextProvider i18n={appI18n}>
      <QueryClientProvider client={queryClient}>
        <AppServicesProvider services={shortcutServices}>
          <QuestionBoxHarness attention={attention} onSelectionChange={onSelectionChange} />
        </AppServicesProvider>
      </QueryClientProvider>
    </I18nextProvider>
  );
}

function QuestionBoxHarness({
  attention,
  onSelectionChange,
}: Readonly<{
  attention: QuestionAttentionItem;
  onSelectionChange: (selection: ReturnType<typeof emptyQuestionSelection>) => void;
}>) {
  const [selection, setSelection] = useState(emptyQuestionSelection(attention.questionID));
  return (
    <QuestionBox
      attention={attention}
      answerQuestion={questionAnswerMutation}
      disabled={false}
      onSelectionStateChange={(nextSelection) => {
        setSelection(nextSelection);
        onSelectionChange(nextSelection);
      }}
      selectionState={selection}
      taskId={attention.taskID}
    />
  );
}
