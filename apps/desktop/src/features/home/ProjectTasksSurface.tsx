import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Plus } from "lucide-react";
import { useCallback, useState, type HTMLAttributes, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { WorkflowPickerItem } from "@/api";
import { canonicalBoardFilter, errorMessage } from "@/api";
import {
  queryKeys,
  useAppNavigation,
  useAppServices,
  useOwnedSidebarRoots,
  useSidebarShell,
  type SidebarMode,
} from "@/app-facade";
import {
  Button,
  createVirtualizedPixelOffsetRequest,
  directionalBoundary,
  EmptyState,
  InfiniteListBoundary,
  InteractiveChip,
  Spinner,
  VirtualizedInfiniteList,
  type VirtualizedInfiniteListBoundaryState,
  type VirtualizedItemVisibilityTrigger,
} from "@/ui";
import {
  projectTaskGroups,
  useProjectTaskListData,
  useProjectTaskListEvents,
  type ProjectTaskGroup,
  type ProjectTaskGroupDisclosure,
  type ProjectTaskListData,
} from "./projectTaskListData";
import { projectTasksPresentation } from "./projectTaskListPresentation";
import type { ProjectTasksViewMemory } from "./projectTasksViewMemory";
import { projectTaskColumnCount, type ProjectTaskListEntry } from "./ProjectTaskRow";

const stickyColumnKeys = new Set(["columns"]);

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
  const queryClient = useQueryClient();
  const { open } = useOwnedSidebarRoots();
  const { activeDestination } = useSidebarShell();
  const [disclosure, setDisclosure] = useState(viewMemory.read().disclosure);
  const [labelEditorTaskID, setLabelEditorTaskID] = useState<string | null>(null);
  const query = useQuery({
    queryKey: queryKeys.board(projectID, undefined, canonicalBoardFilter({ kind: "none" })),
    queryFn: async () => api.getBoard(projectID, undefined, canonicalBoardFilter({ kind: "none" })),
  });
  const data = useProjectTaskListData({
    expanded: disclosure,
    gateReady: query.isSuccess && query.data.workflows.length > 0,
    projectID,
  });
  useProjectTaskListEvents({ enabled: query.isSuccess, projectID });
  const boardBoundary = directionalBoundary({
    failed: query.isError,
    loading: query.isPending,
    loadingLabel: t("states.loading"),
    message: query.isError ? errorMessage(query.error) : "",
    onRetry: () => void query.refetch(),
    retryLabel: t("app.retry"),
  });
  const workflows = query.data?.workflows ?? [];
  const openLinkWorkflow = () => {
    open({
      kind: "linkWorkflow",
      mode: sidebarMode,
      onCompleted: async () => {
        await Promise.all([
          queryClient.invalidateQueries({
            queryKey: queryKeys.projectBoardsRoot(projectID),
            refetchType: "active",
          }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.projectTaskListsRoot(projectID),
            refetchType: "active",
          }),
        ]);
      },
      projectID,
    });
  };
  const openNewTask = () => {
    open({
      boardQueryWorkflowID: undefined,
      kind: "newTask",
      mode: sidebarMode,
      onCreated: async () => {
        await queryClient.invalidateQueries({
          queryKey: queryKeys.projectTaskListsRoot(projectID),
          refetchType: "active",
        });
      },
      projectID,
    });
  };
  const countsBoundary = directionalBoundary({
    failed: data.counts.isError,
    loading: data.counts.isPending,
    loadingLabel: t("states.loading"),
    message: data.counts.isError ? errorMessage(data.counts.error) : "",
    onRetry: () => void data.counts.refetch(),
    retryLabel: t("app.retry"),
  });
  const toggleGroup = (group: ProjectTaskGroup) => {
    const next = { ...disclosure, [group]: !disclosure[group] };
    viewMemory.setDisclosure(next);
    setDisclosure(next);
  };
  const taskDetailID = activeDestination?.kind === "taskDetail" ? activeDestination.taskID : null;
  const activeTaskDetailMode =
    activeDestination?.kind === "taskDetail" ? (activeDestination.mode ?? "shift") : null;
  const openTaskDetail = useCallback(
    (taskID: string) => {
      setLabelEditorTaskID(null);
      open({ kind: "taskDetail", mode: activeTaskDetailMode ?? sidebarMode, taskID });
    },
    [activeTaskDetailMode, open, sidebarMode],
  );
  const presentation = projectTasksPresentation({
    data,
    disclosure,
    groupCounts: data.counts.data,
    labelEditorTaskID,
    onLabelsActivate: (taskID) => {
      setLabelEditorTaskID((current) => (current === taskID ? null : taskID));
    },
    onTaskActivate: openTaskDetail,
    onToggle: toggleGroup,
    projectID,
    taskDetailID,
    t,
  });
  const scrollRestorationReady = projectTaskScrollRestorationReady(data, disclosure);
  const onScrollElementChange = useCallback(
    (element: HTMLDivElement | null) => {
      if (element === null) return;
      const memory = viewMemory.read();
      element.scrollLeft = memory.horizontalOffsetPx;
      element.onscroll = () => {
        const current = viewMemory.read();
        viewMemory.setScrollOffsets(
          scrollRestorationReady ? element.scrollTop : current.verticalOffsetPx,
          element.scrollLeft,
        );
      };
    },
    [scrollRestorationReady, viewMemory],
  );
  return (
    <ProjectTasksContent
      boardBoundary={boardBoundary}
      countsBoundary={countsBoundary}
      entries={presentation.entries}
      onLinkWorkflow={openLinkWorkflow}
      onNewTask={openNewTask}
      onScrollElementChange={onScrollElementChange}
      projectID={projectID}
      scrollRestorationReady={scrollRestorationReady}
      taskCount={presentation.taskCount}
      viewMemory={viewMemory}
      workflows={workflows}
    />
  );
}

