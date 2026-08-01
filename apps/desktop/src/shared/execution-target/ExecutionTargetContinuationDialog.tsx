import { useTranslation } from "react-i18next";

import type {
  WorkflowExecutionTargetSelectionMode,
  WorkflowExecutionTargetSelectionRequirement,
} from "@/api";
import {
  Button,
  compactDialogWidth,
  Dialog,
  ErrorState,
  RadioGroup,
  RadioGroupItem,
  Spinner,
  TextInput,
} from "@/ui";
import { executionTargetSelectionFromDraft } from "./executionTargetContinuation";
import type {
  PendingTaskInitiatingAction,
  TaskInitiatingActionController,
} from "./useExecutionTargetContinuation";

const concreteModes = ["none", "head", "default_branch", "custom_ref"] as const;
type ExecutionTargetPending = Extract<PendingTaskInitiatingAction, { kind: "execution_target" }>;

export function TaskInitiatingActionDialogs({
  continuation,
  onViewDependencies,
}: Readonly<{
  continuation: TaskInitiatingActionController;
  onViewDependencies(taskID: string): void;
}>) {
  const pending = continuation.pending;
  if (pending?.kind === "dependency_confirmation") {
    return (
      <DependencyConfirmationDialog
        continuation={continuation}
        onViewDependencies={onViewDependencies}
        pending={pending}
      />
    );
  }
  return (
    <ExecutionTargetDialog
      continuation={continuation}
      pending={pending?.kind === "execution_target" ? pending : null}
    />
  );
}

function DependencyConfirmationDialog({
  continuation,
  onViewDependencies,
  pending,
}: Readonly<{
  continuation: TaskInitiatingActionController;
  onViewDependencies(taskID: string): void;
  pending: Extract<PendingTaskInitiatingAction, { kind: "dependency_confirmation" }>;
}>) {
  const { t } = useTranslation();
  const taskID = pending.action.kind === "start" ? pending.action.taskID : pending.action.input.taskID;
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
              onViewDependencies(taskID);
            }}
            variant="primary-outline"
          >
            {t("taskDependencyConfirmation.view")}
          </Button>
          <Button
            data-testid="dependency-confirmation-proceed"
            onClick={() => {
              void continuation.proceed();
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
  pending,
}: Readonly<{
  continuation: TaskInitiatingActionController;
  pending: ExecutionTargetPending | null;
}>) {
  const { t } = useTranslation();
  const submitting = pending?.phase === "submitting";
  return (
    <Dialog
      closeDisabled={submitting}
      closeLabel={t("app.close")}
      onClose={continuation.close}
      open={pending !== null}
      title={t("executionTargetContinuation.title")}
    >
      {pending === null ? null : <ExecutionTargetForm continuation={continuation} pending={pending} />}
    </Dialog>
  );
}

function ExecutionTargetForm({
  continuation,
  pending,
}: Readonly<{
  continuation: TaskInitiatingActionController;
  pending: ExecutionTargetPending;
}>) {
  const { t } = useTranslation();
  const submitting = pending.phase === "submitting";
  const selectedTarget = executionTargetSelectionFromDraft(pending.selection);
  return (
    <form
      className="grid gap-[var(--space-4)]"
      onSubmit={(event) => {
        event.preventDefault();
        void continuation.submit();
      }}
    >
      <ExecutionTargetRequirementMessage requirement={pending.requirement} />
      <ExecutionTargetChoices continuation={continuation} disabled={submitting} pending={pending} />
      <ExecutionTargetStatus pending={pending} />
      <div className="flex justify-end gap-[var(--space-2)]">
        <Button disabled={submitting} onClick={continuation.close}>
          {t("app.cancel")}
        </Button>
        <Button
          data-testid="execution-target-submit"
          disabled={submitting || selectedTarget === null}
          type="submit"
          variant="primary"
        >
          {pending.phase === "failed" ? t("app.retry") : t("executionTargetContinuation.continue")}
        </Button>
      </div>
    </form>
  );
}

function ExecutionTargetChoices({
  continuation,
  disabled,
  pending,
}: Readonly<{
  continuation: TaskInitiatingActionController;
  disabled: boolean;
  pending: ExecutionTargetPending;
}>) {
  const { t } = useTranslation();
  return (
    <>
      <RadioGroup
        aria-label={t("executionTargetContinuation.choice")}
        disabled={disabled}
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
          disabled={disabled}
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

function ExecutionTargetStatus({ pending }: Readonly<{ pending: ExecutionTargetPending }>) {
  const { t } = useTranslation();
  if (pending.phase === "failed") {
    return (
      <div role="alert">
        <ErrorState
          body={pending.error}
          fullPage={false}
          reveal={false}
          title={t("executionTargetContinuation.failed")}
        />
      </div>
    );
  }
  if (pending.phase !== "submitting") {
    return null;
  }
  return (
    <div className="flex items-center gap-[var(--space-2)] text-sm text-[var(--color-muted)]" role="status">
      <Spinner size="sm" />
      <span>{t("executionTargetContinuation.resolving")}</span>
    </div>
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
