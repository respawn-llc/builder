import { render, screen } from "@testing-library/react";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeAll, describe, expect, it, vi } from "vitest";

import type { QuestionAttentionItem } from "@/api";
import { AppServicesProvider } from "@/app-facade";
import { appI18n, initializeI18n } from "@/i18n";
import { createTestServices } from "@/test-support/app-services";
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
  return {
    id: "attention-1",
    kind: "question",
    message: "Question",
    occurredAt: 0,
    projectID: "project-1",
    question: {
      kind: "ordinary",
      promptID: "prompt-1",
      recommendedOptionIndex: suggestions.length === 0 ? null : 1,
      sessionID: "session-1",
      stepID: "step-1",
      suggestions,
    },
    currentNode: {
      effectiveAssignee: null,
      effectiveThinking: null,
      nodeID: "node-1",
      sessionID: null,
      transitionBranchKey: null,
    },
    sessionName: "Session",
    taskID: "task-1",
    taskShortID: "TASK-1",
    taskTitle: "Task",
    workflowID: "workflow-1",
  };
}

function approvalAttention(): QuestionAttentionItem {
  return {
    ...ordinaryAttention([]),
    question: {
      approvalDecisions: ["deny", "allow_once"],
      kind: "approval",
      promptID: "prompt-1",
      sessionID: "session-1",
      stepID: "step-1",
    },
  };
}
