import { CheckCircle2, Circle, CircleDot, Plus, X } from "lucide-react";
import { useId, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type {
  TaskDependencies,
  TaskDependencyDirection,
  TaskDependencyDirectionProjection,
  TaskDependencyItem,
  TaskStatusKind,
} from "@/api";
import { requiredTaskDependencyDirection, type TaskDependencyPair } from "@/shared/task-dependencies";
import { ActionableListRow, Button, Island, ProgressInteractiveChip, Spinner } from "@/ui";

export function TaskDependenciesArea({
  dependencies,
  disabled,
  onAdd,
  onRemove,
  onSelectTask,
  taskID,
}: Readonly<{
  dependencies: TaskDependencies;
  disabled: boolean;
  onAdd(direction: TaskDependencyDirection): void;
  onRemove(pair: TaskDependencyPair): void;
  onSelectTask(taskID: string): void;
  taskID: string;
}>) {
  const { t } = useTranslation();
  const blockedBy = requiredTaskDependencyDirection(dependencies, "blocked-by");
  const blocks = requiredTaskDependencyDirection(dependencies, "blocks");
  return (
    <Island className="grid gap-[var(--space-3)] p-[var(--space-3)]" level={1}>
      <header className="flex min-w-0 items-center justify-between gap-[var(--space-2)]">
        <h2 className="m-0 text-base font-semibold">{t("task.dependencies")}</h2>
        {dependencies.blockerCount === 0 ? null : (
          <ProgressInteractiveChip
            label={t("task.dependenciesProgress", {
              completed: dependencies.blockerCount - dependencies.unsatisfiedBlockerCount,
              total: dependencies.blockerCount,
            })}
            maximum={dependencies.blockerCount}
            tone={dependencies.unsatisfiedBlockerCount === 0 ? "success" : "primary"}
            value={dependencies.blockerCount - dependencies.unsatisfiedBlockerCount}
          />
        )}
      </header>
      <DependencyDirection
        direction={blockedBy}
        disabled={disabled}
        onAdd={onAdd}
        onRemove={onRemove}
        onSelectTask={onSelectTask}
        taskID={taskID}
      />
      <div className="h-px bg-[var(--color-outline)]" />
      <DependencyDirection
        direction={blocks}
        disabled={disabled}
        onAdd={onAdd}
        onRemove={onRemove}
        onSelectTask={onSelectTask}
        taskID={taskID}
      />
    </Island>
  );
}

function DependencyDirection({
  direction,
  disabled,
  onAdd,
  onRemove,
  onSelectTask,
  taskID,
}: Readonly<{
  direction: TaskDependencyDirectionProjection;
  disabled: boolean;
  onAdd(direction: TaskDependencyDirection): void;
  onRemove(pair: TaskDependencyPair): void;
  onSelectTask(taskID: string): void;
  taskID: string;
}>) {
  const { t } = useTranslation();
  const headingID = useId();
  const unavailableID = useId();
  const limitReached = direction.addAvailability.kind === "limit_reached";
  return (
    <section aria-labelledby={headingID} data-direction={direction.direction} role="group">
      <header className="mb-[var(--space-1)] flex items-center justify-between gap-[var(--space-2)]">
        <h3 className="m-0 text-sm font-semibold" id={headingID}>
          {t(
            direction.direction === "blocked-by" ? "task.dependenciesBlockedBy" : "task.dependenciesBlocks",
            { count: direction.totalCount },
          )}
        </h3>
        <Button
          aria-describedby={limitReached ? unavailableID : undefined}
          aria-label={t("task.dependenciesAdd")}
          data-testid={`dependency-add-${direction.direction}`}
          disabled={disabled || limitReached}
          onClick={() => {
            onAdd(direction.direction);
          }}
          size="icon-sm"
          variant="ghost"
        >
          <Plus aria-hidden="true" size={15} />
        </Button>
        {limitReached ? (
          <span className="sr-only" id={unavailableID}>
            {t("task.dependenciesLimitReached")}
          </span>
        ) : null}
      </header>
      <div className="grid gap-[2px]">
        {direction.items.map((item) => (
          <DependencyRow
            direction={direction.direction}
            disabled={disabled}
            item={item}
            key={item.taskID}
            onRemove={onRemove}
            onSelectTask={onSelectTask}
            taskID={taskID}
          />
        ))}
      </div>
    </section>
  );
}

function DependencyRow({
  direction,
  disabled,
  item,
  onRemove,
  onSelectTask,
  taskID,
}: Readonly<{
  direction: TaskDependencyDirection;
  disabled: boolean;
  item: TaskDependencyItem;
  onRemove(pair: TaskDependencyPair): void;
  onSelectTask(taskID: string): void;
  taskID: string;
}>) {
  const { t } = useTranslation();
  const pair =
    direction === "blocked-by"
      ? { blockerTaskID: item.taskID, blockedTaskID: taskID }
      : { blockerTaskID: taskID, blockedTaskID: item.taskID };
  return (
    <ActionableListRow
      actions={
        <button
          aria-label={t("task.dependenciesRemove")}
          className="grid size-7 place-items-center rounded-[var(--radius-s)] border-0 bg-transparent text-[var(--color-error)] outline-none focus-visible:ring-[3px] focus-visible:ring-[color-mix(in_srgb,var(--color-error)_35%,transparent)] disabled:cursor-not-allowed disabled:opacity-45"
          data-testid={`dependency-remove-${item.taskID}`}
          disabled={disabled}
          onClick={() => {
            onRemove(pair);
          }}
          type="button"
        >
          <X aria-hidden="true" size={15} />
        </button>
      }
      data-satisfaction={item.satisfaction ?? undefined}
      selectButtonProps={{
        disabled,
        onClick: () => {
          onSelectTask(item.taskID);
        },
      }}
    >
      <span
        className="flex min-w-0 items-center gap-[var(--space-2)]"
        data-testid={`dependency-row-${item.taskID}`}
      >
        <TaskDependencyStatusIcon status={item.status.kind} />
        <span className="shrink-0 font-mono text-xs text-[var(--color-muted)]">{item.shortID}</span>
        <span className="min-w-0 truncate">{item.title}</span>
      </span>
    </ActionableListRow>
  );
}

function TaskDependencyStatusIcon({ status }: Readonly<{ status: TaskStatusKind }>): ReactNode {
  switch (status) {
    case "done":
      return <CheckCircle2 aria-hidden="true" className="text-[var(--color-success)]" size={15} />;
    case "backlog":
      return <Circle aria-hidden="true" size={15} />;
    case "active":
      return <CircleDot aria-hidden="true" className="text-[var(--color-primary)]" size={15} />;
    case "queued":
    case "running":
      return <Spinner className="size-[15px]" size="sm" strokeWidth={2} />;
    case "waiting_approval":
    case "interrupted":
      return <CircleDot aria-hidden="true" className="text-[var(--color-secondary)]" size={15} />;
    case "waiting_question":
      return <CircleDot aria-hidden="true" className="text-[var(--color-primary)]" size={15} />;
  }
}
