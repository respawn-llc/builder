import type { TFunction } from "i18next";

import { errorMessage, type ProjectTaskGroupCounts } from "@/api";
import { directionalBoundary, type VirtualizedInfiniteListBoundaryState } from "@/ui";
import {
  projectTaskGroups,
  type ProjectTaskGroup,
  type ProjectTaskGroupData,
  type ProjectTaskListData,
} from "./projectTaskListData";
import {
  projectTaskColumnEntry,
  projectTaskEntry,
  projectTaskGridClassName,
  type ProjectTaskListEntry,
} from "./ProjectTaskRow";

type ProjectTaskPresentationInput = Readonly<{
  data: ProjectTaskListData;
  disclosure: Readonly<Record<ProjectTaskGroup, boolean>>;
  groupCounts: ProjectTaskGroupCounts | undefined;
  labelEditorTaskID: string | null;
  onLabelsActivate: (taskID: string) => void;
  onResumeTask: (taskID: string) => void;
  onTaskActivate: (taskID: string) => void;
  onToggle: (group: ProjectTaskGroup) => void;
  pendingResumeTaskIDs: ReadonlySet<string>;
  projectID: string;
  resumeDisabled: boolean;
  taskDetailID: string | null;
  t: TFunction;
}>;

export function projectTasksPresentation(input: ProjectTaskPresentationInput): Readonly<{
  entries: readonly ProjectTaskListEntry[];
  taskCount: number | null;
}> {
  if (input.groupCounts === undefined) {
    return { entries: [projectTaskColumnEntry(input.t, undefined)], taskCount: null };
  }
  const { counts, definitions } = input.groupCounts;
  return {
    entries: groupedEntries({ ...input, counts, definitions }),
    taskCount: counts.active + counts.backlog + counts.done,
  };
}

function groupedEntries(
  input: ProjectTaskPresentationInput & {
    counts: Readonly<Record<ProjectTaskGroup, number>>;
    definitions: ProjectTaskGroupCounts["definitions"];
  },
): readonly ProjectTaskListEntry[] {
  return [
    projectTaskColumnEntry(input.t, input.definitions),
    ...projectTaskGroups.flatMap((group) => groupEntries(group, input)),
  ];
}

function groupEntries(
  group: ProjectTaskGroup,
  input: ProjectTaskPresentationInput & {
    counts: Readonly<Record<ProjectTaskGroup, number>>;
    definitions: ProjectTaskGroupCounts["definitions"];
  },
): readonly ProjectTaskListEntry[] {
  const count = input.counts[group];
  if (count === 0) return [];
  const data = input.data[group];
  const entries: ProjectTaskListEntry[] = [
    {
      kind: "group-header",
      key: `group-${group}`,
      groupKey: group,
      label: input.t(`home.prototype.statusGroups.${group}`),
      count,
      ariaLabel: input.t("home.prototype.taskGroupCount", {
        count,
        group: input.t(`home.prototype.statusGroups.${group}`),
      }),
      definitions: input.definitions,
      expanded: input.disclosure[group],
      onToggle: () => {
        input.onToggle(group);
      },
      className: projectTaskGridClassName(
        `project-task-group-header h-10 rounded-[var(--radius-s)] transition-colors duration-100 motion-reduce:transition-none ${
          input.disclosure[group]
            ? "bg-[color-mix(in_srgb,var(--color-island-2)_82%,transparent)]"
            : "bg-[color-mix(in_srgb,var(--color-island-1)_58%,transparent)]"
        }`,
      ),
    },
  ];
  if (!input.disclosure[group]) return entries;
  const initial = groupBoundary(data, "initial", input.t);
  if (initial !== undefined) {
    entries.push(boundaryEntry({ data, direction: "initial", group, state: initial, t: input.t }));
    return entries;
  }
  if (data.hasPreviousPage || data.isFetchingPreviousPage || data.isFetchPreviousPageError) {
    entries.push(
      boundaryEntry({
        data,
        direction: "previous",
        group,
        state: groupBoundary(data, "previous", input.t),
        t: input.t,
      }),
    );
  }
  entries.push(
    ...data.tasks.map((task) =>
      projectTaskEntry({
        group,
        labelEditorTaskID: input.labelEditorTaskID,
        onLabelsActivate: input.onLabelsActivate,
        onResumeTask: input.onResumeTask,
        onTaskActivate: input.onTaskActivate,
        pendingResume: input.pendingResumeTaskIDs.has(task.id),
        projectID: input.projectID,
        resumeDisabled: input.resumeDisabled,
        task,
        taskDetailID: input.taskDetailID,
        t: input.t,
      }),
    ),
  );
  if (data.hasNextPage || data.isFetchingNextPage || data.isFetchNextPageError) {
    entries.push(
      boundaryEntry({
        data,
        direction: "next",
        group,
        state: groupBoundary(data, "next", input.t),
        t: input.t,
      }),
    );
  }
  return entries;
}

type BoundaryDirection = "initial" | "next" | "previous";

function boundaryEntry({
  data,
  direction,
  group,
  state,
  t,
}: Readonly<{
  data: ProjectTaskGroupData;
  direction: BoundaryDirection;
  group: ProjectTaskGroup;
  state: VirtualizedInfiniteListBoundaryState | undefined;
  t: TFunction;
}>): ProjectTaskListEntry {
  return {
    kind: "boundary",
    key: `${group}-${direction}`,
    groupKey: group,
    direction,
    state,
    hasMore:
      direction === "previous" ? data.hasPreviousPage : direction === "next" ? data.hasNextPage : false,
    isFetching:
      direction === "previous"
        ? data.isFetchingPreviousPage
        : direction === "next"
          ? data.isFetchingNextPage
          : data.isFetching,
    loadingLabel: t("app.loadingMore"),
    requestGeneration:
      direction === "previous"
        ? `${direction}:${data.previousRequestGeneration}`
        : direction === "next"
          ? `${direction}:${data.nextRequestGeneration}`
          : "initial",
    onLoadMore:
      direction === "previous"
        ? () => {
            void data.fetchPreviousPage();
          }
        : direction === "next"
          ? () => {
              void data.fetchNextPage();
            }
          : undefined,
  };
}

function groupBoundary(
  data: ProjectTaskGroupData,
  direction: BoundaryDirection,
  t: TFunction,
): VirtualizedInfiniteListBoundaryState | undefined {
  const initial = direction === "initial";
  const failed = initial
    ? data.isError && data.tasks.length === 0
    : direction === "previous"
      ? data.isFetchPreviousPageError
      : data.isFetchNextPageError;
  const loading = initial
    ? data.isPending
    : direction === "previous"
      ? data.isFetchingPreviousPage
      : data.isFetchingNextPage;
  return directionalBoundary({
    failed,
    loading,
    loadingLabel: t("states.loading"),
    message: failed ? errorMessage(data.error) : "",
    onRetry: () => {
      void (initial
        ? data.refetch()
        : direction === "previous"
          ? data.fetchPreviousPage()
          : data.fetchNextPage());
    },
    retryLabel: t("app.retry"),
  });
}
