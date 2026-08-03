import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { KanbanColumn } from "./BoardColumns";
import type { KanbanCardVM, KanbanColumnVM } from "./BoardColumnViewModel";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@/app-facade", () => ({
  formatRelativeTime: () => "now",
  useSidebar: () => ({ openSidebar: vi.fn() }),
}));

const column: KanbanColumnVM = {
  assigneeRole: "",
  id: "column-1",
  name: "Doing",
  taskCount: 1,
};

describe("KanbanColumn retained replacement boundary", () => {
  it("keeps the Retry boundary in fixed column chrome outside the scroll content", () => {
    const boundary = {
      state: "error" as const,
      message: "board.cardsLoadRetryBody",
      retryLabel: "app.retry",
      onRetry: vi.fn(),
    };

    render(
      <KanbanColumn
        actionsDisabled={false}
        cards={[card]}
        column={column}
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

    const alert = screen.getByTestId("virtual-boundary-replacement");
    const list = screen.getByTestId("kanban-column-scroll-column-1");
    expect(alert.closest("[data-testid='kanban-column-scroll-column-1']")).toBeNull();
    expect(alert.parentElement?.contains(alert)).toBe(true);
    expect(list).not.toContainElement(alert);
    expect(screen.getByRole("alert")).toContainElement(screen.getByText("board.cardsLoadRetryBody"));
  });
});

const card = {
  actions: { canDelete: false, canInterrupt: false, canResume: false, canStart: false },
  activeNodeIDs: [],
  borderTone: "default",
  dependencyProgress: null,
  id: "task-1",
  labels: [],
  preview: { markdown: "", truncated: false },
  statusKind: "queued",
  shortID: "T-1",
  title: "Task",
  updatedAt: 0,
  workspaceChipLabel: null,
} as unknown as KanbanCardVM;
