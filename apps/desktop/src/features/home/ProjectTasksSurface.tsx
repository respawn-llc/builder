import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useCallback, useState, type ReactNode } from "react";
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
  projectTaskFinalPageAnchor,
  useProjectTaskListData,
  useProjectTaskListEvents,
  type ProjectTaskGroup,
  type ProjectTaskGroupData,
} from "./projectTaskListData";
import type { ProjectTasksViewMemory } from "./projectTasksViewMemory";
import { projectTaskColumnCount, projectTaskColumnEntry, projectTaskEntry } from "./ProjectTaskRow";

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
  const initialMemory = viewMemory.read();
  const [disclosure, setDisclosure] = useState(initialMemory.disclosure);
  const [anchors, setAnchors] = useState(initialMemory.anchors);
  const [labelEditorTaskID, setLabelEditorTaskID] = useState<string | null>(null);
  const [finalNavigationRequest, setFinalNavigationRequest] = useState<
    Readonly<{ group: ProjectTaskGroup; key: string; offset: number }> | null
  >(null);
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
    labelEditorTaskID,
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
        const memory = viewMemory.read();
        if (memory.disclosure.backlog) {
          const nextAnchors = { ...memory.anchors, backlog: 0 };
          viewMemory.setAnchors(nextAnchors);
          setAnchors(nextAnchors);
        }
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
  const requestFinalEntry = useCallback(
    (group: ProjectTaskGroup, count: number) => {
      const offset = projectTaskFinalPageAnchor(count);
      const nextAnchors = { ...anchors, [group]: offset };
      const key = `final-${group}-${offset.toString()}`;
      viewMemory.setAnchors(nextAnchors);
      setAnchors(nextAnchors);
      setFinalNavigationRequest({
        group,
        key,
        offset,
      });
    },
    [anchors, viewMemory],
  );
  const taskDetailID = activeDestination?.kind === "taskDetail" ? activeDestination.taskID : null;
  const activeTaskDetailMode =
    activeDestination?.kind === "taskDetail" ? (activeDestination.mode ?? "shift") : null;
  const openTaskDetail = useCallback(
    (taskID: string) => {
      setLabelEditorTaskID(null);
      const mode = activeTaskDetailMode ?? sidebarMode;
      open({ kind: "taskDetail", mode, taskID });
    },
    [activeTaskDetailMode, open, sidebarMode],
  );
  const toggleLabelEditor = useCallback((taskID: string) => {
    setLabelEditorTaskID((current) => (current === taskID ? null : taskID));
  }, []);
  const presentation = projectTasksPresentation({
    counts,
    data,
    disclosure,
    labelEditorTaskID,
    onLabelsActivate: toggleLabelEditor,
    onTaskActivate: openTaskDetail,
    onToggle: toggleGroup,
    projectID,
    taskDetailID,
    t,
  });
  const finalNavigation = finalNavigationState({
    counts,
    data,
    disclosure,
    request: finalNavigationRequest,
  });
  return (
    <ProjectTasksContent
      boardBoundary={boardBoundary}
      countsBoundary={countsBoundary}
      entries={presentation.entries}
      finalEntryKey={finalNavigation.entryKey}
      finalEntryRequestKey={finalNavigation.requestKey}
      finalNavigationRequiresRequest={finalNavigation.requiresRequest}
      onLinkWorkflow={openLinkWorkflow}
      onNewTask={openNewTask}
      onRequestFinalEntry={() => {
        if (finalNavigation.request === null) return;
        requestFinalEntry(finalNavigation.request.group, finalNavigation.request.count);
      }}
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
  labelEditorTaskID,
  onLabelsActivate,
  onTaskActivate,
  onToggle,
  projectID,
  taskDetailID,
  t,
}: Readonly<{
  counts: Readonly<Record<ProjectTaskGroup, number>> | undefined;
  data: ReturnType<typeof useProjectTaskListData>;
  disclosure: Readonly<Record<ProjectTaskGroup, boolean>>;
  labelEditorTaskID: string | null;
  onLabelsActivate: (taskID: string) => void;
  onTaskActivate: (taskID: string) => void;
  onToggle: (group: ProjectTaskGroup) => void;
  projectID: string;
  taskDetailID: string | null;
  t: ReturnType<typeof useTranslation>["t"];
}>): Readonly<{
  entries: readonly VirtualizedGroupedGridEntry[];
  taskCount: number | null;
}> {
  if (counts === undefined) {
    return { entries: [projectTaskColumnEntry(t)], taskCount: null };
  }
  return {
    entries: groupedEntries({
      counts,
      data,
      disclosure,
      labelEditorTaskID,
      onLabelsActivate,
      onTaskActivate,
      onToggle,
      projectID,
      taskDetailID,
      t,
    }),
    taskCount: counts.active + counts.backlog + counts.done,
  };
}

