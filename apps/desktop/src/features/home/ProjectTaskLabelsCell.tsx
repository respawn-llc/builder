import type { ButtonHTMLAttributes, Ref } from "react";
import type { useTranslation } from "react-i18next";

import type { TaskListItem } from "@/api";
import {
  LabelChooser,
  ProjectLabelsProvider,
  TaskLabelAssignmentFeedback,
  TaskLabelAssignmentProvider,
  useTaskLabelAssignment,
} from "@/shared/labels";
import { Badge, OneLineOverflowRow, Spinner } from "@/ui";

export function ProjectTaskLabelsCell({
  onOpenChange,
  open,
  projectID,
  task,
  t,
}: Readonly<{
  onOpenChange(open: boolean): void;
  open: boolean;
  projectID: string;
  task: TaskListItem;
  t: ReturnType<typeof useTranslation>["t"];
}>) {
  if (!open) {
    return (
      <TaskLabelTrigger
        disabled={false}
        loading={false}
        onOpenChange={onOpenChange}
        open={false}
        task={task}
        t={t}
      />
    );
  }
  return (
    <ProjectLabelsProvider projectID={projectID} queryEnabled>
      <TaskLabelAssignmentProvider taskID={task.id}>
        <OpenProjectTaskLabelChooser onOpenChange={onOpenChange} task={task} t={t} />
      </TaskLabelAssignmentProvider>
    </ProjectLabelsProvider>
  );
}

function OpenProjectTaskLabelChooser({
  onOpenChange,
  task,
  t,
}: Readonly<{
  onOpenChange(open: boolean): void;
  task: TaskListItem;
  t: ReturnType<typeof useTranslation>["t"];
}>) {
  const assignment = useTaskLabelAssignment();
  const disabled = assignment.isPending || assignment.error !== null;
  const trigger = (
    <TaskLabelTrigger
      disabled={disabled}
      loading={assignment.isPending}
      onOpenChange={onOpenChange}
      open
      task={task}
      t={t}
    />
  );
  return (
    <LabelChooser
      footer={<TaskLabelAssignmentFeedback />}
      invocation={{
        disabled,
        kind: "assignment",
        selectedLabelIDs: assignment.selectedLabelIDs,
        onSelectionChange(labelID, selected) {
          assignment.setSelected(labelID, selected);
        },
      }}
      onOpenChange={onOpenChange}
      open
      preferredSide="top"
      trigger={trigger}
    />
  );
}

function TaskLabelTrigger({
  disabled,
  loading,
  onOpenChange,
  open,
  ref,
  task,
  t,
  ...buttonProps
}: Readonly<{
  disabled: boolean;
  loading: boolean;
  onOpenChange(open: boolean): void;
  open: boolean;
  ref?: Ref<HTMLButtonElement> | undefined;
  task: TaskListItem;
  t: ReturnType<typeof useTranslation>["t"];
}> &
  Omit<ButtonHTMLAttributes<HTMLButtonElement>, "disabled">) {
  return (
    <button
      {...buttonProps}
      aria-expanded={open}
      aria-label={t("home.prototype.editTaskLabels", { shortID: task.shortID })}
      className="flex h-full w-full min-w-0 items-center rounded-[var(--radius-s)] text-left outline-none focus-visible:ring-[3px] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_40%,transparent)] disabled:cursor-not-allowed disabled:opacity-45"
      disabled={disabled}
      onClick={(event) => {
        event.stopPropagation();
        buttonProps.onClick?.(event);
        if (!open) {
          onOpenChange(true);
        }
      }}
      ref={ref}
      type="button"
    >
      {loading ? (
        <Spinner size="sm" />
      ) : (
        <OneLineOverflowRow
          ariaLabel={t("labels.filter")}
          className="w-full"
          items={task.labels.map((label) => ({
            content: (
              <Badge className="py-[3px]" size="compact" tone="neutral">
                {label.name}
              </Badge>
            ),
            id: label.id,
          }))}
          renderOverflow={(hiddenCount) => (
            <Badge className="py-[3px]" size="compact" tone="neutral">
              +{hiddenCount}
            </Badge>
          )}
        />
      )}
    </button>
  );
}
