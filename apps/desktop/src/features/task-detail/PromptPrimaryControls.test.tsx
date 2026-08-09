import { render, screen } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import type { QuestionAttentionItem } from "@/api";
import { AppServicesProvider } from "@/app-facade";
import { appI18n, initializeI18n } from "@/i18n";
import { createTestServices } from "@/test-support/app-services";
import { questionAttention } from "@/test-support/task-detail";
import { PromptPrimaryControlRegistry, type PromptPrimaryControl } from "./PromptPrimaryControlRegistry";
import type { PromptAnswerKey } from "./PromptAnswerState";
import { questionPresentation, emptyQuestionSelection } from "./TaskDetailQuestionState";
import { QuestionFormView } from "./TaskDetailQuestionFormView";

const services = createTestServices([], undefined, { platform: "macos" });
const baseQuestion = questionAttention as unknown as QuestionAttentionItem;

beforeAll(async () => {
  await initializeI18n();
});
afterEach(() => vi.restoreAllMocks());

describe("Task Detail prompt primary controls", () => {
  it("replaces and unregisters exact-key controls without fallback", () => {
    const registry = new PromptPrimaryControlRegistry();
    const key: PromptAnswerKey = { sessionID: "session-1", stepID: "step-1", promptID: "prompt-1" };
    const first = vi.fn();
    const second = vi.fn();
    const unregisterFirst = registry.register(key, { focusPrimary: first });
    const unregisterSecond = registry.register({ ...key }, { focusPrimary: second });
    unregisterFirst();
    expect(registry.focus({ ...key })).toBe(true);
    unregisterSecond();
    expect([second.mock.calls, first.mock.calls, registry.focus(key)]).toEqual([
      [[{ preventScroll: true }]],
      [],
      false,
    ]);
  });

  it.each([
    ["option Question", ordinaryAttention(["One"]), "radio"],
    ["freeform-only Question", ordinaryAttention([]), "textbox"],
    ["runtime Approval", approvalAttention(), "radio"],
  ] as const)("focuses the first answer control for a %s without scrolling", (_name, attention, role) => {
    let control: PromptPrimaryControl | undefined;
    const focus = vi.spyOn(HTMLElement.prototype, "focus");
    render(
      <I18nextProvider i18n={appI18n}>
        <AppServicesProvider services={services}>
          <QuestionFormView
            answerQuestion={{ isPending: false, mutateAsync: async () => undefined }}
            attention={attention}
            disabled={false}
            onSelectionStateChange={() => undefined}
            presentation={questionPresentation(attention)}
            registerPrimaryControl={(next) => {
              control = next;
              return () => undefined;
            }}
            selectionState={emptyQuestionSelection()}
          />
        </AppServicesProvider>
      </I18nextProvider>,
    );
    control?.focusPrimary({ preventScroll: true });
    expect(screen.getAllByRole(role)[0]).toHaveFocus();
    expect(focus).toHaveBeenCalledWith({ preventScroll: true });
  });
});

function ordinaryAttention(suggestions: readonly string[]): QuestionAttentionItem {
  return {
    ...baseQuestion,
    question: {
      ...baseQuestion.question,
      recommendedOptionIndex: suggestions.length === 0 ? null : 1,
      suggestions,
    },
  };
}

function approvalAttention(): QuestionAttentionItem {
  return {
    ...baseQuestion,
    question: {
      approvalDecisions: ["deny", "allow_once"],
      kind: "approval",
      promptID: baseQuestion.question.promptID,
      sessionID: baseQuestion.question.sessionID,
      stepID: baseQuestion.question.stepID,
    },
  };
}