function finalNavigationState({
  counts,
  data,
  disclosure,
  request,
}: Readonly<{
  counts: Readonly<Record<ProjectTaskGroup, number>> | undefined;
  data: ReturnType<typeof useProjectTaskListData>;
  disclosure: Readonly<Record<ProjectTaskGroup, boolean>>;
  request: Readonly<{ group: ProjectTaskGroup; key: string; offset: number }> | null;
}>): Readonly<{
  entryKey: string;
  request: Readonly<{ count: number; group: ProjectTaskGroup }> | null;
  requestKey: string | null;
  requiresRequest: boolean;
}> {
  if (counts === undefined) {
    return { entryKey: "columns", request: null, requestKey: null, requiresRequest: false };
  }
  const group = [...projectTaskGroups].reverse().find((candidate) => counts[candidate] > 0) ?? null;
  if (group === null) {
    return { entryKey: "columns", request: null, requestKey: null, requiresRequest: false };
  }
  if (!disclosure[group]) {
    return finalNavigationReady(`group-${group}`);
  }
  return expandedFinalNavigationState(group, counts[group], data[group], request);
}

function expandedFinalNavigationState(
  group: ProjectTaskGroup,
  count: number,
  data: ProjectTaskGroupData,
  request: Readonly<{ group: ProjectTaskGroup; key: string; offset: number }> | null,
): Readonly<{
  entryKey: string;
  request: Readonly<{ count: number; group: ProjectTaskGroup }> | null;
  requestKey: string | null;
  requiresRequest: boolean;
}> {
  const finalOffset = projectTaskFinalPageAnchor(count);
  const finalPageReady = data.pageParams.includes(finalOffset);
  const finalTaskID = finalPageReady
    ? (data.tasks.at(-1)?.id ?? `group-${group}`)
    : `group-${group}`;
  const pendingRequest =
    request?.group === group && request.offset === finalOffset ? request.key : null;
  return {
    entryKey: finalTaskID,
    request: pendingRequest === null && !finalPageReady ? { count, group } : null,
    requestKey: finalPageReady ? pendingRequest : null,
    requiresRequest: !finalPageReady,
  };
}

function finalNavigationReady(entryKey: string): Readonly<{
  entryKey: string;
  request: null;
  requestKey: null;
  requiresRequest: false;
}> {
  return { entryKey, request: null, requestKey: null, requiresRequest: false };
}