function projectTaskScrollRestorationReady(
  data: ProjectTaskListData,
  disclosure: ProjectTaskGroupDisclosure,
): boolean {
  const counts = data.counts.data?.counts;
  return (
    counts !== undefined &&
    projectTaskGroups.every(
      (group) => !disclosure[group] || counts[group] === 0 || data[group].pages.length > 0,
    )
  );
}

function ProjectTasksContent({
  boardBoundary,
  countsBoundary,
  entries,
  onLinkWorkflow,
  onNewTask,
  onScrollElementChange,
  projectID,
  scrollRestorationReady,
  taskCount,
  viewMemory,
  workflows,
}: Readonly<{
  boardBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  countsBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  entries: readonly ProjectTaskListEntry[];
  onLinkWorkflow: () => void;
  onNewTask: () => void;
  onScrollElementChange: (element: HTMLDivElement | null) => void;
  projectID: string;
  scrollRestorationReady: boolean;
  taskCount: number | null;
  viewMemory: ProjectTasksViewMemory;
  workflows: readonly WorkflowPickerItem[];
}>) {
  const { t } = useTranslation();
  if (boardBoundary !== undefined) {
    return <InfiniteListBoundary direction="initial" state={boardBoundary} />;
  }
  if (countsBoundary?.state === "error" && taskCount === null) {
    return <InfiniteListBoundary direction="initial" state={countsBoundary} />;
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
  const newTaskAvailable = workflows.length === 1 || workflows.some((workflow) => workflow.isProjectDefault);
  const memory = viewMemory.read();
  return (
    <TasksShell workflows={workflows} onLinkWorkflow={onLinkWorkflow} projectID={projectID}>
      {taskCount === 0 ? (
        <ProjectTasksEmpty
          actionLabel={newTaskAvailable ? t("board.newTask") : t("workflowLibrary.linkWorkflow")}
          body={t("home.prototype.noTasksBody")}
          onAction={newTaskAvailable ? onNewTask : onLinkWorkflow}
          title={t("home.prototype.noTasksTitle")}
        />
      ) : (
        <VirtualizedInfiniteList
          ariaLabel={t("home.prototype.projectTasksGrid")}
          className="h-full min-h-0 w-full min-w-0 overflow-auto [scrollbar-width:thin] [&::-webkit-scrollbar:vertical]:hidden"
          estimateSize={() => 38}
          getItemAnchorKey={(entry) => (entry.kind === "task" ? entry.anchorKey : entry.key)}
          getItemKey={(entry) => entry.key}
          getItemOccurrenceKey={(entry) => entry.key}
          getItemWrapperProps={projectTaskEntryWrapperProps}
          hasNextPage={false}
          isFetchingNextPage={false}
          itemRole="row"
          items={entries}
          loadingLabel={t("app.loadingMore")}
          onLoadMore={() => undefined}
          onScrollElementChange={onScrollElementChange}
          pixelOffsetRequest={
            scrollRestorationReady
              ? createVirtualizedPixelOffsetRequest(`restore-${projectID}`, memory.verticalOffsetPx)
              : undefined
          }
          renderItem={renderProjectTaskEntry}
          role="grid"
          rowSpacing="tight"
          stickyItemKeys={stickyColumnKeys}
          testId="project-task-list-grid"
          visibilityTriggers={projectTaskVisibilityTriggers(entries)}
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

function projectTaskEntryWrapperProps(entry: ProjectTaskListEntry): HTMLAttributes<HTMLDivElement> {
  if (entry.kind !== "task") {
    return { className: entry.className };
  }
  return {
    "aria-label": entry.ariaLabel,
    "aria-selected": entry.selected,
    className: entry.className,
    onClick: entry.onActivate,
    onKeyDown: entry.onKeyDown,
    tabIndex: 0,
  };
}

function renderProjectTaskEntry(entry: ProjectTaskListEntry): ReactNode {
  switch (entry.kind) {
    case "column-header":
      return entry.cells.map((cell) => (
        <div aria-label={cell.ariaLabel} className={cell.className} key={cell.key} role="columnheader">
          {cell.content}
        </div>
      ));
    case "group-header":
      return (
        <div aria-colspan={projectTaskColumnCount} role="gridcell">
          <button aria-expanded={entry.expanded} className="w-full" onClick={entry.onToggle} type="button">
            <ChevronRight
              aria-hidden="true"
              className={entry.expanded ? "inline-block rotate-90" : "inline-block"}
              size={16}
            />
            <span aria-hidden="true">
              {entry.label} {entry.count}
            </span>
            <span className="sr-only">{entry.ariaLabel}</span>
          </button>
        </div>
      );
    case "boundary":
      return (
        <div aria-colspan={projectTaskColumnCount} role="gridcell">
          {entry.state === undefined ? (
            <div
              aria-label={entry.isFetching === true ? entry.loadingLabel : undefined}
              aria-live="polite"
              className="grid min-h-12 place-items-center"
              role={entry.isFetching === true ? "status" : undefined}
            >
              {entry.isFetching === true ? <Spinner size="sm" /> : null}
            </div>
          ) : (
            <InfiniteListBoundary direction={entry.direction} state={entry.state} />
          )}
        </div>
      );
    case "task":
      return entry.cells.map((cell) => (
        <div aria-label={cell.ariaLabel} className={cell.className} key={cell.key} role="gridcell">
          {cell.content}
        </div>
      ));
  }
}

function projectTaskVisibilityTriggers(
  entries: readonly ProjectTaskListEntry[],
): readonly VirtualizedItemVisibilityTrigger[] {
  return entries.flatMap((entry) =>
    entry.kind === "boundary" && entry.direction !== "initial"
      ? [
          {
            enabled: entry.hasMore ?? false,
            fetching: entry.isFetching ?? false,
            itemKey: entry.key,
            onVisible: entry.onLoadMore ?? (() => undefined),
            requestGeneration: `${entry.groupKey}:${entry.direction}:${entry.requestGeneration}`,
          },
        ]
      : [],
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
          <InteractiveChip
            className="shrink-0"
            key={workflow.id}
            onClick={() => void navigation.openProject(projectID, workflow.id)}
            title={workflow.description}
          >
            {workflow.name}
          </InteractiveChip>
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
  onAction,
  title,
}: Readonly<{
  actionLabel: string;
  body: string;
  onAction?: () => void;
  title: string;
}>) {
  return (
    <EmptyState
      action={
        <Button onClick={onAction} variant="primary">
          {actionLabel}
        </Button>
      }
      body={body}
      fullPage={false}
      title={title}
    />
  );
}
