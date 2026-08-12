import { useQuery } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useCallback, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { TaskListItem, WorkflowPickerItem } from "@/api";
import { canonicalBoardFilter, errorMessage } from "@/api";
import {
  queryKeys,
  useAppNavigation,
  useAppServices,
  useOwnedSidebarRoots,
  type SidebarMode,
} from "@/app-facade";
import {
  Button,
  directionalBoundary,
  EmptyState,
  InfiniteListBoundary,
  InteractiveChip,
  VirtualizedGroupedGrid,
  type VirtualizedGroupedGridEntry,
  type VirtualizedInfiniteListBoundaryState,
  createVirtualizedPixelOffsetRequest,
} from "@/ui";
import {
  projectTaskGroups,
  useProjectTaskListData,
  useProjectTaskListEvents,
  type ProjectTaskGroup,
  type ProjectTaskGroupData,
} from "./projectTaskListData";
import type { ProjectTasksViewMemory } from "./projectTasksViewMemory";

const columns = ["Status", "Dependencies", "ID", "Title", "Workflow", "Labels"] as const;

export function ProjectTasksSurface({
  projectID,
  sidebarMode,
  viewMemory,
}: Readonly<{
  projectID: string;
  sidebarMode: SidebarMode;
  viewMemory: ProjectTasksViewMemory;
}>) {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const { open } = useOwnedSidebarRoots();
  const initialMemory = viewMemory.read();
  const [disclosure, setDisclosure] = useState(initialMemory.disclosure);
  const [anchors, setAnchors] = useState(initialMemory.anchors);
  const onScrollElementChange = useCallback(
    (element: HTMLDivElement | null) => {
      if (element === null) return;
      const memory = viewMemory.read();
      element.scrollLeft = memory.horizontalOffsetPx;
      element.onscroll = () => {
        viewMemory.setScrollOffsets(element.scrollTop, element.scrollLeft);
      };
    },
    [viewMemory],
  );
  const query = useQuery({
    queryKey: queryKeys.board(projectID, undefined, canonicalBoardFilter({ kind: "none" })),
    queryFn: async () => api.getBoard(projectID, undefined, canonicalBoardFilter({ kind: "none" })),
  });
  const data = useProjectTaskListData({
    anchors,
    expanded: disclosure,
    gateReady: query.isSuccess && query.data.workflows.length > 0,
    projectID,
  });
  useProjectTaskListEvents({
    enabled: query.isSuccess,
    labelEditorTaskID: null,
    projectID,
  });
  const boardBoundary = directionalBoundary({
    failed: query.isError,
    loading: query.isPending,
    loadingLabel: t("states.loading"),
    message: query.isError ? errorMessage(query.error) : "",
    onRetry: () => {
      void query.refetch();
    },
    retryLabel: t("app.retry"),
  });
  const workflows = query.data?.workflows ?? [];
  const openLinkWorkflow = () => {
    open({ kind: "linkWorkflow", mode: sidebarMode, projectID });
  };
  const countsBoundary = directionalBoundary({
    failed: data.counts.isError,
    loading: data.counts.isPending,
    loadingLabel: t("states.loading"),
    message: data.counts.isError ? errorMessage(data.counts.error) : "",
    onRetry: () => {
      void data.counts.refetch();
    },
    retryLabel: t("app.retry"),
  });
  const counts = data.counts.data?.counts;
  const toggleGroup = (group: ProjectTaskGroup) => {
    const next = { ...disclosure, [group]: !disclosure[group] };
    viewMemory.setDisclosure(next);
    if (!next[group]) {
      const nextAnchors = { ...anchors, [group]: 0 };
      viewMemory.setAnchors(nextAnchors);
      setAnchors(nextAnchors);
    }
    setDisclosure(next);
  };
  const presentation = projectTasksPresentation({
    counts,
    data,
    disclosure,
    onToggle: toggleGroup,
    t,
  });
  return (
    <ProjectTasksContent
      boardBoundary={boardBoundary}
      countsBoundary={countsBoundary}
      entries={presentation.entries}
      finalEntryKey={presentation.finalEntryKey}
      onLinkWorkflow={openLinkWorkflow}
      onScrollElementChange={onScrollElementChange}
      projectID={projectID}
      taskCount={presentation.taskCount}
      viewMemory={viewMemory}
      workflows={workflows}
    />
  );
}

