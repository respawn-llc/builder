import { act, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { KanbanColumn } from "./BoardColumns";
import type { KanbanCardVM, KanbanColumnVM } from "./BoardColumnViewModel";
import { useBoardDragLifecycle, type ActiveBoardCardDrag } from "./BoardDragState";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@/app-facade", () => ({
  formatRelativeTime: () => "now",
  useOwnedSidebarRoots: () => ({ open: vi.fn() }),
}));

const column: KanbanColumnVM = {
  assigneeRole: "",
  id: "column-1",
  name: "Doing",
  taskCount: 1,
};

const card: KanbanCardVM = {
  actions: {
    canDelete: false,
    canInterrupt: false,
    canResume: true,
    canStart: false,
  },
  activeNodeIDs: ["column-1"],
  borderTone: "default",
  dependencyProgress: null,
  id: "task-1",
  labels: [],
  preview: { markdown: "", truncated: false },
  shortID: "KNT-1",
  statusKind: "interrupted",
  title: "Task",
  updatedAt: Date.UTC(2026, 0, 1),
  workspaceChipLabel: null,
};

const drag: ActiveBoardCardDrag = {
  instance: { columnID: column.id, taskID: card.id },
  lastCardIndex: 0,
  payload: {
    activeNodeIDs: card.activeNodeIDs,
    canStart: card.actions.canStart,
    statusKind: card.statusKind,
    taskID: card.id,
  },
  snapshot: card,
};

describe("KanbanColumn retained replacement boundary", () => {
  it("keeps the Retry boundary in fixed column chrome outside the scroll content", () => {
    const boundary = {
      state: "error" as const,
      message: "message",
      retryLabel: "retry",
      onRetry: vi.fn(),
    };

    render(
      <KanbanColumn
        actionsDisabled={false}
        cards={[]}
        column={column}
        dragDisabled={false}
        dropState="idle"
        hasMoreCards={false}
        initialBoundary={undefined}
        isFirstActive
        isLoadingMoreCards={false}
        nextBoundary={undefined}
        onCardClick={vi.fn()}
        onCardDragEnd={vi.fn()}
        onCardDragStart={vi.fn()}
        onDeleteTask={vi.fn()}
        onDropTask={vi.fn()}
        onInterruptTask={vi.fn()}
        onLoadMoreCards={vi.fn()}
        onResumeTask={vi.fn()}
        replacementBoundary={boundary}
      />,
    );

    expect(screen.getByRole("alert")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button"));
    expect(boundary.onRetry).toHaveBeenCalledOnce();
  });

  it("keeps server-authorized actions while an invalid Workflow disables drag", () => {
    const onCardDragStart = vi.fn();
    const onResumeTask = vi.fn();

    render(
      <KanbanColumn
        actionsDisabled={false}
        cards={[card]}
        column={column}
        dragDisabled
        dropState="idle"
        hasMoreCards={false}
        initialBoundary={undefined}
        isFirstActive
        isLoadingMoreCards={false}
        nextBoundary={undefined}
        onCardClick={vi.fn()}
        onCardDragEnd={vi.fn()}
        onCardDragStart={onCardDragStart}
        onDeleteTask={vi.fn()}
        onDropTask={vi.fn()}
        onInterruptTask={vi.fn()}
        onLoadMoreCards={vi.fn()}
        onResumeTask={onResumeTask}
        replacementBoundary={undefined}
      />,
    );

    const renderedCard = screen.getByRole("article", { name: "Task" });
    expect(renderedCard).toHaveAttribute("draggable", "false");

    fireEvent.dragStart(renderedCard);
    expect(onCardDragStart).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "board.resume" }));
    expect(onResumeTask).toHaveBeenCalledWith("task-1");
  });

  it("does not restore a drag after the selected Workflow becomes invalid", async () => {
    const rootRef = { current: null };
    const { result, rerender } = renderHook(
      ({ disabled }) => useBoardDragLifecycle({ disabled, rootRef }),
      { initialProps: { disabled: false } },
    );

    act(() => {
      result.current.start(drag);
    });
    expect(result.current.activeDrag).toBe(drag);

    rerender({ disabled: true });
    expect(result.current.activeDrag).toBeNull();

    await waitFor(() => {
      expect(result.current.dragBlocked).toBe(false);
    });
    rerender({ disabled: false });
    expect(result.current.activeDrag).toBeNull();
  });
});
