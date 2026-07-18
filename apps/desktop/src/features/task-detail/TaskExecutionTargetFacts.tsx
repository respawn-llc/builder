import { useTranslation } from "react-i18next";

import type { TaskDetail, WorkflowExecutionTarget, WorkflowManagedExecutionTarget } from "@/api";
import { TaskDetailCopyableValue } from "./TaskDetailCopyableValue";
import { TaskPropertyLine } from "./TaskPropertyLine";

export function TaskExecutionTargetFacts({ detail }: Readonly<{ detail: TaskDetail }>) {
  const { t } = useTranslation();
  return (
    <>
      <TaskPropertyLine
        label={t("task.sourceWorkspace")}
        value={
          <span className="grid min-w-0">
            <span>{detail.sourceWorkspace.name}</span>
            <TaskDetailCopyableValue
              className="break-all font-mono"
              clipboardValue={detail.sourceWorkspace.rootPath}
              kind={{ kind: "source_workspace_path" }}
            >
              {detail.sourceWorkspace.rootPath}
            </TaskDetailCopyableValue>
          </span>
        }
      />
      {detail.executionTarget === null ? null : (
        <LockedExecutionTargetFacts target={detail.executionTarget} />
      )}
    </>
  );
}

function LockedExecutionTargetFacts({ target }: Readonly<{ target: WorkflowExecutionTarget }>) {
  const { t } = useTranslation();
  return (
    <>
      <TaskPropertyLine
        label={t("task.executionTarget")}
        value={t(`task.executionTargetModes.${target.mode}`)}
      />
      {target.mode === "none" ? null : <ManagedExecutionTargetFacts target={target} />}
    </>
  );
}

function ManagedExecutionTargetFacts({ target }: Readonly<{ target: WorkflowManagedExecutionTarget }>) {
  const { t } = useTranslation();
  const worktree = target.managedWorktree;
  return (
    <>
      {worktree?.availability === "available" ? (
        <TaskPropertyLine
          label={t("task.managedWorktree")}
          value={
            <TaskDetailCopyableValue
              className="break-all font-mono"
              clipboardValue={worktree.canonicalRoot}
              kind={{ kind: "managed_worktree_path" }}
            >
              {worktree.canonicalRoot}
            </TaskDetailCopyableValue>
          }
        />
      ) : null}
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
    </>
  );
}

function CommitFact({ target }: Readonly<{ target: WorkflowManagedExecutionTarget }>) {
  const { t } = useTranslation();
  const commitOID = target.commitOID;
  const label = target.provenance === "legacy_observed" ? t("task.observedCommit") : t("task.resolvedCommit");
  return (
    <TaskPropertyLine
      label={label}
      value={
        <TaskDetailCopyableValue className="font-mono" clipboardValue={commitOID} kind={{ kind: "commit" }}>
          {commitOID.slice(0, 12)}
        </TaskDetailCopyableValue>
      }
    />
  );
}
