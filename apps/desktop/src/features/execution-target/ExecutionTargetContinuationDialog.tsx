import { useTranslation } from "react-i18next";

import type {
  WorkflowExecutionTargetSelectionMode,
  WorkflowExecutionTargetSelectionRequirement,
} from "../../api";
import {
  Button,
  Dialog,
  ErrorState,
  RadioGroup,
  RadioGroupItem,
  Spinner,
  TextInput,
} from "../../ui";
import {
  executionTargetSelectionFromDraft,
} from "./executionTargetContinuation";
import type { ExecutionTargetContinuationController } from "./useExecutionTargetContinuation";

const concreteModes = ["none", "head", "default_branch", "custom_ref"] as const;

export function ExecutionTargetContinuationDialog({
  continuation,
}: Readonly<{
  continuation: ExecutionTargetContinuationController;
}>) {
  const { t } = useTranslation();
  const pending = continuation.pending;
  const submitting = pending?.phase === "submitting";
  const selection = pending?.selection;
  const selectedTarget = selection === undefined ? null : executionTargetSelectionFromDraft(selection);
  return (
    <Dialog
      closeDisabled={submitting}
      closeLabel={t("app.close")}
      onClose={continuation.close}
      open={pending !== null}
      title={t("executionTargetContinuation.title")}
    >
      {pending === null || selection === undefined ? null : (
        <form
          className="grid gap-[var(--space-4)]"
          onSubmit={(event) => {
            event.preventDefault();
            void continuation.submit();
          }}
        >
          <ExecutionTargetRequirementMessage requirement={pending.requirement} />
          <RadioGroup
            aria-label={t("executionTargetContinuation.choice")}
            disabled={submitting}
            onValueChange={(mode) => {
              if (isConcreteMode(mode)) {
                continuation.selectMode(mode);
              }
            }}
            value={selection.mode}
          >
            {concreteModes.map((mode) => (
              <label
                className="grid cursor-pointer grid-cols-[auto_minmax(0,1fr)] items-start gap-x-[var(--space-2)] rounded-[var(--radius-m)] border border-[var(--color-outline)] p-[var(--space-2)] transition-[border-color,background-color] has-[[data-state=checked]]:border-[var(--color-primary)] has-[[data-state=checked]]:bg-[var(--color-island-2)]"
                key={mode}
              >
                <RadioGroupItem className="mt-[2px]" value={mode} />
                <span className="grid gap-[2px]">
                  <strong className="text-sm">
                    {t(`executionTargetContinuation.mode_${mode}`)}
                  </strong>
                  <span className="text-sm leading-snug text-[var(--color-muted)]">
                    {t(`executionTargetContinuation.mode_${mode}Help`)}
                  </span>
                </span>
              </label>
            ))}
          </RadioGroup>
          {selection.mode === "custom_ref" ? (
            <TextInput
              disabled={submitting}
              label={t("executionTargetContinuation.customRef")}
              onChange={(event) => {
                continuation.setCustomRef(event.currentTarget.value);
              }}
              required
              value={selection.customRef}
            />
          ) : null}
          {pending.phase === "failed" ? (
            <div role="alert">
              <ErrorState
                body={pending.error}
                fullPage={false}
                reveal={false}
                title={t("executionTargetContinuation.failed")}
              />
            </div>
          ) : null}
          {submitting ? (
            <div
              className="flex items-center gap-[var(--space-2)] text-sm text-[var(--color-muted)]"
              role="status"
            >
              <Spinner size="sm" />
              <span>{t("executionTargetContinuation.resolving")}</span>
            </div>
          ) : null}
          <div className="flex justify-end gap-[var(--space-2)]">
            <Button disabled={submitting} onClick={continuation.close}>
              {t("app.cancel")}
            </Button>
            <Button
              disabled={submitting || selectedTarget === null}
              type="submit"
              variant="primary"
            >
              {pending.phase === "failed" ? t("app.retry") : t("executionTargetContinuation.continue")}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
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
