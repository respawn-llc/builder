import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useCallback, useRef, useState, type ReactNode } from "react";
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
import {
  firstExpandedProjectTaskGroup,
  projectTaskTopNavigationRequiresRequest,
  topNavigationScrollRequest,
} from "./projectTaskListNavigation";
import { projectTasksPresentation } from "./projectTaskListPresentation";
import type { ProjectTasksViewMemory } from "./projectTasksViewMemory";
import { projectTaskColumnCount } from "./ProjectTaskRow";

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
  const [pendingTaskReveal, setPendingTaskReveal] = useState<
    Readonly<{ key: string; taskID: string }> | null
  >(null);
  const [topNavigationRequest, setTopNavigationRequest] = useState<
    Readonly<{ group: ProjectTaskGroup; key: string }> | null
  >(null);
  const scrollRequestSequence = useRef(0);
  const nextScrollRequestKey = useCallback((prefix: string) => {
    scrollRequestSequence.current += 1;
    return `${prefix}-${scrollRequestSequence.current.toString()}`;
  }, []);
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
      onCreated: async (taskID) => {
        const memory = viewMemory.read();
        if (memory.disclosure.backlog) {
          const nextAnchors = { ...memory.anchors, backlog: 0 };
          viewMemory.setAnchors(nextAnchors);
          setAnchors(nextAnchors);
          setPendingTaskReveal({
            key: nextScrollRequestKey(`created-${taskID}`),
            taskID,
          });
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
      setFinalNavigationRequest((request) => (request?.group === group ? null : request));
      setTopNavigationRequest((request) => (request?.group === group ? null : request));
    }
    setDisclosure(next);
  };
  const requestFinalEntry = useCallback(
    (group: ProjectTaskGroup, count: number) => {
      const offset = projectTaskFinalPageAnchor(count);
      const nextAnchors = { ...anchors, [group]: offset };
      const key = nextScrollRequestKey(`final-${group}-${offset.toString()}`);
      viewMemory.setAnchors(nextAnchors);
      setAnchors(nextAnchors);
      setFinalNavigationRequest({
        group,
        key,
        offset,
      });
      if (anchors[group] === offset) {
        void data[group].refetch();
      }
    },
    [anchors, data, nextScrollRequestKey, viewMemory],
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
  const topNavigationRequiresRequest = projectTaskTopNavigationRequiresRequest(
    counts,
    disclosure,
    anchors,
  );
  const scrollRequest = preferredScrollRequest(
    topNavigationScrollRequest(topNavigationRequest, data, disclosure),
    finalNavigation.scrollRequest,
    createdTaskScrollRequest(pendingTaskReveal, presentation.entries),
  );
  const onScrollRequestApplied = scrollRequestAppliedHandler({
    finalNavigationRequest,
    pendingTaskReveal,
    topNavigationRequest,
    setFinalNavigationRequest,
    setPendingTaskReveal,
    setTopNavigationRequest,
  });
  return (
    <ProjectTasksContent
      boardBoundary={boardBoundary}
      countsBoundary={countsBoundary}
      entries={presentation.entries}
      finalEntryKey={finalNavigation.entryKey}
      finalEntryRequestKey={finalNavigation.requestKey}
      finalNavigationInFlight={finalNavigation.inFlight}
      finalNavigationRequiresRequest={finalNavigation.requiresRequest}
      topNavigationRequiresRequest={topNavigationRequiresRequest}
      onLinkWorkflow={openLinkWorkflow}
      onNewTask={openNewTask}
      onRequestFinalEntry={() => {
        if (finalNavigation.request === null) return;
        requestFinalEntry(finalNavigation.request.group, finalNavigation.request.count);
      }}
      onRequestTop={() => {
        setFinalNavigationRequest(null);
        setPendingTaskReveal(null);
        const group = firstExpandedProjectTaskGroup(counts, disclosure);
        const currentAnchors = viewMemory.read().anchors;
        if (group === null || currentAnchors[group] === 0) {
          setTopNavigationRequest(null);
          return false;
        }
        const nextAnchors = { ...currentAnchors, [group]: 0 };
        viewMemory.setAnchors(nextAnchors);
        setAnchors(nextAnchors);
        setTopNavigationRequest({
          group,
          key: nextScrollRequestKey(`top-${group}`),
        });
        return true;
      }}
      onScrollRequestApplied={onScrollRequestApplied}
      onScrollElementChange={onScrollElementChange}
      projectID={projectID}
      scrollRequest={scrollRequest}
      taskCount={presentation.taskCount}
      viewMemory={viewMemory}
      workflows={workflows}
    />
  );
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
  inFlight: boolean;
  request: Readonly<{ count: number; group: ProjectTaskGroup }> | null;
  requestKey: string | null;
  requiresRequest: boolean;
  scrollRequest:
    | Readonly<{ align: "end"; entryKey: string; key: string; target: "entry" }>
    | undefined;
}> {
  if (counts === undefined) {
    return {
      entryKey: "columns",
      inFlight: false,
      request: null,
      requestKey: null,
      requiresRequest: false,
      scrollRequest: undefined,
    };
  }
  const group = [...projectTaskGroups].reverse().find((candidate) => counts[candidate] > 0) ?? null;
  if (group === null) {
    return {
      entryKey: "columns",
      inFlight: false,
      request: null,
      requestKey: null,
      requiresRequest: false,
      scrollRequest: undefined,
    };
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
  inFlight: boolean;
  request: Readonly<{ count: number; group: ProjectTaskGroup }> | null;
  requestKey: string | null;
  requiresRequest: boolean;
  scrollRequest:
    | Readonly<{ align: "end"; entryKey: string; key: string; target: "entry" }>
    | undefined;
}> {
  const finalOffset = projectTaskFinalPageAnchor(count);
  const finalPageReady = data.pageParams.includes(finalOffset) && !data.isPlaceholderData;
  const finalTaskID = finalPageReady
    ? (data.tasks.at(-1)?.id ?? `group-${group}`)
    : `group-${group}`;
  const requestInFlight = finalNavigationRequestInFlight(request, group, finalOffset, data.isError);
  return {
    entryKey: finalTaskID,
    inFlight: requestInFlight,
    request: !requestInFlight && !finalPageReady ? { count, group } : null,
    requestKey: request?.key ?? null,
    requiresRequest: !finalPageReady,
    scrollRequest: finalNavigationScrollRequest(finalPageReady, finalTaskID, request),
  };
}

function finalNavigationRequestInFlight(
  request: Readonly<{ group: ProjectTaskGroup; key: string; offset: number }> | null,
  group: ProjectTaskGroup,
  finalOffset: number,
  failed: boolean,
): boolean {
  return request?.group === group && request.offset === finalOffset && !failed;
}

function finalNavigationScrollRequest(
  finalPageReady: boolean,
  finalTaskID: string,
  request: Readonly<{ group: ProjectTaskGroup; key: string; offset: number }> | null,
) {
  if (!finalPageReady || request === null) return undefined;
  return {
    align: "end" as const,
    entryKey: finalTaskID,
    key: request.key,
    target: "entry" as const,
  };
}

function finalNavigationReady(entryKey: string): Readonly<{
  entryKey: string;
  inFlight: false;
  request: null;
  requestKey: null;
  requiresRequest: false;
  scrollRequest: undefined;
}> {
  return {
    entryKey,
    inFlight: false,
    request: null,
    requestKey: null,
    requiresRequest: false,
    scrollRequest: undefined,
  };
}

function createdTaskScrollRequest(
  reveal: Readonly<{ key: string; taskID: string }> | null,
  entries: readonly VirtualizedGroupedGridEntry[],
) {
  if (
    reveal === null ||
    !entries.some((entry) => entry.kind === "task" && entry.key === reveal.taskID)
  ) {
    return undefined;
  }
  return {
    align: "end" as const,
    entryKey: reveal.taskID,
    key: reveal.key,
    target: "entry" as const,
  };
}

function preferredScrollRequest(
  topRequest:
    | Readonly<{ align: "end"; entryKey: string; key: string; target: "entry" }>
    | Readonly<{ key: string; target: "top" }>
    | undefined,
  finalRequest:
    | Readonly<{ align: "end"; entryKey: string; key: string; target: "entry" }>
    | undefined,
  createdTaskRequest:
    | Readonly<{ align: "end"; entryKey: string; key: string; target: "entry" }>
    | undefined,
) {
  return topRequest ?? finalRequest ?? createdTaskRequest;
}

function scrollRequestAppliedHandler({
  finalNavigationRequest,
  pendingTaskReveal,
  topNavigationRequest,
  setFinalNavigationRequest,
  setPendingTaskReveal,
  setTopNavigationRequest,
}: Readonly<{
  finalNavigationRequest: Readonly<{ group: ProjectTaskGroup; key: string; offset: number }> | null;
  pendingTaskReveal: Readonly<{ key: string; taskID: string }> | null;
  topNavigationRequest: Readonly<{ group: ProjectTaskGroup; key: string }> | null;
  setFinalNavigationRequest: (request: null) => void;
  setPendingTaskReveal: (request: null) => void;
  setTopNavigationRequest: (request: null) => void;
}>): (key: string) => void {
  return (key) => {
    if (finalNavigationRequest?.key === key) {
      setFinalNavigationRequest(null);
    }
    if (pendingTaskReveal?.key === key) {
      setPendingTaskReveal(null);
    }
    if (topNavigationRequest?.key === key) {
      setTopNavigationRequest(null);
    }
  };
}

function ProjectTasksContent({
  boardBoundary,
  countsBoundary,
  entries,
  finalEntryKey,
  finalEntryRequestKey,
  finalNavigationInFlight,
  finalNavigationRequiresRequest,
  topNavigationRequiresRequest,
  onLinkWorkflow,
  onNewTask,
  onRequestFinalEntry,
  onRequestTop,
  onScrollRequestApplied,
  onScrollElementChange,
  projectID,
  scrollRequest,
  taskCount,
  viewMemory,
  workflows,
}: Readonly<{
  boardBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  countsBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  entries: readonly VirtualizedGroupedGridEntry[];
  finalEntryKey: string;
  finalEntryRequestKey: string | null;
  finalNavigationInFlight: boolean;
  finalNavigationRequiresRequest: boolean;
  topNavigationRequiresRequest: boolean;
  onLinkWorkflow: () => void;
  onNewTask: () => void;
  onRequestFinalEntry: () => void;
  onRequestTop: () => boolean;
  onScrollRequestApplied: (key: string) => void;
  onScrollElementChange: (element: HTMLDivElement | null) => void;
  projectID: string;
  scrollRequest:
    | Readonly<{ align: "end"; entryKey: string; key: string; target: "entry" }>
    | Readonly<{ key: string; target: "top" }>
    | undefined;
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
            downDisabled: finalNavigationInFlight,
            finalEntryKey,
            onRequestFinalEntry,
            onRequestTop,
            requestKey: finalEntryRequestKey,
            requiresFinalEntryRequest: finalNavigationRequiresRequest,
            requiresTopEntryRequest: topNavigationRequiresRequest,
            upLabel: t("home.prototype.jumpToTop"),
          }}
          canApplyPixelOffset={entries.some((entry) => entry.kind === "task")}
          onScrollElementChange={onScrollElementChange}
          onScrollRequestApplied={onScrollRequestApplied}
          pixelOffsetRequest={createVirtualizedPixelOffsetRequest(
            `restore-${memory.scrollRequestSequence.toString()}`,
            memory.verticalOffsetPx,
          )}
          scrollRequest={scrollRequest}
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
