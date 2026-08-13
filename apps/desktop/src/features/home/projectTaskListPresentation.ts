import type { TFunction } from "i18next";

import { errorMessage } from "@/api";
import {
  directionalBoundary,
  type VirtualizedGroupedGridEntry,
  type VirtualizedInfiniteListBoundaryState,
} from "@/ui";
import {
  projectTaskGroups,
  type ProjectTaskGroup,
  type ProjectTaskGroupData,
  type ProjectTaskListData,
} from "./projectTaskListData";
import { projectTaskColumnEntry, projectTaskEntry } from "./ProjectTaskRow";

type ProjectTaskPresentationInput = Readonly<{
  counts: Readonly<Record<ProjectTaskGroup, number>> | undefined;
  data: ProjectTaskListData;
  disclosure: Readonly<Record<ProjectTaskGroup, boolean>>;
  labelEditorTaskID: string | null;
  onLabelsActivate: (taskID: string) => void;
  onTaskActivate: (taskID: string) => void;
  onToggle: (group: ProjectTaskGroup) => void;
  projectID: string;
  taskDetailID: string | null;
  t: TFunction;
}>;

export function projectTasksPresentation(
  input: ProjectTaskPresentationInput,
): Readonly<{
  entries: readonly VirtualizedGroupedGridEntry[];
  taskCount: number | null;
}> {
  if (input.counts === undefined) {
    return { entries: [projectTaskColumnEntry(input.t)], taskCount: null };
  }
  return {
    entries: groupedEntries({ ...input, counts: input.counts }),
    taskCount: input.counts.active + input.counts.backlog + input.counts.done,
  };
}

function groupedEntries(
  input: ProjectTaskPresentationInput & {
    counts: Readonly<Record<ProjectTaskGroup, number>>;
  },
): readonly VirtualizedGroupedGridEntry[] {
  return [
    projectTaskColumnEntry(input.t),
    ...projectTaskGroups.flatMap((group) => groupEntries(group, input)),
  ];
}

function groupEntries(
  group: ProjectTaskGroup,
  input: ProjectTaskPresentationInput & {
    counts: Readonly<Record<ProjectTaskGroup, number>>;
  },
): readonly VirtualizedGroupedGridEntry[] {
  const count = input.counts[group];
  if (count === 0) return [];
  const data = input.data[group];
  const entries: VirtualizedGroupedGridEntry[] = [
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
      expanded: input.disclosure[group],
      onToggle: () => {
        input.onToggle(group);
      },
      className:
        "border-b border-[var(--color-outline)] bg-[var(--color-island-2)] px-[var(--space-3)]",
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
        onTaskActivate: input.onTaskActivate,
        projectID: input.projectID,
        task,
        taskDetailID: input.taskDetailID,
        t: input.t,
      }),
    ),
  );
  const replacement = groupBoundary(data, "replacement", input.t);
  if (replacement !== undefined) {
    entries.push(
      boundaryEntry({ data, direction: "replacement", group, state: replacement, t: input.t }),
    );
  }
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

type BoundaryDirection = "initial" | "next" | "previous" | "replacement";

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
}>): VirtualizedGroupedGridEntry {
  return {
    kind: "boundary",
    key: `${group}-${direction}`,
    groupKey: group,
    direction,
    state,
    hasMore:
      direction === "previous"
        ? data.hasPreviousPage
        : direction === "next"
          ? data.hasNextPage
          : false,
    isFetching:
      direction === "previous"
        ? data.isFetchingPreviousPage
        : direction === "next"
          ? data.isFetchingNextPage
          : data.isFetching,
    loadingLabel: t("app.loadingMore"),
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
  const replacement = direction === "replacement";
  const failed = initial
    ? data.isError && data.tasks.length === 0
    : replacement
      ? data.isError && data.tasks.length > 0
      : direction === "previous"
        ? data.isFetchPreviousPageError
        : data.isFetchNextPageError;
  const loading = initial
    ? data.isPending
    : replacement
      ? data.isPlaceholderData && data.isFetching
      : direction === "previous"
        ? data.isFetchingPreviousPage
        : data.isFetchingNextPage;
  return directionalBoundary({
    failed,
    loading,
    loadingLabel: t("states.loading"),
    message: failed ? errorMessage(data.error) : "",
    onRetry: () => {
      void (initial || replacement
        ? data.refetch()
        : direction === "previous"
          ? data.fetchPreviousPage()
          : data.fetchNextPage());
    },
    retryLabel: t("app.retry"),
  });
}
