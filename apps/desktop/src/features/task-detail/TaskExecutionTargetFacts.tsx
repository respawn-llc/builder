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
          <TaskDetailCopyableValue
            className="break-all font-mono"
            clipboardValue={detail.sourceWorkspace.rootPath}
            kind={{ kind: "source_workspace_path" }}
          >
            {detail.sourceWorkspace.rootPath}
          </TaskDetailCopyableValue>
        }
      />
      {detail.executionTarget === null ? null : (
        <LockedExecutionTargetFacts target={detail.executionTarget} />
      )}
      {detail.worktreePath === null ? null : (
        <TaskPropertyLine
          label={t("task.managedWorktree")}
          value={
            <TaskDetailCopyableValue
              className="break-all font-mono"
              clipboardValue={detail.worktreePath}
              kind={{ kind: "managed_worktree_path" }}
            >
              {detail.worktreePath}
            </TaskDetailCopyableValue>
          }
        />
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
  return (
    <>
      <TaskPropertyLine
        label={t("task.requestedRevision")}
        value={<span className="break-all font-mono">{target.requestedRef}</span>}
      />
      <CommitFact target={target} />
    </>
  );
}

function CommitFact({ target }: Readonly<{ target: WorkflowManagedExecutionTarget }>) {
  const { t } = useTranslation();
  const label = target.provenance === "legacy_observed" ? t("task.observedCommit") : t("task.resolvedCommit");
  return (
    <TaskPropertyLine
      label={label}
      value={
        <TaskDetailCopyableValue
          className="font-mono"
          clipboardValue={target.commitOID}
          kind={{ kind: "commit" }}
        >
          {target.commitOID.slice(0, 12)}
        </TaskDetailCopyableValue>
      }
    />
  );
}
