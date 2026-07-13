import { Copy } from "lucide-react";
import { useTranslation } from "react-i18next";

import type {
  TaskDetail,
  WorkflowExecutionTarget,
  WorkflowManagedExecutionTarget,
} from "../../api";
import { errorMessage } from "../../api/errors";
import { useAppServices } from "../../app/useAppServices";
import { IconTooltipButton, showStatusToast } from "../../ui";
import { writeClipboardText } from "../../ui/clipboard";
import { TaskPropertyLine } from "./TaskPropertyLine";

export function TaskExecutionTargetFacts({ detail }: Readonly<{ detail: TaskDetail }>) {
  const { t } = useTranslation();
  return (
    <>
      <TaskPropertyLine label={t("task.sourceWorkspace")} value={detail.sourceWorkspace.name} />
      <TaskPropertyLine
        label={t("task.sourceRoot")}
        value={<span className="break-all font-mono">{detail.sourceWorkspace.rootPath}</span>}
      />
      {detail.executionTarget === null ? null : (
        <LockedExecutionTargetFacts target={detail.executionTarget} />
      )}
    </>
  );
}

function LockedExecutionTargetFacts({
  target,
}: Readonly<{ target: WorkflowExecutionTarget }>) {
  const { t } = useTranslation();
  return (
    <>
      <TaskPropertyLine
        label={t("task.executionTarget")}
        value={t(`task.executionTargetModes.${target.mode}`)}
      />
      <TaskPropertyLine
        label={t("task.executionRoot")}
        value={
          target.effectiveRoot === null ? (
            t("app.unavailable")
          ) : (
            <span className="break-all font-mono">{target.effectiveRoot}</span>
          )
        }
      />
      {target.mode === "none" ? null : <ManagedExecutionTargetFacts target={target} />}
    </>
  );
}

function ManagedExecutionTargetFacts({
  target,
}: Readonly<{ target: WorkflowManagedExecutionTarget }>) {
  const { t } = useTranslation();
  return (
    <>
      <TaskPropertyLine
        label={t("task.requestedRevision")}
        value={<span className="break-all font-mono">{target.requestedRef}</span>}
      />
      <CommitFact target={target} />
      {target.currentBranch === null ? null : (
        <TaskPropertyLine
          label={t("task.currentBranch")}
          value={<span className="break-all font-mono">{target.currentBranch}</span>}
        />
      )}
      <TaskPropertyLine
        label={t("task.managedWorktree")}
        value={<ManagedWorktreeFact target={target} />}
      />
    </>
  );
}

function CommitFact({ target }: Readonly<{ target: WorkflowManagedExecutionTarget }>) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const commitOID = target.commitOID;
  const label =
    target.provenance === "legacy_observed"
      ? t("task.observedCommit")
      : t("task.resolvedCommit");
  return (
    <TaskPropertyLine
      label={label}
      value={
        <span className="inline-flex items-center gap-[var(--space-1)]">
          <span className="font-mono">{commitOID.slice(0, 12)}</span>
          <IconTooltipButton
            label={t("task.copyResolvedCommit")}
            onClick={() => {
              void writeClipboardText(commitOID, nativeBridge)
                .then(() => {
                  showStatusToast({
                    id: "task-resolved-commit-copied",
                    title: t("task.resolvedCommitCopied"),
                    tone: "success",
                  });
                })
                .catch((error: unknown) => {
                  showStatusToast({
                    body: errorMessage(error),
                    id: "task-resolved-commit-copy-failed",
                    title: t("task.resolvedCommitCopyFailed"),
                    tone: "danger",
                  });
                });
            }}
          >
            <Copy aria-hidden="true" size={14} strokeWidth={1.75} />
          </IconTooltipButton>
        </span>
      }
    />
  );
}

function ManagedWorktreeFact({
  target,
}: Readonly<{ target: WorkflowManagedExecutionTarget }>) {
  const { t } = useTranslation();
  const worktree = target.managedWorktree;
  if (worktree === null) {
    return t("app.unavailable");
  }
  return (
    <span className="break-all">
      <span className="font-mono">{worktree.canonicalRoot}</span>
      {worktree.availability === "available" ? null : (
        <span> · {t("app.unavailable")}</span>
      )}
    </span>
  );
}
