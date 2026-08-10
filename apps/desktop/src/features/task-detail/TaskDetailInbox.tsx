import { useEffect, useRef } from "react";
import { ContractError, type AttentionItem, type TaskDetail } from "@/api";
import { parseTaskSetupRecoveryDetail } from "@/api/worktreeSetup";
import type { TaskDetailInitialFocus } from "@/app-facade";
import { sameTaskDetailInitialFocus } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { ApprovalBox, InterruptedCurrentNodeBox, QuestionBox } from "./TaskDetailAttention";
import { emptyQuestionSelection, type QuestionSelectionState } from "./TaskDetailQuestionState";
import type { useTaskMutations } from "./useTaskDetailData";

export function TaskInbox({
  attentionItems,
  currentVersion,
  detail,
  disabled,
  initialFocus,
  mutations,
  questionSelections,
  onQuestionSelectionChange,
}: Readonly<{
  attentionItems: readonly AttentionItem[];
  currentVersion: number;
  detail: TaskDetail;
  disabled: boolean;
  initialFocus?: TaskDetailInitialFocus | undefined;
  mutations: ReturnType<typeof useTaskMutations>;
  questionSelections: ReadonlyMap<string, QuestionSelectionState>;
  onQuestionSelectionChange: (askID: string, selection: QuestionSelectionState) => void;
}>) {
  const { logger } = useAppServices();
  const missingFocusLogKeyRef = useRef<TaskDetailInitialFocus | null>(null);
  const focusedAttentionID = focusedAttentionItemID(attentionItems, initialFocus);

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
          attention={item}
          currentVersion={currentVersion}
          disabled={disabled}
          focusOnMount={item.id === focusedAttentionID}
          key={item.id}
          mutations={mutations}
          onQuestionSelectionChange={onQuestionSelectionChange}
          questionSelections={questionSelections}
          task={detail}
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
      if (item.kind === "question" && !itemIDByAskID.has(item.questionID)) {
        itemIDByAskID.set(item.questionID, item.id);
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
  return attentionItems.find((item) => {
    if (item.kind !== "interrupted_current_node") return false;
    try { return parseTaskSetupRecoveryDetail(item.detailJSON) !== null; } catch (error) { if (error instanceof ContractError) return false; throw error; }
  })?.id ?? attentionItems.find((item) => item.kind === "interrupted_current_node")?.id;
}

function InboxItem({
  attention,
  currentVersion,
  disabled,
  focusOnMount,
  mutations,
  onQuestionSelectionChange,
  questionSelections,
  task,
}: Readonly<{
  attention: AttentionItem;
  currentVersion: number;
  disabled: boolean;
  focusOnMount: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
  onQuestionSelectionChange: (askID: string, selection: QuestionSelectionState) => void;
  questionSelections: ReadonlyMap<string, QuestionSelectionState>;
  task: TaskDetail;
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
    const questionSelection =
      questionSelections.get(attention.questionID) ?? emptyQuestionSelection(attention.questionID);
    return (
      <div ref={focusTargetRef}>
        <QuestionBox
          attention={attention}
          answerQuestion={mutations.answerQuestion}
          disabled={disabled}
          onSelectionStateChange={(selection) => {
            onQuestionSelectionChange(attention.questionID, selection);
          }}
          selectionState={questionSelection}
          taskId={task.id}
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
      <InterruptedCurrentNodeBox attention={attention} canResume={task.actions.canResume} disabled={disabled} />
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