function ProjectTasksContent({
  boardBoundary,
  countsBoundary,
  entries,
  finalEntryKey,
  finalEntryRequestKey,
  finalNavigationRequiresRequest,
  onLinkWorkflow,
  onNewTask,
  onRequestFinalEntry,
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
  finalEntryRequestKey: string | null;
  finalNavigationRequiresRequest: boolean;
  onLinkWorkflow: () => void;
  onNewTask: () => void;
  onRequestFinalEntry: () => void;
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
  if (countsBoundary?.state === "error") {
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
  const memory = viewMemory.read();
  const newTaskWorkflowSelectionAvailable =
    workflows.length === 1 || workflows.some((workflow) => workflow.isProjectDefault);
  return (
    <TasksShell workflows={workflows} onLinkWorkflow={onLinkWorkflow} projectID={projectID}>
      {taskCount === 0 ? (
        <ProjectTasksEmpty
          actionLabel={
            newTaskWorkflowSelectionAvailable
              ? t("board.newTask")
              : t("workflowLibrary.linkWorkflow")
          }
          body={t("home.prototype.noTasksBody")}
          onAction={newTaskWorkflowSelectionAvailable ? onNewTask : onLinkWorkflow}
          title={t("home.prototype.noTasksTitle")}
        />
      ) : (
        <VirtualizedGroupedGrid
          ariaLabel={t("home.prototype.projectTasksGrid")}
          className="h-full min-h-0 min-w-[880px] overflow-auto [scrollbar-width:thin] [&::-webkit-scrollbar:vertical]:hidden"
          columnCount={projectTaskColumnCount}
          entries={entries}
          estimateSize={() => 38}
          navigation={{
            downLabel: t("home.prototype.jumpToBottom"),
            finalEntryKey,
            onRequestFinalEntry,
            requestKey: finalEntryRequestKey,
            requiresFinalEntryRequest: finalNavigationRequiresRequest,
            upLabel: t("home.prototype.jumpToTop"),
          }}
          canApplyPixelOffset={entries.some((entry) => entry.kind === "task")}
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
          <InteractiveChip
            className="shrink-0"
            key={workflow.id}
            onClick={() => {
              void navigation.openProject(projectID, workflow.id);
            }}
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

function groupedEntries({
  counts,
  data,
  disclosure,
  labelEditorTaskID,
  onLabelsActivate,
  onTaskActivate,
  onToggle,
  projectID,
  taskDetailID,
  t,
}: Readonly<{
  counts: Readonly<Record<ProjectTaskGroup, number>>;
  data: ReturnType<typeof useProjectTaskListData>;
  disclosure: Readonly<Record<ProjectTaskGroup, boolean>>;
  labelEditorTaskID: string | null;
  onLabelsActivate: (taskID: string) => void;
  onTaskActivate: (taskID: string) => void;
  onToggle: (group: ProjectTaskGroup) => void;
  projectID: string;
  taskDetailID: string | null;
  t: ReturnType<typeof useTranslation>["t"];
}>): readonly VirtualizedGroupedGridEntry[] {
  return [
    projectTaskColumnEntry(t),
    ...projectTaskGroups.flatMap((group) => {
      const count = counts[group];
      if (count === 0) return [];
      const groupData = data[group];
      const entries: VirtualizedGroupedGridEntry[] = [
        {
          kind: "group-header",
          key: `group-${group}`,
          groupKey: group,
          label: t(`home.prototype.statusGroups.${group}`),
          count,
          ariaLabel: t("home.prototype.taskGroupCount", {
            count,
            group: t(`home.prototype.statusGroups.${group}`),
          }),
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
        entries.push(boundaryEntry({ data: groupData, direction: "initial", group, state: initial, t }));
      } else {
        if (
          groupData.hasPreviousPage ||
          groupData.isFetchingPreviousPage ||
          groupData.isFetchPreviousPageError
        ) {
          entries.push(
            boundaryEntry({
              data: groupData,
              direction: "previous",
              group,
              state: groupBoundary(groupData, "previous", t),
              t,
            }),
          );
        }
        entries.push(
          ...groupData.tasks.map((task) =>
            projectTaskEntry({
              group,
              labelEditorTaskID,
              onLabelsActivate,
              onTaskActivate,
              projectID,
              task,
              taskDetailID,
              t,
            }),
          ),
        );
        if (groupData.hasNextPage || groupData.isFetchingNextPage || groupData.isFetchNextPageError) {
          entries.push(
            boundaryEntry({
              data: groupData,
              direction: "next",
              group,
              state: groupBoundary(groupData, "next", t),
              t,
            }),
          );
        }
      }
      return entries;
    }),
  ];
}

function boundaryEntry({
  data,
  direction,
  group,
  state,
  t,
}: Readonly<{
  data: ProjectTaskGroupData;
  direction: "initial" | "previous" | "next";
  group: ProjectTaskGroup;
  state: VirtualizedInfiniteListBoundaryState | undefined;
  t: ReturnType<typeof useTranslation>["t"];
}>): VirtualizedGroupedGridEntry {
  return {
    kind: "boundary",
    key: `${group}-${direction}`,
    groupKey: group,
    direction,
    state,
    hasMore: direction === "previous" ? data.hasPreviousPage : data.hasNextPage,
    isFetching: direction === "previous" ? data.isFetchingPreviousPage : data.isFetchingNextPage,
    loadingLabel: t("app.loadingMore"),
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
