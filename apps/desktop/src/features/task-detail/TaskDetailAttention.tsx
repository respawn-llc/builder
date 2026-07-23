import { useTranslation } from "react-i18next";

import type { ApprovalAttentionItem, ApprovalSnapshot, InterruptedRunAttentionItem } from "@/api";
import { errorMessage } from "@/api";
import { useAppServices } from "@/app-facade";
import {
  approveExecutionTargetAction,
  ExecutionTargetContinuationDialog,
  executeExecutionTargetAction,
  useExecutionTargetContinuation,
} from "@/shared/execution-target";
import { writeClipboardText } from "@/shared/native-clipboard";
import { WorkflowEdgeRouteGraphic } from "@/shared/workflow-edge";
import { Button, Island, showStatusToast } from "@/ui";
import { TaskDetailCopyableValue } from "./TaskDetailCopyableValue";
import type { useTaskMutations } from "./useTaskDetailData";

export { QuestionBox } from "./TaskDetailQuestionForm";

export function ApprovalBox({
  attention,
  currentVersion,
  disabled,
  mutations,
}: Readonly<{
  attention: ApprovalAttentionItem;
  currentVersion: number;
  disabled: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
}>) {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const executionTargetContinuation = useExecutionTargetContinuation({
    execute: async (action, selection) => executeExecutionTargetAction(api, action, selection),
    onApplied: mutations.refresh,
    onAppliedError: (error) => {
      showStatusToast({
        body: errorMessage(error),
        id: "task-approval-refresh-failed",
        title: t("task.refreshFailed"),
        tone: "danger",
      });
    },
  });
  const snapshot = attention.approvalSnapshot;
  const stale = snapshot.version !== currentVersion;
  function approve(): void {
    void executionTargetContinuation
      .run(approveExecutionTargetAction(attention.taskTransitionID))
      .catch((error: unknown) => {
        showStatusToast({
          body: errorMessage(error),
          id: "task-approval-failed",
          title: t("task.approvalFailed"),
          tone: "danger",
        });
      });
  }
  return (
    <>
      <Island
        aria-label={t("task.approval")}
        className="grid gap-[var(--space-2)] p-[var(--space-2)]"
        level={1}
        radius="l"
        unpadded
      >
        <div className="grid gap-[var(--space-2)]">
          <div
            className="flex min-w-0 items-center gap-[var(--space-2)]"
            data-testid="task-approval-route-action-row"
          >
            <WorkflowEdgeRouteGraphic
              className="-ml-[var(--space-2)]"
              contextMode=""
              layout="compact"
              neutralArrow
              sourceLabel={snapshot.sourceNodeName}
              targetLabel={approvalTargetLabel(snapshot, t)}
            />
            <span className="min-w-0 flex-1" />
            <Button
              className="shrink-0"
              disabled={
                disabled ||
                executionTargetContinuation.running ||
                executionTargetContinuation.pending !== null
              }
              onClick={approve}
              variant="primary"
            >
              {t("task.approve")}
            </Button>
          </div>
          {snapshot.commentary.length > 0 ? (
            <p className="m-0 whitespace-pre-wrap text-sm text-[var(--color-muted)]">{snapshot.commentary}</p>
          ) : null}
          <ApprovalOutputValues outputValues={snapshot.outputValues} />
          {stale ? (
            <p className="m-0 text-sm text-[var(--color-warning)]">
              <strong>{t("task.staleApproval")}</strong> {t("task.staleApprovalBody")}
            </p>
          ) : null}
        </div>
      </Island>
      <ExecutionTargetContinuationDialog continuation={executionTargetContinuation} />
    </>
  );
}

export function InterruptedRunBox({
  attention,
  disabled,
  mutations,
}: Readonly<{
  attention: InterruptedRunAttentionItem;
  disabled: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
}>) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const detailJSON = attention.detailJSON;
  return (
    <Island
      aria-label={t("task.interrupted")}
      className="grid gap-[var(--space-2)] p-[var(--space-4)]"
      level={1}
      radius="l"
      unpadded
    >
      <strong>{t("task.interrupted")}</strong>
      {attention.message.length > 0 ? (
        <p className="m-0 text-sm text-[var(--color-muted)]">{attention.message}</p>
      ) : null}
      {detailJSON !== null ? (
        <Button
          disabled={disabled}
          onClick={() => {
            void writeClipboardText(detailJSON, nativeBridge)
              .then(() => {
                showStatusToast({
                  id: "task-interruption-detail-copied",
                  title: t("task.interruptionDetailCopied"),
                  tone: "success",
                });
              })
              .catch((cause: unknown) => {
                showStatusToast({
                  id: "task-interruption-detail-copy-failed",
                  title: t("task.interruptionDetailCopyFailed"),
                  body: errorMessage(cause),
                  tone: "danger",
                });
              });
          }}
          variant="secondary"
        >
          {t("task.copyInterruptionDetail")}
        </Button>
      ) : null}
      <Button
        disabled={disabled || mutations.resume.isPending}
        onClick={() => {
          mutations.resume.mutate();
        }}
        variant="primary"
      >
        {t("board.resume")}
      </Button>
    </Island>
  );
}

function ApprovalOutputValues({
  outputValues,
}: Readonly<{ outputValues: Readonly<Record<string, string>> }>) {
  const { t } = useTranslation();
  const entries = Object.entries(outputValues);
  if (entries.length === 0) {
    return <p className="m-0 text-sm text-[var(--color-muted)]">{t("app.none")}</p>;
  }
  return (
    <div className="grid gap-[var(--space-2)]">
      {entries.map(([name, value], index) => (
        <div className="grid gap-[var(--space-2)]" key={name}>
          <div className="grid gap-[var(--space-1)]">
            <strong className="text-sm">{name}</strong>
            <TaskDetailCopyableValue
              clipboardValue={value}
              kind={{ kind: "transition_output", outputName: name }}
            >
              {value}
            </TaskDetailCopyableValue>
          </div>
          {index < entries.length - 1 ? (
            <div className="px-[var(--space-2)]">
              <div className="h-px w-full bg-[var(--color-outline)]" />
            </div>
          ) : null}
        </div>
      ))}
    </div>
  );
}

function approvalTargetLabel(
  snapshot: ApprovalSnapshot,
  fallback: ReturnType<typeof useTranslation>["t"],
): string {
  const labels = snapshot.targets
    .map((target) => target.displayName.trim())
    .filter((label) => label.length > 0);
  return labels.join(", ") || fallback("task.targetUnavailable");
}
