import { GitFork } from "lucide-react";
import type { KeyboardEvent, MouseEvent, ReactNode } from "react";
import type { useTranslation } from "react-i18next";

import type { TaskListItem } from "@/api";
import { TaskDependencyProgressInteractiveChip } from "@/shared/task-dependencies";
import { TaskStatusIcon } from "@/shared/task-status";
import type { ProjectTaskGroup } from "./projectTaskListData";
import { ProjectTaskLabelsCell } from "./ProjectTaskLabelsCell";
import { ProjectTaskStatusLegend } from "./ProjectTaskStatusLegend";

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
      onToggle: () => void;
      className?: string | undefined;
    }>
  | Readonly<{
      kind: "boundary";
      key: string;
      groupKey: ProjectTaskGroup;
      direction: "initial" | "previous" | "next";
      state?: import("@/ui").VirtualizedInfiniteListBoundaryState | undefined;
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
        content: <ProjectTaskStatusLegend />,
        className: "grid place-items-center",
      },
      {
        key: "dependencies",
        ariaLabel: t("home.prototype.dependenciesColumn"),
        content: <GitFork aria-hidden="true" size={15} strokeWidth={1.8} />,
        className: "grid place-items-center",
      },
      { key: "id", content: t("home.prototype.idColumn") },
      { key: "title", content: t("home.prototype.titleColumn") },
      { key: "workflow", content: t("home.prototype.workflowColumn") },
      { key: "labels", content: t("labels.filter") },
    ],
    className: projectTaskGridClassName("border-b border-[var(--color-outline)] bg-[var(--color-island-1)]"),
  };
}

export function projectTaskEntry({
  group,
  labelEditorTaskID,
  onLabelsActivate,
  onTaskActivate,
  projectID,
  task,
  taskDetailID,
  t,
}: Readonly<{
  group: ProjectTaskGroup;
  labelEditorTaskID: string | null;
  onLabelsActivate: (taskID: string) => void;
  onTaskActivate: (taskID: string) => void;
  projectID: string;
  task: TaskListItem;
  taskDetailID: string | null;
  t: ReturnType<typeof useTranslation>["t"];
}>): ProjectTaskListEntry {
  const selected = labelEditorTaskID === task.id || (labelEditorTaskID === null && taskDetailID === task.id);
  return {
    kind: "task",
    key: task.id,
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
      "cursor-pointer border-b border-[var(--color-outline)] px-[var(--space-3)] transition-colors duration-100 motion-reduce:transition-none hover:bg-[var(--color-island-2)] aria-selected:bg-[color-mix(in_srgb,var(--color-primary)_12%,var(--color-island-1))] aria-selected:shadow-[inset_3px_0_0_var(--color-primary)]",
    ),
    cells: [
      {
        key: "status",
        ariaLabel: `${t("task.status")}: ${t(`task.statusKinds.${task.status.kind}`)}`,
        className: "grid place-items-center",
        content: (
          <span data-testid={`project-task-status-${task.id}`}>
            <TaskStatusIcon status={task.status.kind} />
          </span>
        ),
      },
      {
        key: "dependencies",
        className: "grid place-items-center",
        content:
          task.dependencyProgress === null ? (
            ""
          ) : (
            <TaskDependencyProgressInteractiveChip
              onClick={(event: MouseEvent<HTMLButtonElement>) => {
                event.stopPropagation();
                onTaskActivate(task.id);
              }}
              progress={task.dependencyProgress}
            />
          ),
      },
      {
        key: "id",
        ariaLabel: task.shortID,
        className: "min-w-0 overflow-hidden font-mono",
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
        className: "min-w-0 truncate",
        content: (
          <span className="block truncate" title={task.title}>
            {task.title}
          </span>
        ),
      },
      {
        key: "workflow",
        className: "min-w-0 truncate",
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
      {
        key: "labels",
        className: "min-w-0",
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
    ],
  };
}

export function projectTaskGridClassName(extra: string): string {
  return `grid min-w-[880px] grid-cols-[40px_96px_112px_minmax(140px,1fr)_minmax(130px,180px)_minmax(160px,240px)] items-center gap-[var(--space-2)] ${extra}`;
}
