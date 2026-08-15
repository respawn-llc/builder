import { GitBranch } from "lucide-react";
import type { KeyboardEvent, MouseEvent, ReactNode } from "react";
import type { useTranslation } from "react-i18next";

import type { ProjectTaskGroupDefinition, TaskListItem } from "@/api";
import { TaskDependencyProgressInteractiveChip } from "@/shared/task-dependencies";
import { TaskStatusIcon } from "@/shared/task-status";
import { Spinner, type VirtualizedInfiniteListBoundaryState } from "@/ui";
import type { ProjectTaskGroup } from "./projectTaskListData";
import { ProjectTaskLabelsCell } from "./ProjectTaskLabelsCell";

export const projectTaskColumnCount = 6;

export type ProjectTaskGridCell = Readonly<{
  key: string;
  content: ReactNode;
  ariaLabel?: string | undefined;
  className?: string | undefined;
}>;

export type ProjectTaskListEntry =
  | Readonly<{
      kind: "column-header";
      key: string;
      cells: readonly ProjectTaskGridCell[];
      className?: string | undefined;
    }>
  | Readonly<{
      kind: "group-header";
      key: string;
      groupKey: ProjectTaskGroup;
      label: string;
      count: number;
      ariaLabel: string;
      expanded: boolean;
      definitions: readonly ProjectTaskGroupDefinition[];
      onToggle: () => void;
      className?: string | undefined;
    }>
  | Readonly<{
      kind: "boundary";
      key: string;
      groupKey: ProjectTaskGroup;
      direction: "initial" | "previous" | "next";
      state?: VirtualizedInfiniteListBoundaryState | undefined;
      hasMore?: boolean | undefined;
      isFetching?: boolean | undefined;
      loadingLabel: string;
      requestGeneration: string;
      onLoadMore?: (() => void) | undefined;
      className?: string | undefined;
    }>
  | Readonly<{
      kind: "task";
      key: string;
      anchorKey: string;
      groupKey: ProjectTaskGroup;
      ariaLabel: string;
      cells: readonly ProjectTaskGridCell[];
      selected?: boolean | undefined;
      onActivate?: ((event: MouseEvent<HTMLDivElement>) => void) | undefined;
      onKeyDown?: ((event: KeyboardEvent<HTMLDivElement>) => void) | undefined;
      className?: string | undefined;
    }>;

export function projectTaskColumnEntry(t: ReturnType<typeof useTranslation>["t"]): ProjectTaskListEntry {
  return {
    kind: "column-header",
    key: "columns",
    cells: [
      {
        key: "status",
        ariaLabel: t("task.status"),
        content: t("task.status"),
      },
      {
        key: "id",
        content: t("home.prototype.idColumn"),
      },
      { key: "title", content: t("home.prototype.titleColumn") },
      {
        key: "dependencies",
        ariaLabel: t("home.prototype.dependenciesColumn"),
        content: <GitBranch aria-hidden="true" size={14} strokeWidth={1.7} />,
        className: "flex h-full items-center justify-center",
      },
      { key: "labels", content: t("labels.filter") },
      { key: "workflow", content: t("home.prototype.workflowColumn") },
    ],
    className: "sr-only !m-0 !h-0 !p-0",
  };
}

