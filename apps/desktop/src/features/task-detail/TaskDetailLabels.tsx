import { Plus } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  LabelChooser,
  orderedAssignedLabels,
  TaskLabelAssignmentFeedback,
  useProjectLabelCatalog,
  useTaskLabelAssignment,
} from "@/shared/labels";
import { Badge, Button, Spinner } from "@/ui";
import { TaskPropertyLine } from "./TaskPropertyLine";

export function TaskDetailLabels({ disabled }: Readonly<{ disabled: boolean }>) {
  const { t } = useTranslation();
  const catalog = useProjectLabelCatalog();
  const assignment = useTaskLabelAssignment();
  const selectedLabelIDs = assignment.selectedLabelIDs;
  const visibleLabels =
    catalog.data === undefined ? [] : orderedAssignedLabels(catalog.data, selectedLabelIDs);
  const pendingLabelIDs = new Set(assignment.pendingLabelIDs);
  const triggerDisabled = disabled || assignment.isPending || assignment.error !== null;
  const triggerLoading = catalog.isPending || assignment.isPending;
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
                assignment.setSelected(labelID, selected);
              },
            }}
            trigger={
              <Button
                aria-label={t("labels.editAssignments")}
                className="min-h-7 h-auto w-full min-w-0 justify-start text-left"
                disabled={triggerDisabled}
                style={{ padding: "var(--space-0)" }}
                variant="ghost"
              >
                <span className="flex min-w-0 flex-wrap items-center gap-[var(--space-1)]">
                  {triggerLoading ? <Spinner size="sm" /> : null}
                  {visibleLabels.length === 0 && !triggerLoading ? (
                    <span className="inline-flex items-center gap-[var(--space-1)] text-[var(--color-muted)]">
                      {t("labels.add")}
                      <Plus aria-hidden="true" size={14} />
                    </span>
                  ) : null}
                  {visibleLabels.map((label) => (
                    <span
                      aria-busy={pendingLabelIDs.has(label.id) || undefined}
                      className={pendingLabelIDs.has(label.id) ? "opacity-60" : undefined}
                      key={label.id}
                    >
                      <Badge tone="neutral">{label.name}</Badge>
                    </span>
                  ))}
                </span>
              </Button>
            }
          />
          <TaskLabelAssignmentFeedback assignment={assignment} />
        </div>
      }
      valueClassName="flex-1"
    />
  );
}
