import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ComponentProps, useState } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeAll, describe, expect, it } from "vitest";

import type { AttentionItem, PendingAsk, QuestionAnswerInput } from "@/api";
import { appI18n, initializeI18n } from "@/i18n";
import {
  anchorQuestionSelection,
  emptyQuestionSelection,
  questionPresentation,
  withApprovalQuestionDecision,
  withOrdinaryQuestionOption,
} from "./TaskDetailQuestionState";
import { QuestionFormView } from "./TaskDetailQuestionFormView";

type QuestionAnswerMutation = ComponentProps<typeof QuestionFormView>["answerQuestion"];

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
    const uninitialized = emptyQuestionSelection(attention.askID);
    const pendingPresentation = questionPresentation(attention, undefined, false);

    expect(pendingPresentation.defaultSelection).toBeNull();
    expect(anchorQuestionSelection(uninitialized, pendingPresentation.defaultSelection)).toBe(uninitialized);

    const pendingAsk: PendingAsk = {
      askID: attention.askID,
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

  it("anchors a settled freeform-only question without an option", () => {
    const attention = ordinaryAttention([], 0);
    expect(
      anchorQuestionSelection(
        emptyQuestionSelection(attention.askID),
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
      emptyQuestionSelection(attention.askID),
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

  it.each([0, 3, 1.5, -1])(
    "submits option one when recommendation metadata is %s",
    async (recommendation) => {
      const attention = ordinaryAttention(["one", "two"], recommendation);
      const presentation = questionPresentation(attention, undefined, false);
      const selection = anchorQuestionSelection(
        emptyQuestionSelection(attention.askID),
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
      emptyQuestionSelection(attention.askID),
      presentation.defaultSelection,
    );
    const inputs: QuestionAnswerInput[] = [];
    const answerQuestion = recordingQuestionAnswerMutation(inputs);
    const user = userEvent.setup();

    renderQuestionForm(attention, presentation, selection, answerQuestion);

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
        questionPresentation(
          approvalAttention(["deny", "allow_session", "allow_once"]),
          undefined,
          false,
        ).defaultSelection,
      ),
    ).toMatchObject({
      approvalDecision: "allow_once",
      provenance: "anchored-default",
    });
    expect(
      anchorQuestionSelection(
        emptyQuestionSelection("ask-1"),
        questionPresentation(approvalAttention(["allow_session", "deny"]), undefined, false)
          .defaultSelection,
      ),
    ).toMatchObject({
      approvalDecision: "allow_session",
      provenance: "anchored-default",
    });
  });

  it("does not rederive an anchored or explicit choice on refresh", () => {
    const ordinarySelection = anchorQuestionSelection(
      emptyQuestionSelection("ask-1"),
      questionPresentation(ordinaryAttention(["one", "two", "three"], 2), undefined, false)
        .defaultSelection,
    );
    const refreshedOrdinary = questionPresentation(
      ordinaryAttention(["three", "one", "two"], 1),
      undefined,
      false,
    ).defaultSelection;
    expect(anchorQuestionSelection(ordinarySelection, refreshedOrdinary)).toBe(ordinarySelection);
    expect(
      anchorQuestionSelection(
        withOrdinaryQuestionOption(ordinarySelection, 3),
        refreshedOrdinary,
      ),
    ).toMatchObject({
      provenance: "explicit",
      selectedOption: 3,
    });

    const approvalSelection = anchorQuestionSelection(
      emptyQuestionSelection("ask-1"),
      questionPresentation(
        approvalAttention(["deny", "allow_session", "allow_once"]),
        undefined,
        false,
      ).defaultSelection,
    );
    const refreshedApproval = questionPresentation(
      approvalAttention(["deny", "allow_once", "allow_session"]),
      undefined,
      false,
    ).defaultSelection;
    expect(anchorQuestionSelection(approvalSelection, refreshedApproval)).toBe(approvalSelection);
    expect(
      anchorQuestionSelection(
        withApprovalQuestionDecision(approvalSelection, "deny"),
        refreshedApproval,
      ),
    ).toMatchObject({
      approvalDecision: "deny",
      provenance: "explicit",
    });
  });

  it("retains an ordinary anchored choice and request identity across refresh, failure, and retry", async () => {
    const initialAttention = ordinaryAttention(["one", "two", "three"], 2);
    const initialPresentation = questionPresentation(initialAttention, undefined, false);
    const selection = anchorQuestionSelection(
      emptyQuestionSelection(initialAttention.askID),
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
    expectSameQuestionRequestID(inputs);
  });

  it("retains an anchored approval decision and request identity across refresh, failure, and retry", async () => {
    const initialAttention = approvalAttention(["deny", "allow_session", "allow_once"]);
    const initialPresentation = questionPresentation(initialAttention, undefined, false);
    const selection = anchorQuestionSelection(
      emptyQuestionSelection(initialAttention.askID),
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
    expectSameQuestionRequestID(inputs);
  });
});

function ordinaryAttention(
  suggestions: readonly string[],
  recommendedOptionIndex: number,
): AttentionItem {
  return {
    approvalSnapshot: null,
    askID: "ask-1",
    detailJSON: "",
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
    runID: "run-1",
    sessionID: "session-1",
    suggestions,
    taskID: "task-1",
    taskShortID: "TASK-1",
    taskTitle: "Task",
    taskTransitionID: "",
    workflowID: "workflow-1",
  };
}

function approvalAttention(decisions: readonly ("allow_once" | "allow_session" | "deny")[]): AttentionItem {
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
  attention: AttentionItem;
  initialSelection: ReturnType<typeof emptyQuestionSelection>;
  presentation: ReturnType<typeof questionPresentation>;
}>) {
  const [selection, setSelection] = useState(initialSelection);
  return (
    <QuestionFormView
      answerQuestion={answerQuestion}
      attention={attention}
      disabled={false}
      onOpenLink={() => {
        return;
      }}
      onSelectionStateChange={setSelection}
      presentation={presentation}
      selectionState={selection}
      taskId={attention.taskID}
    />
  );
}

function recordingQuestionAnswerMutation(
  inputs: QuestionAnswerInput[],
): QuestionAnswerMutation {
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

function expectSameQuestionRequestID(inputs: readonly QuestionAnswerInput[]): void {
  const [first, second] = inputs;
  if (first === undefined || second === undefined) {
    throw new Error("expected two question-answer inputs");
  }
  expect(second.clientRequestID).toBe(first.clientRequestID);
}

function renderQuestionForm(
  attention: AttentionItem,
  presentation: ReturnType<typeof questionPresentation>,
  selection: ReturnType<typeof emptyQuestionSelection>,
  answerQuestion: QuestionAnswerMutation,
): ReturnType<typeof render> {
  return render(questionFormTree(attention, presentation, selection, answerQuestion));
}

function questionFormTree(
  attention: AttentionItem,
  presentation: ReturnType<typeof questionPresentation>,
  selection: ReturnType<typeof emptyQuestionSelection>,
  answerQuestion: QuestionAnswerMutation,
) {
  return (
    <I18nextProvider i18n={appI18n}>
      <QuestionFormHarness
        answerQuestion={answerQuestion}
        attention={attention}
        initialSelection={selection}
        presentation={presentation}
      />
    </I18nextProvider>
  );
}
