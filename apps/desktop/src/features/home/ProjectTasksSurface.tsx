import { useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, ChevronRight, Circle, CircleDot } from "lucide-react";
import {
  useCallback,
  useLayoutEffect,
  useMemo,
  useReducer,
  useState,
  type HTMLAttributes,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";

import { errorMessage, type WorkflowExecutionTargetSelection } from "@/api";
import {
  invalidateProjectBoardQueries,
  invalidateProjectTaskSearches,
  queryKeys,
  reportNonCancelledError,
  useAppServices,
  useConnectionSnapshot,
  useOwnedSidebarRoots,
  useSidebarShell,
  useStatusController,
  type SidebarMode,
} from "@/app-facade";
import {
  executeTaskInitiatingAction,
  TaskInitiatingActionDialogs,
  type TaskInitiatingAction,
  type TaskInitiatingActionDialogResult,
  useTaskInitiatingActionController,
  useTaskResumeAction,
} from "@/shared/execution-target";
import {
  Button,
  createVirtualizedPixelOffsetRequest,
  directionalBoundary,
  EmptyState,
  InfiniteListBoundary,
  Spinner,
  VirtualizedInfiniteList,
  type VirtualizedInfiniteListBoundaryState,
  type VirtualizedItemVisibilityTrigger,
} from "@/ui";
import {
  projectTaskGroups,
  projectTaskGroupPageSize,
  projectTaskGroupPrefetchPages,
  projectTaskListWorkflowCardinality,
  useProjectTaskListData,
  useProjectTaskListEvents,
  type ProjectTaskGroup,
} from "./projectTaskListData";
import {
  projectTaskColumnStyle,
  resolveProjectTaskVisibleColumns,
  useProjectTaskColumnLayout,
  useProjectTaskListWidth,
  type ProjectTaskColumnLayout,
} from "./projectTaskColumnLayout";
import { ProjectTaskColumnMeasurements } from "./ProjectTaskColumnMeasurements";
import { projectTasksPresentation } from "./projectTaskListPresentation";
import {
  emptyProjectTaskSortLayoutState,
  previousSortProjectionChanged,
  projectTaskSortLayoutReducer,
  projectTaskSortProjection,
} from "./projectTaskSortLayout";
import {
  projectTaskScrollRestorationReady,
  projectTaskWorkflowInitialState,
  projectTaskWorkflowStrip,
} from "./projectTaskSurfaceState";
import type { ProjectTasksViewMemory } from "./projectTasksViewMemory";
import { projectTaskSortsEqual, type ProjectTaskSort } from "./projectTaskSorting";
import { projectTaskColumnCount, type ProjectTaskListEntry } from "./ProjectTaskRow";
import { ProjectTaskStatusLegend } from "./ProjectTaskStatusLegend";
import {
  projectTaskWorkflowItems,
  useProjectTaskNewTaskAvailable,
  useProjectTaskWorkflowPages,
} from "./projectTaskWorkflows";
import { ProjectWorkflowStrip } from "./ProjectWorkflowStrip";
import { useProjectLinkWorkflowAction } from "./useProjectLinkWorkflowAction";

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
  const connection = useConnectionSnapshot();
  const { push } = useStatusController();
  const queryClient = useQueryClient();
  const { open } = useOwnedSidebarRoots();
  const { activeDestination } = useSidebarShell();
  const [disclosure, setDisclosure] = useState(viewMemory.read().disclosure);
  const [sort, setSort] = useState(viewMemory.read().sort);
  const [sortLayout, dispatchSortLayout] = useReducer(
    projectTaskSortLayoutReducer,
    emptyProjectTaskSortLayoutState,
  );
  const [labelEditorTaskID, setLabelEditorTaskID] = useState<string | null>(null);
  const workflowsQuery = useProjectTaskWorkflowPages(projectID);
  const data = useProjectTaskListData({
    expanded: disclosure,
    projectID,
    sort,
  });
  const onSortChange = useCallback(
    (nextSort: ProjectTaskSort) => {
      if (projectTaskSortsEqual(sort, nextSort)) {
        return;
      }
      for (const group of projectTaskGroups) {
        queryClient.removeQueries({
          exact: true,
          queryKey: queryKeys.projectTaskGroup(projectID, group, nextSort),
        });
      }
      viewMemory.setSort(nextSort);
      setSort(nextSort);
      dispatchSortLayout({ kind: "sort-selected" });
    },
    [projectID, queryClient, sort, viewMemory],
  );
  const refreshTaskSurfaces = useCallback(async (): Promise<void> => {
    await Promise.all([
      invalidateProjectBoardQueries(queryClient, projectID),
      invalidateProjectTaskSearches(queryClient, projectID),
      queryClient.invalidateQueries({
        queryKey: queryKeys.projectTaskListsRoot(projectID),
        refetchType: "active",
      }),
    ]);
  }, [projectID, queryClient]);
  const reportResumeError = useCallback(
    (error: unknown) => {
      reportNonCancelledError(error, (failure) => {
        push({
          id: "project-task-list-resume-error",
          tone: "danger",
          title: t("board.resumeFailed"),
          body: errorMessage(failure),
          durationMs: Infinity,
        });
      });
    },
    [push, t],
  );
  const executeInitiatingAction = useCallback(
    async (action: TaskInitiatingAction, selection?: WorkflowExecutionTargetSelection) => {
      const result = await executeTaskInitiatingAction(api, action, selection);
      return result;
    },
    [api],
  );
  const initiatingAction = useTaskInitiatingActionController({
    execute: executeInitiatingAction,
    onApplied: refreshTaskSurfaces,
    onAppliedError: reportResumeError,
  });
  const resumeAction = useTaskResumeAction(initiatingAction);
  const { layout: columnLayout, retainRenderedWidths } = useProjectTaskColumnLayout(data);
  useProjectTaskListEvents({ enabled: true, projectID });
  const workflowsInitialState = projectTaskWorkflowInitialState(
    workflowsQuery.data !== undefined,
    workflowsQuery.isError,
    workflowsQuery.isPending,
  );
  const workflowsBoundary = directionalBoundary({
    failed: workflowsInitialState.failed,
    loading: workflowsInitialState.loading,
    loadingLabel: t("states.loading"),
    message: workflowsQuery.isError ? errorMessage(workflowsQuery.error) : "",
    onRetry: () => void workflowsQuery.refetch(),
    retryLabel: t("app.retry"),
  });
  const previousWorkflowsBoundary = directionalBoundary({
    failed: workflowsQuery.isFetchPreviousPageError,
    loading: workflowsQuery.isFetchingPreviousPage,
    loadingLabel: t("states.loading"),
    message: workflowsQuery.isError ? errorMessage(workflowsQuery.error) : "",
    onRetry: () => void workflowsQuery.fetchPreviousPage(),
    retryLabel: t("app.retry"),
  });
  const nextWorkflowsBoundary = directionalBoundary({
    failed: workflowsQuery.isFetchNextPageError,
    loading: workflowsQuery.isFetchingNextPage,
    loadingLabel: t("app.loadingMore"),
    message: workflowsQuery.isError ? errorMessage(workflowsQuery.error) : "",
    onRetry: () => void workflowsQuery.fetchNextPage(),
    retryLabel: t("app.retry"),
  });
  const workflows = projectTaskWorkflowItems(workflowsQuery.data);
  const workflowCardinality = projectTaskListWorkflowCardinality(data);
  const newTaskAvailable = useProjectTaskNewTaskAvailable(projectID, workflowsQuery.data);
  const openLinkWorkflow = useProjectLinkWorkflowAction(projectID, sidebarMode);
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
  const openTaskDetail = useCallback(
    (taskID: string) => {
      setLabelEditorTaskID(null);
      open({ kind: "taskDetail", mode: sidebarMode, taskID });
    },
    [open, sidebarMode],
  );
  const openTaskDependencies = useCallback(
    (taskID: string) => {
      setLabelEditorTaskID(null);
      open({
        kind: "taskDetail",
        initialFocus: { kind: "dependencies" },
        mode: sidebarMode,
        taskID,
      });
    },
    [open, sidebarMode],
  );
  const presentation = projectTasksPresentation({
    data,
    disclosure,
    groupCounts: data.counts.data,
    labelEditorTaskID,
    onLabelsActivate: (taskID) => {
      setLabelEditorTaskID((current) => (current === taskID ? null : taskID));
    },
    onResumeTask: (taskID) => {
      void resumeAction.execute(taskID).catch(reportResumeError);
    },
    onTaskActivate: openTaskDetail,
    onToggle: toggleGroup,
    pendingResumeTaskIDs: resumeAction.pendingTaskIDs,
    projectID,
    resumeDisabled:
      connection.phase !== "connected" || initiatingAction.pending !== null || initiatingAction.running,
    taskDetailID,
    t,
  });
  const sortProjection = useMemo(
    () =>
      projectTaskSortProjection(
        { error: data.active.isError, isSortReplacement: data.active.isSortReplacement },
        { error: data.backlog.isError, isSortReplacement: data.backlog.isSortReplacement },
        { error: data.done.isError, isSortReplacement: data.done.isSortReplacement },
      ),
    [
      data.active.isError,
      data.active.isSortReplacement,
      data.backlog.isError,
      data.backlog.isSortReplacement,
      data.done.isError,
      data.done.isSortReplacement,
    ],
  );
  const sortProjectionChanged =
    sortLayout.transition && previousSortProjectionChanged(sortLayout.previousProjection, sortProjection);
  const layoutChangeScrollBehavior =
    sortLayout.pulse || sortProjectionChanged ? "natural" : "preserve-leading-item";
  useLayoutEffect(() => {
    dispatchSortLayout({ kind: "projection-committed", projection: sortProjection });
  }, [sortProjection]);
  const scrollRestorationReady = projectTaskScrollRestorationReady(data, disclosure);
  const onScrollElementChange = useCallback(
    (element: HTMLDivElement | null) => {
      if (element === null) return;
      element.scrollLeft = 0;
      element.onscroll = () => {
        const current = viewMemory.read();
        viewMemory.setScrollOffsets(scrollRestorationReady ? element.scrollTop : current.verticalOffsetPx, 0);
      };
    },
    [scrollRestorationReady, viewMemory],
  );
  function handleTaskInitiatingDialogResult(result: TaskInitiatingActionDialogResult): void {
    if (result.kind === "view_dependencies") {
      openTaskDependencies(result.taskID);
      return;
    }
    if (result.action.kind !== "resume") {
      throw new Error(`Project Task list cannot continue a ${result.action.kind} action.`);
    }
    const resumed =
      result.selection === undefined
        ? resumeAction.execute(result.action.taskID)
        : resumeAction.continueExecution(result.action, result.selection);
    void resumed.catch(reportResumeError);
  }

  return (
    <>
      <ProjectTaskColumnMeasurements data={data} onMeasure={retainRenderedWidths} />
      <ProjectTasksContent
        countsBoundary={countsBoundary}
        columnLayout={columnLayout}
        entries={presentation.entries}
        layoutChangeScrollBehavior={layoutChangeScrollBehavior}
        onLinkWorkflow={openLinkWorkflow}
        onNewTask={openNewTask}
        onScrollElementChange={onScrollElementChange}
        projectID={projectID}
        scrollRestorationReady={scrollRestorationReady}
        taskCount={presentation.taskCount}
        viewMemory={viewMemory}
        newTaskAvailable={newTaskAvailable}
        workflowColumnRelevant={workflowCardinality !== "one"}
        workflowCount={workflows.length}
        workflowsBoundary={workflowsBoundary}
        workflowStrip={projectTaskWorkflowStrip(
          workflowsBoundary,
          workflows.length,
          <ProjectWorkflowStrip
            hasNextPage={workflowsQuery.hasNextPage}
            hasPreviousPage={workflowsQuery.hasPreviousPage}
            initialBoundary={workflowsBoundary}
            isFetchingNextPage={workflowsQuery.isFetchingNextPage}
            isFetchingPreviousPage={workflowsQuery.isFetchingPreviousPage}
            nextBoundary={nextWorkflowsBoundary}
            onLinkWorkflow={openLinkWorkflow}
            onLoadNext={() => {
              void workflowsQuery.fetchNextPage();
            }}
            onLoadPrevious={() => {
              void workflowsQuery.fetchPreviousPage();
            }}
            onSortChange={onSortChange}
            previousBoundary={previousWorkflowsBoundary}
            projectID={projectID}
            sort={sort}
            workflows={workflows}
          />,
        )}
      />
      <TaskInitiatingActionDialogs
        continuation={initiatingAction}
        onResult={handleTaskInitiatingDialogResult}
      />
    </>
  );
}

