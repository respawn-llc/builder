import { render, screen } from "@testing-library/react";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeAll, describe, expect, it, vi } from "vitest";

import type { QuestionAttentionItem } from "@/api";
import { attentionItemSchema } from "@/api/schemas/common";
import { AppServicesProvider } from "@/app-facade";
import { appI18n, initializeI18n } from "@/i18n";
import { createTestServices } from "@/test-support/app-services";
import { questionAttention } from "@/test-support/task-detail";
import type { PromptPrimaryControl } from "./PromptPrimaryControlRegistry";
import { questionPresentation, emptyQuestionSelection } from "./TaskDetailQuestionState";
import { QuestionFormView } from "./TaskDetailQuestionFormView";

const services = createTestServices([], undefined, { platform: "macos" });

beforeAll(async () => {
  await initializeI18n();
});

describe("Task Detail prompt primary controls", () => {
  it.each([
    ["option Question", ordinaryAttention(["One"]), "radio"],
    ["freeform-only Question", ordinaryAttention([]), "textbox"],
    ["runtime Approval", approvalAttention(), "radio"],
  ] as const)("focuses the first answer control for a %s without scrolling", (_name, attention, role) => {
    let control: PromptPrimaryControl | undefined;
    const focus = vi.spyOn(HTMLElement.prototype, "focus");
    try {
      render(
        <Harness
          attention={attention}
          register={(next) => {
            control = next;
            return () => {
              if (control === next) {
                control = undefined;
              }
            };
          }}
        />,
      );

      if (control !== undefined) {
        control.focusPrimary({ preventScroll: true });
      }

      expect(screen.getAllByRole(role)[0]).toHaveFocus();
      expect(focus).toHaveBeenCalledWith({ preventScroll: true });
    } finally {
      focus.mockRestore();
    }
  });
});

function Harness({
  attention,
  register,
}: Readonly<{
  attention: QuestionAttentionItem;
  register(control: PromptPrimaryControl): () => void;
}>) {
  const [selection, setSelection] = useState(emptyQuestionSelection());
  return (
    <I18nextProvider i18n={appI18n}>
      <AppServicesProvider services={services}>
        <QuestionFormView
          answerQuestion={{ isPending: false, mutateAsync: async () => undefined }}
          attention={attention}
          disabled={false}
          onSelectionStateChange={setSelection}
          presentation={questionPresentation(attention)}
          registerPrimaryControl={register}
          selectionState={selection}
        />
      </AppServicesProvider>
    </I18nextProvider>
  );
}

function ordinaryAttention(suggestions: readonly string[]): QuestionAttentionItem {
  const attention = baseQuestionAttention();
  return {
    ...attention,
    question: {
      ...attention.question,
      recommendedOptionIndex: suggestions.length === 0 ? null : 1,
      suggestions,
    },
  };
}

function approvalAttention(): QuestionAttentionItem {
  const attention = baseQuestionAttention();
  return {
    ...attention,
    question: {
      approvalDecisions: ["deny", "allow_once"],
      kind: "approval",
      promptID: attention.question.promptID,
      sessionID: attention.question.sessionID,
      stepID: attention.question.stepID,
    },
  };
}

function baseQuestionAttention(): QuestionAttentionItem {
  const attention = attentionItemSchema.parse(questionAttention);
  if (attention.kind !== "question") {
    throw new Error("expected Question attention fixture");
  }
  return attention;
}
