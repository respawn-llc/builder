import { useTranslation } from "react-i18next";

import type { AttentionItem, TaskTransition } from "../../api";
import { errorMessage } from "../../api/errors";
import { useAppServices } from "../../app/useAppServices";
import { Button, Island, showStatusToast } from "../../ui";
import { writeClipboardText } from "../../ui/clipboard";
import { WorkflowEdgeRouteGraphic } from "../workflow-editor/WorkflowEdgeRouteGraphic";
import {
  ExecutionTargetContinuationDialog,
} from "../execution-target/ExecutionTargetContinuationDialog";
import { useExecutionTargetContinuation } from "../execution-target/useExecutionTargetContinuation";
import {
  approveExecutionTargetAction,
  executeExecutionTargetAction,
} from "../execution-target/executionTargetContinuation";
import type { useTaskMutations } from "./useTaskDetailData";

export { QuestionBox } from "./TaskDetailQuestionForm";

export function ApprovalBox({
  attention,
  currentVersion,
  disabled,
  mutations,
  transitions,
}: Readonly<{
  attention: AttentionItem;
  currentVersion: number;
  disabled: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
  transitions: readonly TaskTransition[];
}>) {
  const { t } = useTranslation();
  const { api, nativeBridge } = useAppServices();
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
  const transition = transitions.find((item) => item.id === attention.taskTransitionID);
  const stale = transition !== undefined && transition.version !== currentVersion;
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
        {transition !== undefined ? (
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
              sourceLabel={transition.sourceNodeName}
              targetLabel={transitionTargetLabel(transition, t)}
            />
            <span className="min-w-0 flex-1" />
            <Button
              className="shrink-0"
              disabled={disabled || executionTargetContinuation.pending !== null}
              onClick={approve}
              variant="primary"
            >
              {t("task.approve")}
            </Button>
          </div>
          {transition.commentary.length > 0 ? (
            <p className="m-0 whitespace-pre-wrap text-sm text-[var(--color-muted)]">
              {transition.commentary}
            </p>
          ) : null}
          <ApprovalOutputValues
            nativeBridge={nativeBridge}
            outputValues={transition.outputValues}
            onCopied={(name) => {
              showStatusToast({
                id: `task-approval-output-copied-${name}`,
                title: t("task.outputValueCopied", { name }),
                tone: "success",
              });
            }}
            onCopyFailed={(name, error) => {
              showStatusToast({
                body: errorMessage(error),
                id: `task-approval-output-copy-failed-${name}`,
                title: t("task.outputValueCopyFailed", { name }),
                tone: "danger",
              });
            }}
          />
          {stale ? (
            <p className="m-0 text-sm text-[var(--color-warning)]">
              <strong>{t("task.staleApproval")}</strong> {t("task.staleApprovalBody")}
            </p>
          ) : null}
        </div>
        ) : (
          <>
            <p>{t("task.unavailableSnapshot")}</p>
            <Button
              disabled={disabled || executionTargetContinuation.pending !== null}
              onClick={approve}
              variant="primary"
            >
              {t("task.approve")}
            </Button>
          </>
        )}
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
  attention: AttentionItem;
  disabled: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
}>) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
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
      {attention.detailJSON.trim().length > 0 ? (
        <Button
          disabled={disabled}
          onClick={() => {
            void writeClipboardText(attention.detailJSON, nativeBridge)
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
        onClick={() => void mutations.resume.mutateAsync()}
        variant="primary"
      >
        {t("board.resume")}
      </Button>
    </Island>
  );
}

function ApprovalOutputValues({
  nativeBridge,
  onCopyFailed,
  onCopied,
  outputValues,
}: Readonly<{
  nativeBridge: ReturnType<typeof useAppServices>["nativeBridge"];
  onCopyFailed: (name: string, error: unknown) => void;
  onCopied: (name: string) => void;
  outputValues: Readonly<Record<string, string>>;
}>) {
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
            <button
              className="-mx-[var(--space-1)] min-w-0 whitespace-pre-wrap rounded-[var(--radius-m)] px-[var(--space-1)] py-[var(--space-1)] text-left text-sm text-[var(--color-muted)] transition-colors hover:bg-[var(--color-island-2)] hover:text-[var(--color-on-island)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-primary)]"
              onClick={() => {
                void writeClipboardText(value, nativeBridge)
                  .then(() => {
                    onCopied(name);
                  })
                  .catch((error: unknown) => {
                    onCopyFailed(name, error);
                  });
              }}
              type="button"
            >
              {value}
            </button>
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

function transitionTargetLabel(
  transition: TaskTransition,
  fallback: ReturnType<typeof useTranslation>["t"],
): string {
  const labels = transition.edges
    .map((edge) => edge.targetNodeName.trim())
    .filter((label) => label.length > 0);
  return labels.join(", ") || fallback("task.targetUnavailable");
}
