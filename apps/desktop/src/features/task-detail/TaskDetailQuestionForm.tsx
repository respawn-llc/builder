import { useEffect } from "react";
import { useTranslation } from "react-i18next";

import type { QuestionAttentionItem } from "@/api";
import { Island } from "@/ui";
import {
  anchorQuestionSelection,
  questionPresentation,
  type QuestionSelectionState,
} from "./TaskDetailQuestionState";
import { QuestionFormView } from "./TaskDetailQuestionFormView";
import type { QuestionAnswerMutation } from "./TaskDetailQuestionAnswer";
import type { PromptPrimaryControl } from "./PromptPrimaryControlRegistry";
import { taskDetailIslandRadius } from "./taskDetailIslandStyles";

export function QuestionBox({
  attention,
  answerQuestion,
  disabled,
  selectionState,
  onSelectionStateChange,
  registerPrimaryControl,
}: Readonly<{
  attention: QuestionAttentionItem;
  answerQuestion: QuestionAnswerMutation;
  disabled: boolean;
  selectionState: QuestionSelectionState;
  onSelectionStateChange: (selection: QuestionSelectionState) => void;
  registerPrimaryControl?: ((control: PromptPrimaryControl) => () => void) | undefined;
}>) {
  const { t } = useTranslation();
  const presentation = questionPresentation(attention);
  const effectiveSelection = anchorQuestionSelection(selectionState, presentation.defaultSelection);
  useEffect(() => {
    if (effectiveSelection !== selectionState) {
      onSelectionStateChange(effectiveSelection);
    }
  }, [effectiveSelection, onSelectionStateChange, selectionState]);

  return (
    <Island
      aria-label={t("task.question")}
      className="p-[var(--space-4)]"
      level={1}
      radius={taskDetailIslandRadius}
      unpadded
    >
      <QuestionFormView
        answerQuestion={answerQuestion}
        attention={attention}
        disabled={disabled}
        onSelectionStateChange={onSelectionStateChange}
        presentation={presentation}
        registerPrimaryControl={registerPrimaryControl}
        selectionState={effectiveSelection}
      />
    </Island>
  );
}
