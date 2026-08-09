import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { AttentionItem, TaskDetail } from "@/api";
import type { TaskDetailInitialFocus } from "@/app-facade";
import { sameTaskDetailInitialFocus } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { ApprovalBox, InterruptedCurrentNodeBox, QuestionBox } from "./TaskDetailAttention";
import { taskDetailAttentionRowKey } from "./TaskDetailAttentionRowKey";
import { emptyQuestionSelection, type QuestionSelectionState } from "./TaskDetailQuestionState";
import { promptAnswerKey, type PromptAnswerKey, type PromptAnswerState } from "./PromptAnswerState";
import { PromptPrimaryControlRegistry, type PromptPrimaryFocusRequest } from "./PromptPrimaryControlRegistry";
import type { useTaskMutations } from "./useTaskDetailData";
import type { QuestionAnswerMutation } from "./TaskDetailQuestionAnswer";

export function TaskInbox({
  answerQuestion,
  attentionItems,
  currentVersion,
  detail,
  disabled,
  initialFocus,
  mutations,
  primaryFocusRequest,
  promptAnswerState,
  onQuestionSelectionChange,
}: Readonly<{
  answerQuestion: QuestionAnswerMutation;
  attentionItems: readonly AttentionItem[];
  currentVersion: number;
  detail: TaskDetail;
  disabled: boolean;
  initialFocus?: TaskDetailInitialFocus | undefined;
  mutations: ReturnType<typeof useTaskMutations>;
  primaryFocusRequest?: PromptPrimaryFocusRequest | undefined;
  promptAnswerState: PromptAnswerState;
  onQuestionSelectionChange: (key: PromptAnswerKey, selection: QuestionSelectionState) => void;
}>) {
  const { logger } = useAppServices();
  const missingFocusLogKeyRef = useRef<TaskDetailInitialFocus | null>(null);
  const [primaryControls] = useState(() => new PromptPrimaryControlRegistry());
  const focusedAttentionID = focusedAttentionItemID(attentionItems, initialFocus);

  useLayoutEffect(() => {
    if (primaryFocusRequest !== undefined) {
      primaryControls.focus(primaryFocusRequest.key);
    }
  }, [primaryControls, primaryFocusRequest]);

  useEffect(() => {
    if (initialFocus === undefined || focusedAttentionID !== undefined) {
      return;
    }
    if (sameTaskDetailInitialFocus(missingFocusLogKeyRef.current, initialFocus)) {
      return;
    }
    missingFocusLogKeyRef.current = initialFocus;
    void logger.append("warn", "Task detail initial focus target did not match current attention rows.", {
      taskID: detail.id,
      ...initialFocusLogContext(initialFocus),
    });
  }, [detail.id, focusedAttentionID, initialFocus, logger]);

  return (
    <>
      {attentionItems.map((item) => (
        <InboxItem
          answerQuestion={answerQuestion}
          attention={item}
          currentVersion={currentVersion}
          disabled={disabled}
          focusOnMount={item.id === focusedAttentionID}
          key={taskDetailAttentionRowKey(item)}
          mutations={mutations}
          onQuestionSelectionChange={onQuestionSelectionChange}
          primaryControls={primaryControls}
          promptAnswerState={promptAnswerState}
        />
      ))}
    </>
  );
}

function initialFocusLogContext(focus: TaskDetailInitialFocus): Readonly<Record<string, string>> {
  if (focus.kind === "question") {
    return { focusAskIDs: focus.askIDs.join(","), focusKind: focus.kind };
  }
  if (focus.kind === "approval") {
    return { focusKind: focus.kind, focusApprovalID: focus.approvalID };
  }
  return { focusKind: focus.kind };
}

function focusedAttentionItemID(
  attentionItems: readonly AttentionItem[],
  initialFocus: TaskDetailInitialFocus | undefined,
): string | undefined {
  if (initialFocus === undefined) {
    return undefined;
  }
  if (initialFocus.kind === "dependencies") {
    return undefined;
  }
  if (initialFocus.kind === "question") {
    const itemIDByAskID = new Map<string, string>();
    for (const item of attentionItems) {
      if (item.kind === "question" && !itemIDByAskID.has(item.question.promptID)) {
        itemIDByAskID.set(item.question.promptID, item.id);
      }
    }
    return initialFocus.askIDs
      .map((askID) => itemIDByAskID.get(askID))
      .find((itemID) => itemID !== undefined);
  }
  if (initialFocus.kind === "approval") {
    return attentionItems.find(
      (item) => item.kind === "approval" && item.approvalID === initialFocus.approvalID,
    )?.id;
  }
  return attentionItems.find((item) => item.kind === "interrupted_current_node")?.id;
}

function InboxItem({
  answerQuestion,
  attention,
  currentVersion,
  disabled,
  focusOnMount,
  mutations,
  onQuestionSelectionChange,
  primaryControls,
  promptAnswerState,
}: Readonly<{
  answerQuestion: QuestionAnswerMutation;
  attention: AttentionItem;
  currentVersion: number;
  disabled: boolean;
  focusOnMount: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
  onQuestionSelectionChange: (key: PromptAnswerKey, selection: QuestionSelectionState) => void;
  primaryControls: PromptPrimaryControlRegistry;
  promptAnswerState: PromptAnswerState;
}>) {
  const focusTargetRef = useRef<HTMLDivElement | null>(null);
  const scrolledRef = useRef(false);

  useEffect(() => {
    if (!focusOnMount || scrolledRef.current) {
      return;
    }
    scrolledRef.current = true;
    let cancelAlignedScroll: (() => void) | undefined;
    const cancelScroll = scheduleScroll(() => {
      cancelAlignedScroll = scheduleScroll(() => {
        focusTargetRef.current?.scrollIntoView({ block: "start", behavior: "auto" });
      });
    });
    return () => {
      cancelScroll();
      cancelAlignedScroll?.();
    };
  }, [focusOnMount]);

  if (attention.kind === "question") {
    const key = promptAnswerKey(attention);
    const questionSelection = promptAnswerState.selection(key) ?? emptyQuestionSelection();
    return (
      <div ref={focusTargetRef}>
        <QuestionBox
          attention={attention}
          answerQuestion={answerQuestion}
          disabled={disabled}
          onSelectionStateChange={(selection) => {
            onQuestionSelectionChange(key, selection);
          }}
          registerPrimaryControl={(control) => primaryControls.register(key, control)}
          selectionState={questionSelection}
        />
      </div>
    );
  }
  if (attention.kind === "approval") {
    return (
      <div ref={focusTargetRef}>
        <ApprovalBox
          attention={attention}
          currentVersion={currentVersion}
          disabled={disabled}
          mutations={mutations}
        />
      </div>
    );
  }
  return (
    <div ref={focusTargetRef}>
      <InterruptedCurrentNodeBox attention={attention} disabled={disabled} />
    </div>
  );
}

function scheduleScroll(callback: () => void): () => void {
  if (typeof window !== "undefined" && window.requestAnimationFrame instanceof Function) {
    const frame = window.requestAnimationFrame(() => {
      callback();
    });
    return () => {
      window.cancelAnimationFrame(frame);
    };
  }
  const timeout = setTimeout(() => {
    callback();
  }, 0);
  return () => {
    clearTimeout(timeout);
  };
}
