import { useState } from "react";
import { useTranslation } from "react-i18next";

import type {
  WorkflowExecutionTargetSelection,
  WorkflowExecutionTargetSelectionMode,
  WorkflowExecutionTargetSelectionRequirement,
  TaskSetupRecovery,
} from "@/api";
import { useTextFieldSubmitShortcut } from "@/app-facade";
import { Button, compactDialogWidth, Dialog, RadioGroup, RadioGroupItem, TextInput } from "@/ui";
import {
  executionTargetSelectionFromDraft,
  proceedWithTaskInitiatingAction,
  type ExecutionTargetSelectionDraft,
  type TaskInitiatingAction,
} from "./executionTargetContinuation";
import type {
  PendingTaskInitiatingAction,
  TaskInitiatingActionController,
} from "./useExecutionTargetContinuation";

const concreteModes = ["none", "head", "default_branch", "custom_ref"] as const;
type ExecutionTargetPending = Extract<PendingTaskInitiatingAction, { kind: "execution_target" }>;

export function TaskSetupRecoveryDialog({
  onClose,
  onSubmit,
  open,
  recovery,
  retrySelection,
  running,
}: Readonly<{
  onClose(): void;
  onSubmit(selection?: WorkflowExecutionTargetSelection): void;
  open: boolean;
  recovery: Pick<TaskSetupRecovery, "diagnostic" | "scriptPath" | "retainedWorktree" | "retainedPreviousWorktree">;
  retrySelection?: WorkflowExecutionTargetSelection;
  running: boolean;
}>) {
  const { t } = useTranslation();
  const [selectionDraft, setSelectionDraft] = useState<ExecutionTargetSelectionDraft | null>(null);
  const close = () => { setSelectionDraft(null); onClose(); };
  const selection = selectionDraft === null ? null : executionTargetSelectionFromDraft(selectionDraft);
  return (
    <Dialog closeLabel={t("app.close")} onClose={close} open={open} title={t("task.interrupted")}>
      <div className="grid gap-[var(--space-3)]">
        <p className="m-0 whitespace-pre-wrap font-mono text-sm text-[var(--color-error)]">
          {recovery.diagnostic}
        </p>
        {[recovery.scriptPath, recovery.retainedWorktree?.root, recovery.retainedPreviousWorktree?.root].map((value) =>
          value == null ? null : <p className="m-0 break-all font-mono text-sm" key={value}>{value}</p>,
        )}
        {selectionDraft === null ? (
          <div className="flex justify-end gap-[var(--space-2)]">
            <Button onClick={close}>{t("app.cancel")}</Button>
            <Button data-testid="setup-recovery-choose" onClick={() => {
              setSelectionDraft({ mode: "default_branch", customRef: null });
            }}>
              {t("executionTargetContinuation.title")}
            </Button>
            <Button data-testid="setup-recovery-retry" disabled={running} onClick={() => {
              onSubmit(retrySelection);
            }} variant="primary">
              {t("app.retry")}
            </Button>
          </div>
        ) : (
          <>
            <ExecutionTargetChoices
              continuation={{
                selectMode(mode) { setSelectionDraft({ ...selectionDraft, mode }); },
                setCustomRef(customRef) { setSelectionDraft({ ...selectionDraft, customRef }); },
              }}
              pending={{ selection: selectionDraft }}
            />
            <div className="flex justify-end gap-[var(--space-2)]">
              <Button onClick={close}>{t("app.cancel")}</Button>
              <Button data-testid="setup-recovery-target-submit" disabled={selection === null || running} onClick={() => {
                if (selection !== null) onSubmit(selection);
              }} variant="primary">
                {t("executionTargetContinuation.continue")}
              </Button>
            </div>
          </>
        )}
      </div>
    </Dialog>
  );
}

export type TaskInitiatingActionDialogResult =
  | Readonly<{
      kind: "continue";
      action: TaskInitiatingAction;
      selection?: WorkflowExecutionTargetSelection;
    }>
  | Readonly<{
      kind: "view_dependencies";
      taskID: string;
    }>;

