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
    <ProjectLabelsProvider projectID={projectID} queryEnabled subscribeToProject={false}>
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
      trigger={trigger}
    />
  );
}

function TaskLabelTrigger({
  disabled,
  loading,
  onOpenChange,
  open,
  task,
  t,
}: Readonly<{
  disabled: boolean;
  loading: boolean;
  onOpenChange(open: boolean): void;
  open: boolean;
  task: TaskListItem;
  t: ReturnType<typeof useTranslation>["t"];
}>) {
  return (
    <button
      aria-expanded={open}
      aria-label={t("home.prototype.editTaskLabels", { shortID: task.shortID })}
      className="block h-full w-full min-w-0 rounded-[var(--radius-s)] text-left outline-none focus-visible:ring-[3px] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_40%,transparent)] disabled:cursor-not-allowed disabled:opacity-45"
      disabled={disabled}
      onClick={(event) => {
        event.stopPropagation();
        if (!open) {
          onOpenChange(true);
        }
      }}
      type="button"
    >
      {loading ? (
        <Spinner size="sm" />
      ) : (
        <OneLineOverflowRow
          ariaLabel={t("labels.filter")}
          items={task.labels.map((label) => ({
            content: <Badge tone="neutral">{label.name}</Badge>,
            id: label.id,
          }))}
          renderOverflow={(hiddenCount) => <Badge tone="neutral">+{hiddenCount}</Badge>}
        />
      )}
    </button>
  );
}
