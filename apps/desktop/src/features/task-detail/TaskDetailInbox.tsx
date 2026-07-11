import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

import type { AttentionItem, TaskDetail } from "../../api";
import type { TaskDetailInitialFocus } from "../../app/sidebarContext";
import { sameTaskDetailInitialFocus } from "../../app/taskDetailInitialFocus";
import { useAppServices } from "../../app/useAppServices";
import { Island } from "../../ui";
import {
  ApprovalBox,
  InterruptedRunBox,
  QuestionBox,
} from "./TaskDetailAttention";
import { emptyQuestionSelection, type QuestionSelectionState } from "./TaskDetailQuestionState";
import type { useTaskMutations } from "./useTaskDetailData";

export function TaskInbox({
  currentVersion,
  detail,
  disabled,
  initialFocus,
  mutations,
  questionSelections,
  onQuestionSelectionChange,
}: Readonly<{
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
  const focusedAttentionID = focusedAttentionItemID(detail.attention, initialFocus);

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
      {detail.attention.map((item) => (
        <InboxItem
          attention={item}
          currentVersion={currentVersion}
          disabled={disabled}
          focusOnMount={item.id === focusedAttentionID}
          key={item.id}
          mutations={mutations}
          onQuestionSelectionChange={onQuestionSelectionChange}
          questionSelection={questionSelections.get(item.askID) ?? emptyQuestionSelection(item.askID)}
          taskId={detail.id}
          transitions={detail.transitions}
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
    return { focusKind: focus.kind, focusTaskTransitionID: focus.taskTransitionID };
  }
  return { focusKind: focus.kind, focusRunID: focus.runID };
}

function focusedAttentionItemID(
  attentionItems: readonly AttentionItem[],
  initialFocus: TaskDetailInitialFocus | undefined,
): string | undefined {
  if (initialFocus === undefined) {
    return undefined;
  }
  if (initialFocus.kind === "question") {
    const itemIDByAskID = new Map<string, string>();
    for (const item of attentionItems) {
      if (item.kind === "question" && !itemIDByAskID.has(item.askID)) {
        itemIDByAskID.set(item.askID, item.id);
      }
    }
    return initialFocus.askIDs.map((askID) => itemIDByAskID.get(askID)).find((itemID) => itemID !== undefined);
  }
  if (initialFocus.kind === "approval") {
    return attentionItems.find(
      (item) => item.kind === "approval" && item.taskTransitionID === initialFocus.taskTransitionID,
    )?.id;
  }
  return attentionItems.find(
    (item) => item.kind === "interrupted_run" && item.runID === initialFocus.runID,
  )?.id;
}

function InboxItem({
  attention,
  currentVersion,
  disabled,
  focusOnMount,
  mutations,
  onQuestionSelectionChange,
  questionSelection,
  taskId,
  transitions,
}: Readonly<{
  attention: AttentionItem;
  currentVersion: number;
  disabled: boolean;
  focusOnMount: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
  onQuestionSelectionChange: (askID: string, selection: QuestionSelectionState) => void;
  questionSelection: QuestionSelectionState;
  taskId: string;
  transitions: TaskDetail["transitions"];
}>) {
  const { t } = useTranslation();
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
    return (
      <div ref={focusTargetRef}>
        <QuestionBox
          attention={attention}
          disabled={disabled}
          mutations={mutations}
          onSelectionStateChange={(selection) => {
            onQuestionSelectionChange(attention.askID, selection);
          }}
          selectionState={questionSelection}
          taskId={taskId}
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
          transitions={transitions}
        />
      </div>
    );
  }
  if (attention.kind === "interrupted_run") {
    return (
      <div ref={focusTargetRef}>
        <InterruptedRunBox attention={attention} disabled={disabled} mutations={mutations} />
      </div>
    );
  }
  return (
    <div ref={focusTargetRef}>
      <Island aria-label={attention.kind || t("task.inbox")} className="grid gap-[var(--space-2)]" level={1} radius="l">
        <h3 className="m-0">{attention.kind || t("task.inbox")}</h3>
        <p className="m-0">{attention.message}</p>
      </Island>
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
