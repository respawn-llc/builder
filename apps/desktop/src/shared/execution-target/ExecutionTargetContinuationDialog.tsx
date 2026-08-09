import { useTranslation } from "react-i18next";

import type {
  WorkflowExecutionTargetSelection,
  WorkflowExecutionTargetSelectionMode,
  WorkflowExecutionTargetSelectionRequirement,
} from "@/api";
import { useTextFieldSubmitShortcut } from "@/app-facade";
import { Button, compactDialogWidth, Dialog, RadioGroup, RadioGroupItem, TextInput } from "@/ui";
import {
  executionTargetSelectionFromDraft,
  proceedWithTaskInitiatingAction,
  type TaskInitiatingAction,
} from "./executionTargetContinuation";
import type {
  PendingTaskInitiatingAction,
  TaskInitiatingActionController,
} from "./useExecutionTargetContinuation";

const concreteModes = ["none", "head", "default_branch", "custom_ref"] as const;
type ExecutionTargetPending = Extract<PendingTaskInitiatingAction, { kind: "execution_target" }>;

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
}: Readonly<{
  continuation: TaskInitiatingActionController;
  onResult(result: TaskInitiatingActionDialogResult): void;
}>) {
  const pending = continuation.pending;
  if (pending?.kind === "dependency_confirmation") {
    return <DependencyConfirmationDialog continuation={continuation} onResult={onResult} pending={pending} />;
  }
  if (pending?.kind === "setup_recovery") {
    return <SetupRecoveryDialog continuation={continuation} onResult={onResult} pending={pending} />;
  }
  return (
    <ExecutionTargetDialog
      continuation={continuation}
      onResult={onResult}
      pending={pending?.kind === "execution_target" ? pending : null}
    />
  );
}

function SetupRecoveryDialog({
  continuation,
  onResult,
  pending,
}: Readonly<{
  continuation: TaskInitiatingActionController;
  onResult(result: TaskInitiatingActionDialogResult): void;
  pending: Extract<PendingTaskInitiatingAction, { kind: "setup_recovery" }>;
}>) {
  const { t } = useTranslation();
  const selectedTarget =
    pending.selection === null ? null : executionTargetSelectionFromDraft(pending.selection);
  const retrySelection =
    pending.targetIntent.kind === "explicit_override" ? pending.targetIntent.selection : undefined;
  return (
    <Dialog closeLabel={t("app.close")} onClose={continuation.close} open title={t("setupRecovery.title")}>
      <div className="grid gap-[var(--space-4)]">
        <div className="grid gap-[var(--space-2)]">
          <p className="m-0 text-[var(--color-muted)]">
            {t("setupRecovery.setupScriptBody")}
          </p>
          <p className="m-0 whitespace-pre-wrap font-mono text-sm text-[var(--color-error)]">
            {pending.failure.diagnostic}
          </p>
          {pending.failure.scriptPath === null ? null : (
            <p className="m-0 break-all font-mono text-sm text-[var(--color-muted)]">
              {pending.failure.scriptPath}
            </p>
          )}
          {pending.failure.retainedWorktree === null ? null : (
            <p className="m-0 break-all font-mono text-sm text-[var(--color-muted)]">
              {pending.failure.retainedWorktree.root}
            </p>
          )}
          {pending.failure.retainedPreviousWorktree === null ? null : (
            <p className="m-0 break-all font-mono text-sm text-[var(--color-muted)]">
              {pending.failure.retainedPreviousWorktree.root}
            </p>
          )}
        </div>
        {pending.selection === null ? (
          <div className="flex flex-wrap justify-end gap-[var(--space-2)]">
            <Button onClick={continuation.close}>{t("app.cancel")}</Button>
            <Button
              data-testid="setup-recovery-choose"
              onClick={continuation.chooseAnotherTarget}
              variant="primary-outline"
            >
              {t("setupRecovery.chooseTarget")}
            </Button>
            <Button
              data-testid="setup-recovery-retry"
              disabled={continuation.running}
              onClick={() => {
                onResult({
                  kind: "continue",
                  action: pending.action,
                  ...(retrySelection === undefined ? {} : { selection: retrySelection }),
                });
              }}
              variant="primary"
            >
              {t("setupRecovery.retry")}
            </Button>
          </div>
        ) : (
          <form
            className="grid gap-[var(--space-4)]"
            onSubmit={(event) => {
              event.preventDefault();
              if (selectedTarget !== null) {
                onResult({ kind: "continue", action: pending.action, selection: selectedTarget });
              }
            }}
          >
            <ExecutionTargetChoices continuation={continuation} pending={pending} />
            <div className="flex justify-end gap-[var(--space-2)]">
              <Button onClick={continuation.close}>{t("app.cancel")}</Button>
              <Button
                data-testid="setup-recovery-target-submit"
                disabled={selectedTarget === null || continuation.running}
                type="submit"
                variant="primary"
              >
                {t("setupRecovery.useTarget")}
              </Button>
            </div>
          </form>
        )}
      </div>
    </Dialog>
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
  continuation: TaskInitiatingActionController;
  pending: ExecutionTargetPending | Extract<PendingTaskInitiatingAction, { kind: "setup_recovery" }>;
}>) {
  const { t } = useTranslation();
  if (pending.selection === null) {
    throw new Error("Execution Target choices require an active selection draft.");
  }
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
            continuation.setCustomRef(event.currentTarget.value);
          }}
          required
          value={pending.selection.customRef}
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