function ProjectTasksContent({
  columnLayout,
  countsBoundary,
  entries,
  layoutChangeScrollBehavior,
  onLinkWorkflow,
  onNewTask,
  onScrollElementChange,
  projectID,
  scrollRestorationReady,
  taskCount,
  viewMemory,
  workflowColumnRelevant,
  workflowCount,
  workflowsBoundary,
  workflowStrip,
  newTaskAvailable,
}: Readonly<{
  columnLayout: ProjectTaskColumnLayout;
  countsBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  entries: readonly ProjectTaskListEntry[];
  layoutChangeScrollBehavior: "natural" | "preserve-leading-item";
  onLinkWorkflow: () => void;
  onNewTask: () => void;
  onScrollElementChange: (element: HTMLDivElement | null) => void;
  projectID: string;
  scrollRestorationReady: boolean;
  taskCount: number | null;
  viewMemory: ProjectTasksViewMemory;
  workflowColumnRelevant: boolean;
  workflowCount: number;
  workflowsBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  workflowStrip: ReactNode;
  newTaskAvailable: boolean;
}>) {
  const { t } = useTranslation();
  const listEntries = entries;
  const { containerRef, widthPx } = useProjectTaskListWidth();
  const visibleColumns = resolveProjectTaskVisibleColumns(widthPx, columnLayout, workflowColumnRelevant);
  const workflowsResolved = workflowsBoundary === undefined;
  const memory = viewMemory.read();
  return (
    <TasksShell workflowStrip={workflowStrip}>
      {workflowsResolved && workflowCount === 0 ? (
        <ProjectTasksEmpty
          actionLabel={t("workflowLibrary.linkWorkflow")}
          body={t("home.prototype.noLinkedWorkflowsBody")}
          onAction={onLinkWorkflow}
          title={t("home.prototype.noLinkedWorkflowsTitle")}
        />
      ) : countsBoundary?.state === "error" && taskCount === null ? (
        <InfiniteListBoundary direction="initial" state={countsBoundary} />
      ) : taskCount === 0 ? (
        <ProjectTasksEmpty
          actionLabel={newTaskAvailable ? t("board.newTask") : t("workflowLibrary.linkWorkflow")}
          body={t("home.prototype.noTasksBody")}
          onAction={newTaskAvailable ? onNewTask : onLinkWorkflow}
          title={t("home.prototype.noTasksTitle")}
        />
      ) : (
        <div
          className="h-full min-h-0"
          data-dependencies-visible={visibleColumns.dependencies}
          data-labels-visible={visibleColumns.labelsPx !== null}
          data-testid="project-task-list-layout"
          data-title-visible={visibleColumns.title}
          data-workflow-visible={visibleColumns.workflow}
          ref={containerRef}
          style={projectTaskColumnStyle(columnLayout, visibleColumns)}
        >
          <VirtualizedInfiniteList
            ariaLabel={t("home.prototype.projectTasksGrid")}
            className="project-task-list-scroll h-full min-h-0 w-full min-w-0 overflow-x-hidden overflow-y-auto pb-[var(--space-2)] hide-scrollbar"
            estimateSize={() => 44}
            getItemAnchorKey={(entry) => (entry.kind === "task" ? entry.anchorKey : entry.key)}
            getItemKey={(entry) => entry.key}
            getItemOccurrenceKey={(entry) => entry.key}
            getItemWrapperProps={projectTaskEntryWrapperProps}
            hasNextPage={false}
            isFetchingNextPage={false}
            itemRole="row"
            items={listEntries}
            layoutChangeScrollBehavior={layoutChangeScrollBehavior}
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
            testId="project-task-list-grid"
            visibilityTriggers={projectTaskVisibilityTriggers(listEntries)}
          />
        </div>
      )}
      {countsBoundary === undefined || taskCount === null ? null : (
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
        <div
          aria-label={cell.ariaLabel}
          className={cell.className}
          data-project-task-column={cell.key}
          key={cell.key}
          role="columnheader"
        >
          {cell.content}
        </div>
      ));
    case "group-header":
      return (
        <div
          className="col-span-full flex h-10 items-center gap-[var(--space-2)]"
          aria-colspan={projectTaskColumnCount}
          role="gridcell"
        >
          <span className="inline-grid h-full w-4 shrink-0 place-items-center">
            <ProjectTaskStatusLegend
              definitions={entry.definitions}
              trigger={<ProjectTaskGroupIcon group={entry.groupKey} />}
            />
          </span>
          <button
            aria-expanded={entry.expanded}
            className="flex h-full min-w-0 flex-1 items-center gap-[var(--space-2)] text-left text-sm outline-none"
            onClick={entry.onToggle}
            type="button"
          >
            <ChevronRight
              aria-hidden="true"
              className={`shrink-0 transition-transform duration-100 motion-reduce:transition-none ${
                entry.expanded ? "rotate-90" : ""
              }`}
              size={14}
              strokeWidth={1.8}
            />
            <span aria-hidden="true" className="font-semibold">
              {entry.label}
            </span>
            <span aria-hidden="true" className="text-xs tabular-nums text-[var(--color-muted)]">
              {entry.count}
            </span>
            <span className="sr-only">{entry.ariaLabel}</span>
          </button>
        </div>
      );
    case "boundary":
      return (
        <div className="col-span-full" aria-colspan={projectTaskColumnCount} role="gridcell">
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
        <div
          aria-label={cell.ariaLabel}
          className={cell.className}
          data-project-task-column={cell.key}
          key={cell.key}
          role="gridcell"
        >
          {cell.content}
        </div>
      ));
  }
}