export function TaskInitiatingActionDialogs({
  continuation,
  onResult,
  setupRecovery,
}: Readonly<{
  continuation: TaskInitiatingActionController;
  onResult(result: TaskInitiatingActionDialogResult): void;
  setupRecovery?: Readonly<{
    onClose(): void;
    onSubmit(selection?: WorkflowExecutionTargetSelection): void;
    recovery: TaskSetupRecovery;
  }> | undefined;
}>) {
  const pending = continuation.pending;
  if (setupRecovery !== undefined && pending === null) {
    return (
      <TaskSetupRecoveryDialog
        {...setupRecovery}
        open
        retrySelection={setupRecovery.recovery.executionTarget}
        running={continuation.running}
      />
    );
  }
  if (pending?.kind === "dependency_confirmation") {
    return <DependencyConfirmationDialog continuation={continuation} onResult={onResult} pending={pending} />;
  }
  if (pending?.kind === "setup_recovery") {
    const { failure } = pending;
    return <TaskSetupRecoveryDialog onClose={continuation.close} onSubmit={(selection) => {
      onResult({ kind: "continue", action: pending.action, ...(selection === undefined ? {} : { selection }) });
    }} open recovery={{
      diagnostic: failure.diagnostic, scriptPath: failure.scriptPath,
      retainedWorktree: { root: failure.worktree.registered.kent.canonicalRoot, worktreeID: failure.worktree.registered.kent.worktreeID },
      retainedPreviousWorktree: failure.retainedPreviousWorktree === null ? null : {
        root: failure.retainedPreviousWorktree.worktree.registered.kent.canonicalRoot,
        worktreeID: failure.retainedPreviousWorktree.worktree.registered.kent.worktreeID,
      },
    }} {...(pending.retrySelection === undefined ? {} : { retrySelection: pending.retrySelection })}
      running={continuation.running} />;
  }
  return (
    <ExecutionTargetDialog
      continuation={continuation}
      onResult={onResult}
      pending={pending?.kind === "execution_target" ? pending : null}
    />
  );
}