function projectTasksPresentation({
  counts,
  data,
  disclosure,
  onToggle,
  t,
}: Readonly<{
  counts: Readonly<Record<ProjectTaskGroup, number>> | undefined;
  data: ReturnType<typeof useProjectTaskListData>;
  disclosure: Readonly<Record<ProjectTaskGroup, boolean>>;
  onToggle: (group: ProjectTaskGroup) => void;
  t: ReturnType<typeof useTranslation>["t"];
}>): Readonly<{
  entries: readonly VirtualizedGroupedGridEntry[];
  finalEntryKey: string;
  taskCount: number | null;
}> {
  if (counts === undefined) {
    return { entries: columnEntries(), finalEntryKey: "columns", taskCount: null };
  }
  const lastVisibleGroup =
    [...projectTaskGroups].reverse().find((group) => counts[group] > 0) ?? null;
  const finalEntryKey =
    lastVisibleGroup === null
      ? "columns"
      : disclosure[lastVisibleGroup]
        ? data[lastVisibleGroup].tasks.at(-1)?.id ?? `group-${lastVisibleGroup}`
        : `group-${lastVisibleGroup}`;
  return {
    entries: groupedEntries({ counts, data, disclosure, onToggle, t }),
    finalEntryKey,
    taskCount: counts.active + counts.backlog + counts.done,
  };
}

function ProjectTasksContent({
  boardBoundary,
  countsBoundary,
  entries,
  finalEntryKey,
  onLinkWorkflow,
  onScrollElementChange,
  projectID,
  taskCount,
  viewMemory,
  workflows,
}: Readonly<{
  boardBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  countsBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  entries: readonly VirtualizedGroupedGridEntry[];
  finalEntryKey: string;
  onLinkWorkflow: () => void;
  onScrollElementChange: (element: HTMLDivElement | null) => void;
  projectID: string;
  taskCount: number | null;
  viewMemory: ProjectTasksViewMemory;
  workflows: readonly WorkflowPickerItem[];
}>) {
  const { t } = useTranslation();
  if (boardBoundary !== undefined) {
    return <InfiniteListBoundary direction="initial" state={boardBoundary} />;
  }
  if (workflows.length === 0) {
    return (
      <TasksShell workflows={workflows} onLinkWorkflow={onLinkWorkflow} projectID={projectID}>
        <ProjectTasksEmpty
          actionLabel={t("workflowLibrary.linkWorkflow")}
          body={t("home.prototype.noLinkedWorkflowsBody")}
          onAction={onLinkWorkflow}
          title={t("home.prototype.noLinkedWorkflowsTitle")}
        />
      </TasksShell>
    );
  }
  const memory = viewMemory.read();
  return (
    <TasksShell workflows={workflows} onLinkWorkflow={onLinkWorkflow} projectID={projectID}>
      {taskCount === 0 ? (
        <ProjectTasksEmpty
          actionLabel={t("board.newTask")}
          body={t("home.prototype.noTasksBody")}
          disabled
          title={t("home.prototype.noTasksTitle")}
        />
      ) : (
        <VirtualizedGroupedGrid
          ariaLabel={t("home.prototype.projectTasksGrid")}
          className="h-full min-h-0 min-w-[880px] overflow-auto [scrollbar-width:thin] [&::-webkit-scrollbar:vertical]:hidden"
          columnCount={columns.length}
          entries={entries}
          estimateSize={() => 38}
          navigation={{
            downLabel: t("home.prototype.jumpToBottom"),
            finalEntryKey,
            upLabel: t("home.prototype.jumpToTop"),
          }}
          onScrollElementChange={onScrollElementChange}
          pixelOffsetRequest={createVirtualizedPixelOffsetRequest(
            `restore-${memory.scrollRequestSequence.toString()}`,
            memory.verticalOffsetPx,
          )}
          testId="project-task-list-grid"
        />
      )}
      {countsBoundary === undefined ? null : (
        <div className="absolute inset-x-0 top-10">
          <InfiniteListBoundary direction="initial" state={countsBoundary} />
        </div>
      )}
    </TasksShell>
  );
}

