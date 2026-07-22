import { Plus } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage, type ProjectLabel } from "@/api";
import { LabelChooser, useProjectLabelCatalog, useTaskLabelAssignment } from "@/shared/labels";
import { Badge, Button, Spinner } from "@/ui";
import { TaskPropertyLine } from "./TaskPropertyLine";

export function TaskDetailLabels({ disabled }: Readonly<{ disabled: boolean }>) {
  const { t } = useTranslation();
  const catalog = useProjectLabelCatalog();
  const assignment = useTaskLabelAssignment();
  const labelNamesByID = useMemo(
    () => new Map(catalog.data?.labels.map((label) => [label.id, label.name]) ?? []),
    [catalog.data?.labels],
  );
  const selectedLabelIDs =
    assignment.snapshot?.visibleLabelIDs ?? assignment.assignment.data?.labelIDs ?? [];
  const visibleLabels = selectedLabelIDs.flatMap((labelID) => {
    const name = labelNamesByID.get(labelID);
    return name === undefined ? [] : [{ id: labelID, name }];
  });
  const pendingLabelIDs = new Set(assignment.snapshot?.pendingLabelIDs ?? []);
  const triggerDisabled = disabled || assignment.snapshot?.closed === true;
  const trigger = (
    <TaskDetailLabelTrigger
      disabled={triggerDisabled}
      loading={assignment.assignment.isPending}
      pendingLabelIDs={pendingLabelIDs}
      visibleLabels={visibleLabels}
    />
  );
  return (
    <TaskPropertyLine
      label={t("labels.filter")}
      value={
        <div className="grid min-w-0 gap-[var(--space-1)]">
          <LabelChooser
            invocation={{
              kind: "assignment",
              selectedLabelIDs,
              onSelectionChange(labelID, selected) {
                assignment.controller?.setDesired(labelID, selected);
              },
            }}
            trigger={trigger}
          />
          <AssignmentFailures />
        </div>
      }
      valueClassName="flex-1"
    />
  );
}

function TaskDetailLabelTrigger({
  disabled,
  loading,
  pendingLabelIDs,
  visibleLabels,
}: Readonly<{
  disabled: boolean;
  loading: boolean;
  pendingLabelIDs: ReadonlySet<string>;
  visibleLabels: readonly ProjectLabel[];
}>) {
  const { t } = useTranslation();
  return (
    <Button
      aria-label={t("labels.editAssignments")}
      className="min-h-7 h-auto w-full min-w-0 justify-start text-left"
      disabled={disabled}
      style={{ padding: "var(--space-0)" }}
      variant="ghost"
    >
      <span className="flex min-w-0 flex-wrap items-center gap-[var(--space-1)]">
        {loading ? <Spinner size="sm" /> : null}
        {visibleLabels.length === 0 && !loading ? (
          <span className="inline-flex items-center gap-[var(--space-1)] text-[var(--color-muted)]">
            {t("labels.add")}
            <Plus aria-hidden="true" size={14} />
          </span>
        ) : null}
        {visibleLabels.map((label) => (
          <span className={pendingLabelIDs.has(label.id) ? "opacity-60" : undefined} key={label.id}>
            <Badge tone="neutral">{label.name}</Badge>
          </span>
        ))}
      </span>
    </Button>
  );
}

function AssignmentFailures() {
  const { t } = useTranslation();
  const assignment = useTaskLabelAssignment();
  const failures = assignment.snapshot?.failures ?? [];
  const reconciliationFailure = assignment.snapshot?.reconciliationFailure ?? null;
  const initialError = assignment.assignment.isError ? assignment.assignment.error : null;
  if (failures.length === 0 && reconciliationFailure === null && initialError === null) {
    return null;
  }
  return (
    <div className="grid gap-[var(--space-1)]" role="alert">
      {failures.map((failure) => (
        <FailureRow
          error={failure.error}
          key={failure.labelID}
          onRetry={() => {
            assignment.controller?.retry(failure.labelID);
          }}
          title={t("labels.assignmentFailed")}
        />
      ))}
      {reconciliationFailure === null ? null : (
        <FailureRow
          error={reconciliationFailure.error}
          onRetry={() => {
            assignment.controller?.retryReconciliation();
          }}
          title={t("labels.assignmentRefreshFailed")}
        />
      )}
      {initialError === null ? null : (
        <FailureRow
          error={initialError}
          onRetry={() => {
            void assignment.assignment.refetch();
          }}
          title={t("labels.assignmentRefreshFailed")}
        />
      )}
    </div>
  );
}

function FailureRow({ error, onRetry, title }: Readonly<{ error: unknown; onRetry(): void; title: string }>) {
  const { t } = useTranslation();
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-[var(--space-1)] text-[var(--color-error)]">
      <span>{title}</span>
      <span className="min-w-0">{errorMessage(error)}</span>
      <Button onClick={onRetry} variant="primary">
        {t("app.retry")}
      </Button>
    </div>
  );
}
