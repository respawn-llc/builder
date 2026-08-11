import type { ReactNode } from "react";

import type { AttentionItem, TaskDetail } from "@/api";
import type { TaskDetailInitialFocus } from "@/app-facade";
import { LoadingState } from "@/ui";
import { TaskInbox } from "./TaskDetailInbox";
import type { PromptAnswerKey, PromptAnswerState } from "./PromptAnswerState";
import type { PromptPrimaryFocusRequest } from "./PromptPrimaryControlRegistry";
import type { QuestionAnswerMutation } from "./TaskDetailQuestionAnswer";
import type { QuestionSelectionState } from "./TaskDetailQuestionState";
import type { useTaskMutations } from "./useTaskDetailData";

export function TaskDetailInboxRow({
  answerQuestion,
  attentionItems,
  attentionPending,
  detail,
  disabled,
  initialFocus,
  mutations,
  onQuestionSelectionChange,
  primaryFocusRequest,
  promptAnswerState,
}: Readonly<{
  answerQuestion: QuestionAnswerMutation;
  attentionItems: readonly AttentionItem[];
  attentionPending: boolean;
  detail: TaskDetail;
  disabled: boolean;
  initialFocus?: TaskDetailInitialFocus | undefined;
  mutations: ReturnType<typeof useTaskMutations>;
  onQuestionSelectionChange: (key: PromptAnswerKey, selection: QuestionSelectionState) => void;
  primaryFocusRequest?: PromptPrimaryFocusRequest | undefined;
  promptAnswerState: PromptAnswerState;
}>): ReactNode {
  if (attentionPending) {
    return <LoadingState appearanceDelayMs={0} fullPage={false} reveal={false} title={undefined} />;
  }
  return (
    <TaskInbox
      attentionItems={attentionItems}
      answerQuestion={answerQuestion}
      currentVersion={detail.workflowVersion}
      detail={detail}
      disabled={disabled}
      initialFocus={initialFocus}
      mutations={mutations}
      onQuestionSelectionChange={onQuestionSelectionChange}
      primaryFocusRequest={primaryFocusRequest}
      promptAnswerState={promptAnswerState}
    />
  );
}
