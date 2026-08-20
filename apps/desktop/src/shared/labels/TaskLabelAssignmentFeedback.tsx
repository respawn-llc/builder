import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import { Button } from "@/ui";
import { useTaskLabelAssignmentOptional } from "./taskLabelAssignmentContext";
import type { TaskLabelAssignmentData } from "./taskLabelAssignmentData";

export function TaskLabelAssignmentFeedback({
  assignment: suppliedAssignment,
}: Readonly<{ assignment?: TaskLabelAssignmentData | undefined }> = {}) {
  const { t } = useTranslation();
  const contextualAssignment = useTaskLabelAssignmentOptional();
  const assignment = suppliedAssignment ?? contextualAssignment;
  if (assignment === null) {
    throw new Error("TaskLabelAssignmentProvider is required");
  }
  return (
    <>
      {assignment.error === null ? null : (
        <FailureRow error={assignment.error} onRetry={assignment.retryLoad} title={t("labels.loadFailed")} />
      )}
      {assignment.failures.length === 0 ? null : (
        <div className="grid gap-[var(--space-1)]">
          {assignment.failures.map((failure) => (
            <FailureRow
              error={failure.error}
              key={failure.labelID}
              onRetry={() => {
                assignment.retry(failure.labelID);
              }}
              title={t("labels.assignmentFailed")}
            />
          ))}
        </div>
      )}
    </>
  );
}

function FailureRow({ error, onRetry, title }: Readonly<{ error: unknown; onRetry(): void; title: string }>) {
  const { t } = useTranslation();
  return (
    <div
      className="flex min-w-0 flex-wrap items-center gap-[var(--space-1)] text-[var(--color-error)]"
      role="alert"
    >
      <span>{title}</span>
      <span className="min-w-0">{errorMessage(error)}</span>
      <Button onClick={onRetry} variant="primary">
        {t("app.retry")}
      </Button>
    </div>
  );
}
