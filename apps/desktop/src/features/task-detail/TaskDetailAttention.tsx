import { useTranslation } from "react-i18next";

import type { ApprovalAttentionItem, ApprovalSnapshot, InterruptedCurrentNodeAttentionItem } from "@/api";
import { ContractError, errorMessage } from "@/api";
import { parseTaskSetupRecoveryDetail } from "@/api/worktreeSetup";
import { useAppServices } from "@/app-facade";
import { writeClipboardText } from "@/shared/native-clipboard";
import { WorkflowEdgeRouteGraphic } from "@/shared/workflow-edge";
import { Button, Island, showStatusToast } from "@/ui";
import { TaskResumeButton } from "./TaskResumeButton";
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
  const snapshot = attention.approvalSnapshot;
  const stale = snapshot.version !== currentVersion;
  function approve(): void {
    void mutations.approveApproval
      .mutateAsync(attention.approvalID)
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
                mutations.approveApproval.isPending
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
    </>
  );
}

export function InterruptedCurrentNodeBox({
  attention,
  canResume,
  disabled,
}: Readonly<{
  attention: InterruptedCurrentNodeAttentionItem;
  canResume: boolean;
  disabled: boolean;
}>) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const detailJSON = attention.detailJSON;
  let recovery = null;
  let recoveryError: string | null = null;
  try {
    recovery = parseTaskSetupRecoveryDetail(detailJSON);
  } catch (error) {
    if (!(error instanceof ContractError)) throw error;
    recoveryError = errorMessage(error);
  }
  return (
    <Island
      aria-label={t("task.interrupted")}
      className="grid gap-[var(--space-2)] p-[var(--space-4)]"
      level={1}
      radius="l"
      unpadded
    >
      <strong>{t("task.interrupted")}</strong>
      {attention.message !== null ? (
        <p className="m-0 text-sm text-[var(--color-muted)]">{attention.message}</p>
      ) : null}
      {recoveryError === null ? null : (
        <p className="m-0 text-sm text-[var(--color-error)]" role="alert">{recoveryError}</p>
      )}
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
      {recovery !== null || canResume ? (
        <TaskResumeButton disabled={disabled} {...(recovery === null ? {} : { recovery })} />
      ) : null}
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