function ProjectTaskGroupIcon({ group }: Readonly<{ group: ProjectTaskGroup }>) {
  if (group === "done") {
    return <CheckCircle2 aria-hidden="true" className="text-[var(--color-success)]" size={15} />;
  }
  if (group === "backlog") {
    return <Circle aria-hidden="true" className="text-[var(--color-muted)]" size={15} />;
  }
  return <CircleDot aria-hidden="true" className="text-[var(--color-primary)]" size={15} />;
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
            itemKey: entry.direction === "next" ? projectTaskNextPageTriggerKey(entries, entry) : entry.key,
            onVisible: entry.onLoadMore ?? (() => undefined),
            requestGeneration: `${entry.groupKey}:${entry.direction}:${entry.requestGeneration}`,
          },
        ]
      : [],
  );
}

function projectTaskNextPageTriggerKey(
  entries: readonly ProjectTaskListEntry[],
  boundary: Extract<ProjectTaskListEntry, { kind: "boundary" }>,
): string {
  const groupTasks = entries.filter(
    (entry): entry is Extract<ProjectTaskListEntry, { kind: "task" }> =>
      entry.kind === "task" && entry.groupKey === boundary.groupKey,
  );
  const prefetchDistance = projectTaskGroupPageSize * projectTaskGroupPrefetchPages;
  return groupTasks[Math.max(0, groupTasks.length - prefetchDistance)]?.key ?? boundary.key;
}

function TasksShell({
  children,
  workflowStrip,
}: Readonly<{
  children: ReactNode;
  workflowStrip: ReactNode;
}>) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      {workflowStrip}
      <div className="relative mx-[var(--space-4)] mb-[var(--space-3)] min-h-0 flex-1 overflow-hidden">
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
