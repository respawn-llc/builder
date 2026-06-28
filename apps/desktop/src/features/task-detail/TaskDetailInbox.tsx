import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

import type { AttentionItem, TaskDetail } from "../../api";
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
  focusFirstQuestion = false,
  mutations,
  questionSelections,
  onQuestionSelectionChange,
}: Readonly<{
  currentVersion: number;
  detail: TaskDetail;
  disabled: boolean;
  focusFirstQuestion?: boolean | undefined;
  mutations: ReturnType<typeof useTaskMutations>;
  questionSelections: ReadonlyMap<string, QuestionSelectionState>;
  onQuestionSelectionChange: (askID: string, selection: QuestionSelectionState) => void;
}>) {
  const firstQuestionID = focusFirstQuestion
    ? (detail.attention.find((item) => item.kind === "question")?.id ?? "")
    : "";
  return (
    <>
      {detail.attention.map((item) => (
        <InboxItem
          attention={item}
          currentVersion={currentVersion}
          disabled={disabled}
          focusOnMount={item.id === firstQuestionID}
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
  if (typeof window !== "undefined" && typeof window.requestAnimationFrame === "function") {
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
