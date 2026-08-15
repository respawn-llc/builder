import { Plus } from "lucide-react";
import { useCallback, useLayoutEffect, useRef, type UIEvent } from "react";
import { useTranslation } from "react-i18next";

import { useAppNavigation } from "@/app-facade";
import {
  autoLoadAvailable,
  InfiniteListBoundary,
  InteractiveChip,
  type VirtualizedInfiniteListBoundaryState,
} from "@/ui";
import type { ProjectTaskWorkflowItem } from "./projectTaskWorkflows";

type WorkflowStripAnchor = Readonly<{
  offsetWithinViewportPx: number;
  workflowID: string;
}>;

type WorkflowStripWindow = Readonly<{
  count: number;
  firstWorkflowID: string | undefined;
  lastWorkflowID: string | undefined;
}>;

export function ProjectWorkflowStrip({
  hasNextPage,
  hasPreviousPage,
  initialBoundary,
  isFetchingNextPage,
  isFetchingPreviousPage,
  nextBoundary,
  onLinkWorkflow,
  onLoadNext,
  onLoadPrevious,
  previousBoundary,
  projectID,
  workflows,
}: Readonly<{
  hasNextPage: boolean;
  hasPreviousPage: boolean;
  initialBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  isFetchingNextPage: boolean;
  isFetchingPreviousPage: boolean;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  onLinkWorkflow: () => void;
  onLoadNext: () => void;
  onLoadPrevious: () => void;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  projectID: string;
  workflows: readonly ProjectTaskWorkflowItem[];
}>) {
  const { t } = useTranslation();
  const navigation = useAppNavigation();
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const workflowElementsRef = useRef(new Map<string, HTMLSpanElement>());
  const pendingAnchorRef = useRef<WorkflowStripAnchor | null>(null);
  const previousWindowRef = useRef<WorkflowStripWindow | null>(null);
  const captureAnchor = useCallback(() => {
    const scroll = scrollRef.current;
    if (scroll === null) {
      pendingAnchorRef.current = null;
      return;
    }
    const anchor = workflows
      .map((workflow) => ({
        element: workflowElementsRef.current.get(workflow.id),
        workflowID: workflow.id,
      }))
      .find(
        ({ element }) =>
          element !== undefined && element.offsetLeft + element.offsetWidth > scroll.scrollLeft,
      );
    pendingAnchorRef.current =
      anchor?.element === undefined
        ? null
        : {
            offsetWithinViewportPx: anchor.element.offsetLeft - scroll.scrollLeft,
            workflowID: anchor.workflowID,
          };
  }, [workflows]);
  const loadVisibleEdges = useCallback(
    (element: HTMLDivElement) => {
      if (
        autoLoadAvailable(hasPreviousPage, previousBoundary) &&
        !isFetchingPreviousPage &&
        element.scrollLeft <= 1
      ) {
        captureAnchor();
        onLoadPrevious();
      }
      const trailingDistance = element.scrollWidth - element.clientWidth - element.scrollLeft;
      if (autoLoadAvailable(hasNextPage, nextBoundary) && !isFetchingNextPage && trailingDistance <= 1) {
        captureAnchor();
        onLoadNext();
      }
    },
    [
      hasNextPage,
      hasPreviousPage,
      captureAnchor,
      isFetchingNextPage,
      isFetchingPreviousPage,
      nextBoundary,
      onLoadNext,
      onLoadPrevious,
      previousBoundary,
    ],
  );
  useLayoutEffect(() => {
    const element = scrollRef.current;
    if (element === null) {
      return;
    }
    const currentWindow = workflowStripWindow(workflows);
    if (!workflowStripWindowsEqual(previousWindowRef.current, currentWindow)) {
      const anchor = pendingAnchorRef.current;
      const anchorElement = anchor === null ? undefined : workflowElementsRef.current.get(anchor.workflowID);
      if (anchor !== null && anchorElement !== undefined) {
        element.scrollLeft = Math.max(0, anchorElement.offsetLeft - anchor.offsetWithinViewportPx);
      }
      pendingAnchorRef.current = null;
      previousWindowRef.current = currentWindow;
    }
    loadVisibleEdges(element);
  }, [loadVisibleEdges, workflows]);
  const onScroll = (event: UIEvent<HTMLDivElement>) => {
    loadVisibleEdges(event.currentTarget);
  };

  return (
    <div
      className="flex shrink-0 gap-[var(--space-2)] overflow-x-auto px-[var(--space-4)] py-[var(--space-3)] hide-scrollbar"
      onScroll={onScroll}
      ref={scrollRef}
    >
      {initialBoundary === undefined ? (
        <>
          {previousBoundary === undefined ? null : (
            <div className="min-w-64 shrink-0">
              <InfiniteListBoundary direction="previous" state={previousBoundary} />
            </div>
          )}
          {workflows.map((workflow) => (
            <span
              className="shrink-0"
              key={workflow.id}
              ref={(element) => {
                if (element === null) {
                  workflowElementsRef.current.delete(workflow.id);
                  return;
                }
                workflowElementsRef.current.set(workflow.id, element);
              }}
            >
              <InteractiveChip
                onClick={() => void navigation.openProject(projectID, workflow.id)}
                title={workflow.description}
              >
                {workflow.name}
              </InteractiveChip>
            </span>
          ))}
          {nextBoundary === undefined ? null : (
            <div className="min-w-64 shrink-0">
              <InfiniteListBoundary direction="next" state={nextBoundary} />
            </div>
          )}
          <InteractiveChip className="shrink-0" onClick={onLinkWorkflow}>
            <Plus aria-hidden="true" size={14} strokeWidth={1.8} />
            {t("workflowLibrary.linkWorkflow")}
          </InteractiveChip>
        </>
      ) : (
        <div className="min-w-64">
          <InfiniteListBoundary direction="initial" state={initialBoundary} />
        </div>
      )}
    </div>
  );
}

function workflowStripWindow(workflows: readonly ProjectTaskWorkflowItem[]): WorkflowStripWindow {
  return {
    count: workflows.length,
    firstWorkflowID: workflows.at(0)?.id,
    lastWorkflowID: workflows.at(-1)?.id,
  };
}

function workflowStripWindowsEqual(left: WorkflowStripWindow | null, right: WorkflowStripWindow): boolean {
  return (
    left !== null &&
    left.count === right.count &&
    left.firstWorkflowID === right.firstWorkflowID &&
    left.lastWorkflowID === right.lastWorkflowID
  );
}
