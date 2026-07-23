import { useEffect } from "react";
import { useTranslation } from "react-i18next";

import type { AttentionItem } from "@/api";
import { useOpenExternalLink } from "@/app-facade";
import { Island } from "@/ui";
import {
  anchorQuestionSelection,
  emptyQuestionSelection,
  questionPresentation,
  type QuestionSelectionState,
} from "./TaskDetailQuestionState";
import { QuestionFormView } from "./TaskDetailQuestionFormView";
import { usePendingAsks, type useTaskMutations } from "./useTaskDetailData";

export function QuestionBox({
  attention,
  disabled,
  mutations,
  selectionState,
  onSelectionStateChange,
  taskId,
}: Readonly<{
  attention: AttentionItem;
  disabled: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
  selectionState: QuestionSelectionState;
  onSelectionStateChange: (selection: QuestionSelectionState) => void;
  taskId: string;
}>) {
  const { t } = useTranslation();
  const asks = usePendingAsks(attention.question?.kind === "approval" ? null : attention.sessionID);
  const pendingAsk = asks.data?.find((ask) => ask.askID === attention.askID);
  const presentation = questionPresentation(attention, pendingAsk, asks.isSuccess);
  const selection = selectionForAsk(selectionState, attention.askID);
  const effectiveSelection = anchorQuestionSelection(selection, presentation.defaultSelection);
  const openLink = useOpenExternalLink();

  useEffect(() => {
    if (effectiveSelection !== selection) {
      onSelectionStateChange(effectiveSelection);
    }
  }, [effectiveSelection, onSelectionStateChange, selection]);

  return (
    <Island aria-label={t("task.question")} className="p-[var(--space-4)]" level={1} radius="l" unpadded>
      <QuestionFormView
        answerQuestion={mutations.answerQuestion}
        attention={attention}
        disabled={disabled}
        onOpenLink={openLink}
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
