import type { PromptAnswerBatchInput, QuestionAnswerInput, QuestionAttentionItem } from "@/api";
import type { QuestionSelectionState } from "./TaskDetailQuestionState";

export type QuestionAnswerMutation = Readonly<{
  isPending: boolean;
  mutateAsync(
    input: QuestionAnswerInput,
    attempt: Readonly<{
      attention: QuestionAttentionItem;
      selection: QuestionSelectionState;
    }>,
  ): Promise<unknown>;
}>;

export function questionAnswerBatchInput(input: QuestionAnswerInput): PromptAnswerBatchInput {
  return {
    sessionID: input.sessionID,
    stepID: input.stepID,
    entries:
      input.kind === "approval"
        ? [
            {
              kind: "approval",
              promptID: input.promptID,
              decision: input.decision,
              commentary: optionalText(input.commentary),
            },
          ]
        : [
            {
              kind: "question",
              promptID: input.promptID,
              selectedOptionNumber: input.selectedOptionNumber,
              freeform: optionalText(input.freeformAnswer),
            },
          ],
  };
}

function optionalText(value: string): string | null {
  return value.trim().length === 0 ? null : value;
}
