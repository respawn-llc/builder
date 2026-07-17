import { fireEvent, render, screen, within } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeAll, vi } from "vitest";

import { appI18n, initializeI18n } from "../../i18n/setup";
import { createBoardDragEvent, TestDataTransfer } from "../../testSupport/boardDrag";
import { KanbanColumn, type KanbanColumnProps } from "./BoardColumns";
import type { KanbanCardVM, KanbanColumnVM } from "./BoardColumnViewModel";
import { boardCardDragPayloadType } from "./BoardDragTypes";

describe("KanbanColumn", () => {
  beforeAll(async () => {
    await initializeI18n();
  });

  it("renders load-more with shared spinner and hidden accessible label", () => {
    renderColumn({ hasMoreCards: true, isLoadingMoreCards: true });

    expect(screen.getByRole("status")).toContainElement(screen.getByTestId("spinner"));
  });

  it("renders an empty collapsed column as an expandable bar without task pagination surface", () => {
    const onExpandColumn = vi.fn();

    renderColumn({
      cards: [],
      column: { ...column, assigneeRole: "reviewer", name: "Review", taskCount: 0 },
      isCollapsed: true,
      onExpandColumn,
    });

    const renderedColumn = screen.getByRole("listitem", { name: "Review" });

    expect(renderedColumn).toHaveAttribute("data-collapsed", "true");
    expect(screen.queryByTestId("kanban-column-task-count-backlog")).not.toBeInTheDocument();
    expect(screen.queryByTestId("kanban-column-scroll-backlog")).not.toBeInTheDocument();
    expect(within(renderedColumn).queryByText("reviewer")).not.toBeInTheDocument();

    fireEvent.click(within(renderedColumn).getByRole("button", { name: "Expand Review" }));

    expect(onExpandColumn).toHaveBeenCalledTimes(1);
  });

  it("omits the footer when the card has no status, chip, or action content", () => {
    renderColumn();

    expect(screen.queryByTestId("task-card-footer")).not.toBeInTheDocument();
  });

  it("keeps action buttons in the chip row, uses danger interrupt, and omits run count chip", () => {
    const onInterruptTask = vi.fn();
    const onCardClick = vi.fn();

    renderColumn({
      cards: [testCard({ actions: { canInterrupt: true } })],
      onCardClick,
      onInterruptTask,
    });

    const footer = screen.getByTestId("task-card-footer");
    expect(screen.queryByRole("button", { name: "Open task detail" })).not.toBeInTheDocument();

    const interruptButton = within(footer).getByRole("button", { name: "Interrupt" });
    expect(interruptButton).toHaveAttribute("type", "button");
    expect(interruptButton).not.toHaveTextContent("Interrupt");

    fireEvent.click(interruptButton);

    expect(onInterruptTask).toHaveBeenCalledWith("task-1");
    expect(onCardClick).not.toHaveBeenCalled();
  });

  it("uses an icon-only resume control without opening task detail", () => {
    const onResumeTask = vi.fn();
    const onCardClick = vi.fn();

    renderColumn({
      cards: [testCard({ actions: { canResume: true } })],
      onCardClick,
      onResumeTask,
    });

    const resumeButton = within(screen.getByTestId("task-card-footer")).getByRole("button", {
      name: "Resume",
    });
    expect(resumeButton).not.toHaveTextContent("Resume");

    fireEvent.click(resumeButton);

    expect(onResumeTask).toHaveBeenCalledWith("task-1");
    expect(onCardClick).not.toHaveBeenCalled();
  });

  it("treats question-waiting cards as answer-blocked instead of interruptible", () => {
    renderColumn({
      cards: [
        testCard({
          actions: { canInterrupt: true },
          statusKind: "waiting_question",
          title: "Question task",
        }),
      ],
    });

    const questionCard = screen.getByRole("article", { name: "Question task" });

    expect(questionCard).toHaveAttribute("data-task-card-state", "waiting-answer");
    expect(within(questionCard).queryByRole("button", { name: "Interrupt" })).not.toBeInTheDocument();
    expect(within(questionCard).queryByTestId("task-card-active-run-spinner")).not.toBeInTheDocument();
  });

  it("renders bounded card previews, workspace chips only when present, and distinct question/approval border tones", () => {
    renderColumn({
      cards: [
        testCard({
          preview: {
            markdown: "Bounded preview from the server: **semantic source remains intact**",
            truncated: true,
          },
          id: "task-question",
          borderTone: "primary",
          statusKind: "waiting_question",
          title: "Question task",
          workspaceChipLabel: null,
        }),
        testCard({
          id: "task-approval",
          borderTone: "secondary",
          statusKind: "waiting_approval",
          title: "Approval task",
          workspaceChipLabel: "Other workspace",
        }),
      ],
    });

    const questionCard = screen.getByRole("article", { name: "Question task" });
    const approvalCard = screen.getByRole("article", { name: "Approval task" });

    expect(within(questionCard).getByTestId("task-card-body")).toHaveTextContent(
      "Bounded preview from the server: semantic source remains intact",
    );
    expect(within(questionCard).getByTestId("task-card-preview-ellipsis")).toHaveTextContent("…");
    expect(within(approvalCard).queryByTestId("task-card-preview-ellipsis")).not.toBeInTheDocument();
    expect(within(questionCard).queryByTestId("task-card-chip-slot")).not.toBeInTheDocument();
    expect(within(approvalCard).getByTestId("task-card-chip-slot")).toHaveTextContent("Other workspace");
    expect(questionCard).toHaveAttribute("data-task-card-border-tone", "primary");
    expect(approvalCard).toHaveAttribute("data-task-card-border-tone", "secondary");
  });

  it("shows an active run spinner only for running cards", () => {
    renderColumn({
      cards: [
        testCard({ id: "task-running", statusKind: "running", title: "Running task" }),
        testCard({ id: "task-question", statusKind: "waiting_question", title: "Question task" }),
        testCard({ id: "task-approval", statusKind: "waiting_approval", title: "Approval task" }),
        testCard({ id: "task-interrupted", statusKind: "interrupted", title: "Interrupted task" }),
        testCard({ id: "task-canceled", statusKind: "canceled", title: "Canceled task" }),
      ],
    });

    expect(
      within(screen.getByRole("article", { name: "Running task" })).getByTestId(
        "task-card-active-run-spinner",
      ),
    ).toBeInTheDocument();
    expect(
      within(screen.getByRole("article", { name: "Question task" })).queryByTestId(
        "task-card-active-run-spinner",
      ),
    ).not.toBeInTheDocument();
    expect(
      within(screen.getByRole("article", { name: "Approval task" })).queryByTestId(
        "task-card-active-run-spinner",
      ),
    ).not.toBeInTheDocument();
    expect(
      within(screen.getByRole("article", { name: "Interrupted task" })).queryByTestId(
        "task-card-active-run-spinner",
      ),
    ).not.toBeInTheDocument();
    expect(
      within(screen.getByRole("article", { name: "Canceled task" })).queryByTestId(
        "task-card-active-run-spinner",
      ),
    ).not.toBeInTheDocument();
  });

  it("opens task detail when clicking any non-action area of the card", () => {
    const onCardClick = vi.fn();

    renderColumn({
      cards: [testCard({ workspaceChipLabel: "Other workspace" })],
      onCardClick,
    });

    const renderedCard = screen.getByTestId("task-card");
    fireEvent.click(screen.getByTestId("task-card-title"));
    fireEvent.click(screen.getByTestId("task-card-body"));
    fireEvent.click(screen.getByTestId("task-card-footer"));
    renderedCard.focus();
    expect(renderedCard).toHaveFocus();
    fireEvent.keyDown(renderedCard, { key: "Enter" });

    expect(onCardClick).toHaveBeenCalledTimes(4);
    expect(onCardClick).toHaveBeenCalledWith("task-1");
  });

  it("deletes cards from the context menu without opening task detail", async () => {
    const onCardClick = vi.fn();
    const onDeleteTask = vi.fn();

    renderColumn({ onCardClick, onDeleteTask });

    fireEvent.contextMenu(screen.getByRole("article", { name: "Task" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));

    expect(onDeleteTask).toHaveBeenCalledWith("task-1");
    expect(onCardClick).not.toHaveBeenCalled();
  });

  it("starts override drags for active cards without start or move targets", () => {
    const onCardDragStart = vi.fn();
    const onCardClick = vi.fn();
    const draggableCard = testCard({
      actions: { canStart: false, manualMoveTargetNodeIDs: [] },
    });

    renderColumn({
      cards: [draggableCard],
      dropState: "blocked",
      onCardClick,
      onCardDragStart,
    });

    const dataTransfer = new TestDataTransfer();
    const renderedCard = screen.getByRole("article", { name: "Task" });
    expect(renderedCard).toHaveAttribute("draggable", "true");
    expect(screen.getByRole("listitem", { name: "Backlog" })).toHaveAttribute("data-drop-state", "blocked");

    fireEvent.dragStart(renderedCard, { dataTransfer });

    expect(dataTransfer.types).toEqual([boardCardDragPayloadType]);
    expect(dataTransfer.setDragImage).toHaveBeenCalledTimes(1);
    expect(dataTransfer.setDragImage.mock.calls[0]?.[0]).toBeInstanceOf(HTMLElement);
    expect(onCardDragStart).toHaveBeenCalledTimes(1);
    expect(onCardDragStart).toHaveBeenCalledWith({
      instance: { columnID: "backlog", taskID: "task-1" },
      lastCardIndex: 0,
      payload: {
        taskID: "task-1",
        canStart: false,
        activeNodeIDs: ["backlog"],
        statusKind: "backlog",
        manualMoveTargetNodeIDs: [],
      },
      snapshot: draggableCard,
    });
    expect(onCardClick).not.toHaveBeenCalled();
  });

  it("accepts board-card dragover before drop-state rerenders from idle", () => {
    renderColumn({ cards: [] });

    const dataTransfer = new TestDataTransfer();
    dataTransfer.setData(boardCardDragPayloadType, "board-card");
    const event = createBoardDragEvent("dragover", { dataTransfer });

    screen.getByRole("listitem", { name: "Backlog" }).dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
    expect(dataTransfer.dropEffect).toBe("move");
  });

  it("ignores unrelated dragover while the column has no active board drag", () => {
    renderColumn({ cards: [] });

    const dataTransfer = new TestDataTransfer();
    dataTransfer.setData("text/plain", "unrelated");
    const event = createBoardDragEvent("dragover", { dataTransfer });

    screen.getByRole("listitem", { name: "Backlog" }).dispatchEvent(event);

    expect(event.defaultPrevented).toBe(false);
    expect(dataTransfer.dropEffect).toBe("none");
  });
});

const column: KanbanColumnVM = {
  assigneeRole: "",
  id: "backlog",
  name: "Backlog",
  taskCount: 1,
};

const card: KanbanCardVM = {
  activeNodeIDs: ["backlog"],
  actions: {
    canInterrupt: false,
    canResume: false,
    canStart: true,
    manualMoveTargetNodeIDs: [],
  },
  preview: { markdown: "Body", truncated: false },
  id: "task-1",
  shortID: "T-1",
  workspaceChipLabel: null,
  borderTone: "default",
  statusKind: "backlog",
  statusRunIDs: [],
  title: "Task",
  updatedAt: Date.UTC(2026, 0, 1),
};

type CardOverrides = Omit<Partial<KanbanCardVM>, "actions"> &
  Readonly<{ actions?: Partial<KanbanCardVM["actions"]> }>;

function testCard(overrides: CardOverrides = {}): KanbanCardVM {
  return {
    ...card,
    ...overrides,
    actions: { ...card.actions, ...overrides.actions },
  };
}

const noop = () => undefined;
const defaultColumnProps = {
  actionsDisabled: false,
  cards: [card],
  column,
  dropState: "idle",
  hasMoreCards: false,
  isFirstActive: false,
  isLoadingMoreCards: false,
  onCardClick: noop,
  onCardDragEnd: noop,
  onCardDragStart: noop,
  onDeleteTask: noop,
  onDropTask: noop,
  onInterruptTask: noop,
  onLoadMoreCards: noop,
  onResumeTask: noop,
} satisfies KanbanColumnProps;

function renderColumn(overrides: Partial<KanbanColumnProps> = {}) {
  return render(
    <I18nextProvider i18n={appI18n}>
      <KanbanColumn {...defaultColumnProps} {...overrides} />
    </I18nextProvider>,
  );
}