function DependencyConfirmationDialog({
  continuation,
  onResult,
  pending,
}: Readonly<{
  continuation: TaskInitiatingActionController;
  onResult(result: TaskInitiatingActionDialogResult): void;
  pending: Extract<PendingTaskInitiatingAction, { kind: "dependency_confirmation" }>;
}>) {
  const { t } = useTranslation();
  const taskID = pending.action.kind === "move" ? pending.action.input.taskID : pending.action.taskID;
  return (
    <Dialog
      closeLabel={t("app.close")}
      onClose={continuation.close}
      open
      title={t("taskDependencyConfirmation.title")}
      width={compactDialogWidth}
    >
      <div className="grid gap-[var(--space-4)]">
        <p className="m-0 text-[var(--color-muted)]">
          {t("taskDependencyConfirmation.body", {
            count: pending.unsatisfiedDependencyCount,
          })}
        </p>
        <div className="flex justify-end gap-[var(--space-2)]">
          <Button
            data-testid="dependency-confirmation-view"
            onClick={() => {
              continuation.close();
              onResult({ kind: "view_dependencies", taskID });
            }}
            variant="primary-outline"
          >
            {t("taskDependencyConfirmation.view")}
          </Button>
          <Button
            data-testid="dependency-confirmation-proceed"
            onClick={() => {
              continuation.close();
              onResult({
                kind: "continue",
                action: proceedWithTaskInitiatingAction(pending.action),
              });
            }}
            variant="primary"
          >
            {t("taskDependencyConfirmation.start")}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}

function ExecutionTargetDialog({
  continuation,
  onResult,
  pending,
}: Readonly<{
  continuation: TaskInitiatingActionController;
  onResult(result: TaskInitiatingActionDialogResult): void;
  pending: ExecutionTargetPending | null;
}>) {
  const { t } = useTranslation();
  return (
    <Dialog
      closeLabel={t("app.close")}
      onClose={continuation.close}
      open={pending !== null}
      title={t("executionTargetContinuation.title")}
    >
      {pending === null ? null : (
        <ExecutionTargetForm continuation={continuation} onResult={onResult} pending={pending} />
      )}
    </Dialog>
  );
}

function ExecutionTargetForm({
  continuation,
  onResult,
  pending,
}: Readonly<{
  continuation: TaskInitiatingActionController;
  onResult(result: TaskInitiatingActionDialogResult): void;
  pending: ExecutionTargetPending;
}>) {
  const { t } = useTranslation();
  const selectedTarget = executionTargetSelectionFromDraft(pending.selection);
  const canSubmit = selectedTarget !== null;
  const formShortcut = useTextFieldSubmitShortcut({
    available: canSubmit,
    kind: "form",
  });
  return (
    <form
      className="grid gap-[var(--space-4)]"
      onKeyDown={formShortcut}
      onSubmit={(event) => {
        event.preventDefault();
        if (!canSubmit) {
          return;
        }
        onResult({
          kind: "continue",
          action: pending.action,
          selection: selectedTarget,
        });
      }}
    >
      <ExecutionTargetRequirementMessage requirement={pending.requirement} />
      <ExecutionTargetChoices continuation={continuation} pending={pending} />
      <div className="flex justify-end gap-[var(--space-2)]">
        <Button onClick={continuation.close}>{t("app.cancel")}</Button>
        <Button data-testid="execution-target-submit" disabled={!canSubmit} type="submit" variant="primary">
          {t("executionTargetContinuation.continue")}
        </Button>
      </div>
    </form>
  );
}

function ExecutionTargetChoices({
  continuation,
  pending,
}: Readonly<{
  continuation: Pick<TaskInitiatingActionController, "selectMode" | "setCustomRef">;
  pending: Pick<ExecutionTargetPending, "selection">;
}>) {
  const { t } = useTranslation();
  return (
    <>
      <RadioGroup
        aria-label={t("executionTargetContinuation.choice")}
        onValueChange={(mode) => {
          if (isConcreteMode(mode)) {
            continuation.selectMode(mode);
          }
        }}
        value={pending.selection.mode}
      >
        {concreteModes.map((mode) => (
          <label
            className="grid cursor-pointer grid-cols-[auto_minmax(0,1fr)] items-start gap-x-[var(--space-2)] rounded-[var(--radius-m)] border border-[var(--color-outline)] p-[var(--space-2)] transition-[border-color,background-color] has-[[data-state=checked]]:border-[var(--color-primary)] has-[[data-state=checked]]:bg-[var(--color-island-2)]"
            key={mode}
          >
            <RadioGroupItem className="mt-[2px]" value={mode} />
            <span className="grid gap-[2px]">
              <strong className="text-sm">{t(`executionTargetContinuation.mode_${mode}`)}</strong>
              <span className="text-sm leading-snug text-[var(--color-muted)]">
                {t(`executionTargetContinuation.mode_${mode}Help`)}
              </span>
            </span>
          </label>
        ))}
      </RadioGroup>
      {pending.selection.mode === "custom_ref" ? (
        <TextInput
          label={t("executionTargetContinuation.customRef")}
          onChange={(event) => {
            continuation.setCustomRef(event.currentTarget.value.trim().length === 0 ? null : event.currentTarget.value);
          }}
          required
          value={pending.selection.customRef ?? ""}
        />
      ) : null}
    </>
  );
}

function ExecutionTargetRequirementMessage({
  requirement,
}: Readonly<{ requirement: WorkflowExecutionTargetSelectionRequirement }>) {
  const { t } = useTranslation();
  if (requirement.reason === "policy_requires_selection") {
    return (
      <p className="m-0 text-[var(--color-muted)]">
        {t("executionTargetContinuation.policyRequiresSelection")}
      </p>
    );
  }
  return (
    <div className="grid gap-[var(--space-1)]">
      <p className="m-0 text-[var(--color-muted)]">
        {t("executionTargetContinuation.configuredTargetUnavailable")}
      </p>
      <p className="m-0 text-sm text-[var(--color-warning)]">
        {t(`executionTargetContinuation.unavailable_${requirement.unavailableCause}`)}
      </p>
    </div>
  );
}

function isConcreteMode(value: string): value is WorkflowExecutionTargetSelectionMode {
  return concreteModes.some((mode) => mode === value);
}