function TasksShell({
  children,
  onLinkWorkflow,
  projectID,
  workflows,
}: Readonly<{
  children: ReactNode;
  onLinkWorkflow: () => void;
  projectID: string;
  workflows: readonly WorkflowPickerItem[];
}>) {
  const { t } = useTranslation();
  const navigation = useAppNavigation();
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 gap-[var(--space-2)] overflow-x-auto px-[var(--space-4)] py-[var(--space-3)] hide-scrollbar">
        {workflows.map((workflow) => (
          <WorkflowBoardChip
            key={workflow.id}
            onClick={() => {
              void navigation.openProject(projectID, workflow.id);
            }}
            workflow={workflow}
          />
        ))}
        <InteractiveChip className="shrink-0" onClick={onLinkWorkflow}>
          <Plus aria-hidden="true" size={14} strokeWidth={1.8} />
          {t("workflowLibrary.linkWorkflow")}
        </InteractiveChip>
      </div>
      <div className="relative m-[var(--space-4)] mt-0 min-h-0 flex-1 overflow-hidden rounded-[var(--radius-l)] border border-[var(--color-outline)] bg-[var(--color-island-1)]">
        {children}
      </div>
    </div>
  );
}

function ProjectTasksEmpty({
  actionLabel,
  body,
  disabled = false,
  onAction,
  title,
}: Readonly<{
  actionLabel: string;
  body: string;
  disabled?: boolean;
  onAction?: () => void;
  title: string;
}>) {
  return (
    <EmptyState
      action={
        <Button disabled={disabled} onClick={onAction} variant="primary">
          {actionLabel}
        </Button>
      }
      body={body}
      fullPage={false}
      title={title}
    />
  );
}

function columnEntries(): readonly VirtualizedGroupedGridEntry[] {
  return [
    {
      kind: "column-header",
      key: "columns",
      cells: columns.map((column) => ({ key: column.toLowerCase(), content: column })),
      className: taskGridClassName("border-b border-[var(--color-outline)] bg-[var(--color-island-1)]"),
    },
  ];
}

function groupedEntries({
  counts,
  data,
  disclosure,
  onToggle,
  t,
}: Readonly<{
  counts: Readonly<Record<ProjectTaskGroup, number>>;
  data: ReturnType<typeof useProjectTaskListData>;
  disclosure: Readonly<Record<ProjectTaskGroup, boolean>>;
  onToggle: (group: ProjectTaskGroup) => void;
  t: ReturnType<typeof useTranslation>["t"];
}>): readonly VirtualizedGroupedGridEntry[] {
  return [
    ...columnEntries(),
    ...projectTaskGroups.flatMap((group) => {
      const count = counts[group];
      if (count === 0) return [];
      const groupData = data[group];
      const entries: VirtualizedGroupedGridEntry[] = [
        {
          kind: "group-header",
          key: `group-${group}`,
          groupKey: group,
          label: groupName(group),
          count,
          ariaLabel: `${groupName(group)}, ${count.toString()} ${count === 1 ? "task" : "tasks"}`,
          expanded: disclosure[group],
          onToggle: () => {
            onToggle(group);
          },
          className: "border-b border-[var(--color-outline)] bg-[var(--color-island-2)] px-[var(--space-3)]",
        },
      ];
      if (!disclosure[group]) return entries;
      const initial = groupBoundary(groupData, "initial", t);
      if (initial !== undefined) {
        entries.push(boundaryEntry(group, "initial", initial, groupData));
      } else {
        if (groupData.hasPreviousPage || groupData.isFetchingPreviousPage || groupData.isFetchPreviousPageError) {
          entries.push(
            boundaryEntry(group, "previous", groupBoundary(groupData, "previous", t), groupData),
          );
        }
        entries.push(...groupData.tasks.map((task) => taskEntry(group, task)));
        if (groupData.hasNextPage || groupData.isFetchingNextPage || groupData.isFetchNextPageError) {
          entries.push(boundaryEntry(group, "next", groupBoundary(groupData, "next", t), groupData));
        }
      }
      return entries;
    }),
  ];
}

