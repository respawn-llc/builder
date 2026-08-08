import { useEffect } from "react";
import { useTranslation } from "react-i18next";

import type { QuestionAttentionItem } from "@/api";
import { Island } from "@/ui";
import {
  anchorQuestionSelection,
  emptyQuestionSelection,
  questionPresentation,
  type QuestionSelectionState,
} from "./TaskDetailQuestionState";
import { QuestionFormView } from "./TaskDetailQuestionFormView";
import type { QuestionAnswerMutation } from "./TaskDetailQuestionAnswer";
import { usePendingAsks } from "./useTaskDetailData";

export function QuestionBox({
  attention,
  answerQuestion,
  disabled,
  selectionState,
  onSelectionStateChange,
  taskId,
}: Readonly<{
  attention: QuestionAttentionItem;
  answerQuestion: QuestionAnswerMutation;
  disabled: boolean;
  selectionState: QuestionSelectionState;
  onSelectionStateChange: (selection: QuestionSelectionState) => void;
  taskId: string;
}>) {
  const { t } = useTranslation();
  const pendingAskSessionID =
    attention.question?.kind === "approval" || attention.suggestions.length > 0
      ? null
      : attention.sessionID;
  const asks = usePendingAsks(pendingAskSessionID);
  const pendingAsk = asks.data?.find((ask) => ask.askID === attention.questionID);
  const pendingAskLookupSettled =
    asks.isSuccess && asks.isFetchedAfterMount && !asks.isFetching;
  const presentation = questionPresentation(attention, pendingAsk, pendingAskLookupSettled);
  const selection = selectionForAsk(selectionState, attention.questionID);
  const effectiveSelection = anchorQuestionSelection(selection, presentation.defaultSelection);
  useEffect(() => {
    if (effectiveSelection !== selection) {
      onSelectionStateChange(effectiveSelection);
    }
  }, [effectiveSelection, onSelectionStateChange, selection]);

  return (
    <Island aria-label={t("task.question")} className="p-[var(--space-4)]" level={1} radius="l" unpadded>
      <QuestionFormView
        answerQuestion={answerQuestion}
        attention={attention}
        disabled={disabled}
        onSelectionStateChange={onSelectionStateChange}
        presentation={presentation}
        selectionState={effectiveSelection}
        taskId={taskId}
      />
    </Island>
  );
}

function selectionForAsk(selection: QuestionSelectionState, askID: string): QuestionSelectionState {
  return selection.askID === askID ? selection : emptyQuestionSelection(askID);
}