export function projectTaskEntry({
  group,
  labelEditorTaskID,
  onLabelsActivate,
  onResumeTask,
  onTaskActivate,
  pendingResume,
  projectID,
  resumeDisabled,
  task,
  taskDetailID,
  t,
}: Readonly<{
  group: ProjectTaskGroup;
  labelEditorTaskID: string | null;
  onLabelsActivate: (taskID: string) => void;
  onResumeTask: (taskID: string) => void;
  onTaskActivate: (taskID: string) => void;
  pendingResume: boolean;
  projectID: string;
  resumeDisabled: boolean;
  task: TaskListItem;
  taskDetailID: string | null;
  t: ReturnType<typeof useTranslation>["t"];
}>): ProjectTaskListEntry {
  const selected = labelEditorTaskID === task.id || (labelEditorTaskID === null && taskDetailID === task.id);
  return {
    kind: "task",
    key: `${group}-${task.id}`,
    anchorKey: task.id,
    groupKey: group,
    ariaLabel: `${task.shortID} ${task.title}`,
    selected,
    onActivate: () => {
      onTaskActivate(task.id);
    },
    onKeyDown: (event: KeyboardEvent<HTMLDivElement>) => {
      if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) {
        return;
      }
      event.preventDefault();
      onTaskActivate(task.id);
    },
    className: projectTaskGridClassName(
      "project-task-row min-h-10 cursor-pointer rounded-[var(--radius-s)] text-[13px] leading-5 outline-none transition-[background-color,scale] duration-100 ease-out motion-reduce:transition-none hover:bg-[color-mix(in_srgb,var(--color-island-2)_78%,transparent)] focus-visible:ring-[2px] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_45%,transparent)] active:[scale:0.997] active:bg-[var(--color-island-3)] aria-selected:bg-[color-mix(in_srgb,var(--color-primary)_11%,var(--color-island-1))]",
    ),
    cells: [
      {
        key: "status",
        ariaLabel: `${t("task.status")}: ${t(`task.statusKinds.${task.status.kind}`)}`,
        className: "flex h-full items-center justify-center",
        content:
          task.status.kind === "interrupted" ? (
            <button
              aria-label={`${t("board.resume")}: ${task.shortID}`}
              className="inline-grid size-6 place-items-center rounded-full outline-none transition-colors hover:bg-[var(--color-island-3)] focus-visible:ring-[2px] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_45%,transparent)] disabled:cursor-not-allowed disabled:opacity-45"
              data-testid={`project-task-status-${task.id}`}
              disabled={resumeDisabled || pendingResume}
              onClick={(event) => {
                event.stopPropagation();
                onResumeTask(task.id);
              }}
              title={t("board.resume")}
              type="button"
            >
              {pendingResume ? <Spinner size="sm" /> : <TaskStatusIcon status={task.status.kind} />}
            </button>
          ) : (
            <span
              data-testid={`project-task-status-${task.id}`}
              onClick={(event) => {
                event.stopPropagation();
              }}
            >
              <TaskStatusIcon status={task.status.kind} />
            </span>
          ),
      },
      {
        key: "id",
        ariaLabel: task.shortID,
        className: "min-w-0 overflow-hidden font-mono text-xs text-[var(--color-muted)]",
        content: (
          <span
            className="block overflow-hidden text-ellipsis whitespace-nowrap [direction:rtl] text-left"
            data-testid={`project-task-id-${task.id}`}
            title={task.shortID}
          >
            {task.shortID}
          </span>
        ),
      },
      {
        key: "title",
        className: "min-w-0 truncate text-[var(--color-on-island)]",
        content: (
          <span className="block truncate" title={task.title}>
            {task.title}
          </span>
        ),
      },
      {
        key: "dependencies",
        className: "flex h-full items-center justify-center",
        content:
          task.dependencyProgress === null ? null : (
            <TaskDependencyProgressInteractiveChip
              onClick={(event: MouseEvent<HTMLButtonElement>) => {
                event.stopPropagation();
                onTaskActivate(task.id);
              }}
              progress={task.dependencyProgress}
              size="compact"
            />
          ),
      },
      {
        key: "labels",
        className: "flex h-full min-w-0 items-center",
        content: (
          <ProjectTaskLabelsCell
            onOpenChange={(open) => {
              if (open !== (labelEditorTaskID === task.id)) {
                onLabelsActivate(task.id);
              }
            }}
            open={labelEditorTaskID === task.id}
            projectID={projectID}
            task={task}
            t={t}
          />
        ),
      },
      {
        key: "workflow",
        className: "min-w-0 truncate text-xs text-[var(--color-muted)]",
        content: (
          <span
            className="block truncate"
            data-testid={`project-task-workflow-${task.id}`}
            title={task.workflowName ?? undefined}
          >
            {task.workflowName}
          </span>
        ),
      },
    ],
  };
}

export function projectTaskGridClassName(extra: string): string {
  return `project-task-grid-row grid w-full items-center gap-x-[var(--space-3)] px-[var(--space-3)] ${extra}`;
}
