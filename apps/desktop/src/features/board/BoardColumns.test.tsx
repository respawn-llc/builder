import { fireEvent, render, screen } from "@testing-library/react";
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
      message: "message",
      retryLabel: "retry",
      onRetry: vi.fn(),
    };

    render(
      <KanbanColumn
        actionsDisabled={false}
        cards={[]}
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
    const chrome = alert.closest(".pointer-events-none");
    expect(alert.closest("[data-testid='kanban-column-scroll-column-1']")).toBeNull();
    expect(chrome).toHaveClass("pointer-events-none");
    expect(alert.parentElement).toHaveClass("pointer-events-auto");
    expect(screen.getByRole("alert")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button"));
    expect(boundary.onRetry).toHaveBeenCalledOnce();
  });
});
