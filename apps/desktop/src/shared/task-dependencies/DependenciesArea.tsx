import { Plus, X } from "lucide-react";
import { useId } from "react";
import { useTranslation } from "react-i18next";

import type {
  TaskDependencies,
  TaskDependencyDirection,
  TaskDependencyDirectionProjection,
  TaskDependencyItem,
} from "@/api";
import type { TaskSearchResult } from "@/app-facade";
import { TaskStatusIcon } from "@/shared/task-status";
import { ActionableListRow, Button, Island } from "@/ui";
import { TaskDependencyPicker } from "./TaskDependencyPicker";
import { TaskDependencyProgressChip } from "./TaskDependencyProgressChip";
import { requiredTaskDependencyDirection } from "./dependencyCache";

export function DependenciesArea({
  dependencies,
  disabled,
  excludedTaskIDs,
  navigationDisabled,
  onAdd,
  onRemove,
  onSelectCandidate,
  onSelectTask,
  previewProgress = false,
  projectID,
}: Readonly<{
  dependencies: TaskDependencies;
  disabled: boolean;
  excludedTaskIDs(direction: TaskDependencyDirection): ReadonlySet<string>;
  navigationDisabled: boolean;
  onAdd(direction: TaskDependencyDirection): void;
  onRemove(direction: TaskDependencyDirection, item: TaskDependencyItem): void;
  onSelectCandidate(direction: TaskDependencyDirection, result: TaskSearchResult): Promise<unknown>;
  onSelectTask(taskID: string): void;
  previewProgress?: boolean | undefined;
  projectID: string;
}>) {
  const { t } = useTranslation();
  const blockedBy = requiredTaskDependencyDirection(dependencies, "blocked-by");
  const blocks = requiredTaskDependencyDirection(dependencies, "blocks");
  return (
    <Island className="grid gap-[var(--space-3)] p-[var(--space-3)]" level={1} radius="l">
      <header className="flex min-w-0 items-center justify-between gap-[var(--space-2)]">
        <h2 className="m-0 text-base font-semibold">{t("task.dependencies")}</h2>
        {dependencies.blockerCount === 0 ? null : (
          <TaskDependencyProgressChip
            preview={previewProgress}
            progress={{
              satisfiedCount: dependencies.blockerCount - dependencies.unsatisfiedBlockerCount,
              totalCount: dependencies.blockerCount,
            }}
          />
        )}
      </header>
      <DependencyDirection
        direction={blockedBy}
        disabled={disabled}
        excludedTaskIDs={excludedTaskIDs("blocked-by")}
        navigationDisabled={navigationDisabled}
        onAdd={onAdd}
        onRemove={onRemove}
        onSelectCandidate={onSelectCandidate}
        onSelectTask={onSelectTask}
        projectID={projectID}
      />
      <div className="h-px bg-[var(--color-outline)]" />
      <DependencyDirection
        direction={blocks}
        disabled={disabled}
        excludedTaskIDs={excludedTaskIDs("blocks")}
        navigationDisabled={navigationDisabled}
        onAdd={onAdd}
        onRemove={onRemove}
        onSelectCandidate={onSelectCandidate}
        onSelectTask={onSelectTask}
        projectID={projectID}
      />
    </Island>
  );
}

function DependencyDirection({
  direction,
  disabled,
  excludedTaskIDs,
  navigationDisabled,
  onAdd,
  onRemove,
  onSelectCandidate,
  onSelectTask,
  projectID,
}: Readonly<{
  direction: TaskDependencyDirectionProjection;
  disabled: boolean;
  excludedTaskIDs: ReadonlySet<string>;
  navigationDisabled: boolean;
  onAdd(direction: TaskDependencyDirection): void;
  onRemove(direction: TaskDependencyDirection, item: TaskDependencyItem): void;
  onSelectCandidate(direction: TaskDependencyDirection, result: TaskSearchResult): Promise<unknown>;
  onSelectTask(taskID: string): void;
  projectID: string;
}>) {
  const { t } = useTranslation();
  const headingID = useId();
  const unavailableID = useId();
  const limitReached = direction.addAvailability.kind === "limit_reached";
  const trigger = (
    <Button
      aria-describedby={limitReached ? unavailableID : undefined}
      aria-label={t("task.dependenciesAdd")}
      data-testid={`dependency-add-${direction.direction}`}
      disabled={navigationDisabled || limitReached}
      size="icon-sm"
      variant="ghost"
    >
      <Plus aria-hidden="true" size={15} />
    </Button>
  );
  return (
    <section aria-labelledby={headingID} data-direction={direction.direction} role="group">
      <header className="mb-[var(--space-1)] flex items-center justify-between gap-[var(--space-2)]">
        <h3 className="m-0 text-sm font-semibold" id={headingID}>
          {t(
            direction.direction === "blocked-by" ? "task.dependenciesBlockedBy" : "task.dependenciesBlocks",
            { count: direction.totalCount },
          )}
        </h3>
        {limitReached ? (
          trigger
        ) : (
          <TaskDependencyPicker
            disabled={navigationDisabled}
            excludedTaskIDs={excludedTaskIDs}
            onCreateTask={() => {
              onAdd(direction.direction);
            }}
            onSelect={async (result) => onSelectCandidate(direction.direction, result)}
            projectID={projectID}
            trigger={trigger}
          />
        )}
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
            navigationDisabled={navigationDisabled}
            onRemove={onRemove}
            onSelectTask={onSelectTask}
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
  navigationDisabled,
  onRemove,
  onSelectTask,
}: Readonly<{
  direction: TaskDependencyDirection;
  disabled: boolean;
  item: TaskDependencyItem;
  navigationDisabled: boolean;
  onRemove(direction: TaskDependencyDirection, item: TaskDependencyItem): void;
  onSelectTask(taskID: string): void;
}>) {
  const { t } = useTranslation();
  return (
    <ActionableListRow
      actions={
        <button
          aria-label={t("task.dependenciesRemove")}
          className="grid size-7 place-items-center rounded-[var(--radius-s)] border-0 bg-transparent text-[var(--color-error)] outline-none focus-visible:ring-[3px] focus-visible:ring-[color-mix(in_srgb,var(--color-error)_35%,transparent)] disabled:cursor-not-allowed disabled:opacity-45"
          data-testid={`dependency-remove-${item.taskID}`}
          disabled={disabled}
          onClick={() => {
            onRemove(direction, item);
          }}
          type="button"
        >
          <X aria-hidden="true" size={15} />
        </button>
      }
      data-satisfaction={item.satisfaction ?? undefined}
      selectButtonProps={{
        disabled: navigationDisabled,
        onClick: () => {
          onSelectTask(item.taskID);
        },
      }}
    >
      <span
        className="flex min-w-0 items-center gap-[var(--space-2)]"
        data-testid={`dependency-row-${item.taskID}`}
      >
        <TaskStatusIcon status={item.status.kind} />
        <span className="shrink-0 font-mono text-xs text-[var(--color-muted)]">{item.shortID}</span>
        <span className="min-w-0 truncate">{item.title}</span>
      </span>
    </ActionableListRow>
  );
}