function taskEntry(group: ProjectTaskGroup, task: TaskListItem): VirtualizedGroupedGridEntry {
  return {
    kind: "task",
    key: task.id,
    groupKey: group,
    ariaLabel: `${task.shortID} ${task.title}`,
    className: taskGridClassName(
      "border-b border-[var(--color-outline)] px-[var(--space-3)] hover:bg-[var(--color-island-2)]",
    ),
    cells: [
      { key: "status", content: task.status.kind },
      {
        key: "dependencies",
        content:
          task.dependencyProgress === null
            ? ""
            : `${task.dependencyProgress.satisfiedCount.toString()}/${task.dependencyProgress.totalCount.toString()}`,
      },
      { key: "id", content: task.shortID },
      { key: "title", content: task.title },
      { key: "workflow", content: task.workflowName ?? "" },
      { key: "labels", content: task.labels.map((label) => label.name).join(", ") },
    ],
  };
}

function boundaryEntry(
  group: ProjectTaskGroup,
  direction: "initial" | "previous" | "next",
  state: VirtualizedInfiniteListBoundaryState | undefined,
  data: ProjectTaskGroupData,
): VirtualizedGroupedGridEntry {
  return {
    kind: "boundary",
    key: `${group}-${direction}`,
    groupKey: group,
    direction,
    state,
    hasMore: direction === "previous" ? data.hasPreviousPage : data.hasNextPage,
    isFetching: direction === "previous" ? data.isFetchingPreviousPage : data.isFetchingNextPage,
    loadingLabel: "Loading",
    onLoadMore:
      direction === "previous"
        ? () => {
            void data.fetchPreviousPage();
          }
        : () => {
            void data.fetchNextPage();
          },
  };
}

function groupBoundary(
  data: ProjectTaskGroupData,
  direction: "initial" | "previous" | "next",
  t: ReturnType<typeof useTranslation>["t"],
): VirtualizedInfiniteListBoundaryState | undefined {
  const initial = direction === "initial";
  const failed =
    initial ? data.isError && data.tasks.length === 0 : direction === "previous" ? data.isFetchPreviousPageError : data.isFetchNextPageError;
  const loading =
    initial ? data.isPending : direction === "previous" ? data.isFetchingPreviousPage : data.isFetchingNextPage;
  return directionalBoundary({
    failed,
    loading,
    loadingLabel: t("states.loading"),
    message: failed ? errorMessage(data.error) : "",
    onRetry: () => {
      void (initial ? data.refetch() : direction === "previous" ? data.fetchPreviousPage() : data.fetchNextPage());
    },
    retryLabel: t("app.retry"),
  });
}

function groupName(group: ProjectTaskGroup): string {
  return group === "active" ? "Active" : group === "backlog" ? "Backlog" : "Done";
}

function taskGridClassName(extra: string): string {
  return `grid min-w-[880px] grid-cols-[40px_96px_112px_minmax(140px,1fr)_minmax(130px,180px)_minmax(160px,240px)] items-center gap-[var(--space-2)] ${extra}`;
}

function WorkflowBoardChip({
  onClick,
  workflow,
}: Readonly<{ onClick: () => void; workflow: WorkflowPickerItem }>) {
  return (
    <InteractiveChip className="shrink-0" onClick={onClick} title={workflow.description}>
      {workflow.name}
    </InteractiveChip>
  );
}
