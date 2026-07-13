import { fireEvent, render, screen, within } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeAll, vi } from "vitest";

import { appI18n, initializeI18n } from "../../i18n/setup";
import { KanbanColumn } from "./BoardColumns";
import type { KanbanCardVM, KanbanColumnVM } from "./BoardColumnViewModel";
import { boardCardDragPayloadType, decodeBoardCardDragPayload } from "./BoardDragTypes";

describe("KanbanColumn", () => {
  beforeAll(async () => {
    await initializeI18n();
  });

  it("renders load-more with shared spinner and hidden accessible label", () => {
    render(
      <I18nextProvider i18n={appI18n}>
        <KanbanColumn
          actionsDisabled={false}
          cards={[card]}
          column={column}
          dropState="idle"
          hasMoreCards
          isFirstActive={false}
          isLoadingMoreCards
          onCardClick={() => undefined}
          onCardDragEnd={() => undefined}
          onCardDragStart={() => undefined}
          onDeleteTask={() => undefined}
          onDropTask={() => undefined}
          onInterruptTask={() => undefined}
          onLoadMoreCards={() => undefined}
          onResumeTask={() => undefined}
        />
      </I18nextProvider>,
    );

    expect(screen.getByRole("status")).toContainElement(screen.getByTestId("spinner"));
  });

  it("renders an empty collapsed column as an expandable bar without task pagination surface", () => {
    const onExpandColumn = vi.fn();

    render(
      <I18nextProvider i18n={appI18n}>
        <KanbanColumn
          actionsDisabled={false}
          cards={[]}
          column={{ ...column, assigneeRole: "reviewer", name: "Review", taskCount: 0 }}
          dropState="idle"
          hasMoreCards={false}
          isCollapsed
          isFirstActive={false}
          isLoadingMoreCards={false}
          onCardClick={() => undefined}
          onCardDragEnd={() => undefined}
          onCardDragStart={() => undefined}
          onDeleteTask={() => undefined}
          onDropTask={() => undefined}
          onExpandColumn={onExpandColumn}
          onInterruptTask={() => undefined}
          onLoadMoreCards={() => undefined}
          onResumeTask={() => undefined}
        />
      </I18nextProvider>,
    );

    const renderedColumn = screen.getByRole("listitem", { name: "Review" });

    expect(renderedColumn).toHaveAttribute("data-collapsed", "true");
    expect(screen.queryByTestId("kanban-column-task-count-backlog")).not.toBeInTheDocument();
    expect(screen.queryByTestId("kanban-column-scroll-backlog")).not.toBeInTheDocument();
    expect(within(renderedColumn).queryByText("reviewer")).not.toBeInTheDocument();

    fireEvent.click(within(renderedColumn).getByRole("button", { name: "Expand Review" }));

    expect(onExpandColumn).toHaveBeenCalledTimes(1);
  });

  it("keeps action buttons in the chip row, uses danger interrupt, and omits run count chip", () => {
    const onInterruptTask = vi.fn();
    const onCardClick = vi.fn();

    render(
      <I18nextProvider i18n={appI18n}>
        <KanbanColumn
          actionsDisabled={false}
          cards={[
            {
              ...card,
              actions: {
                ...card.actions,
                canInterrupt: true,
              },
            },
          ]}
          column={column}
          dropState="idle"
          hasMoreCards={false}
          isFirstActive={false}
          isLoadingMoreCards={false}
          onCardClick={onCardClick}
          onCardDragEnd={() => undefined}
          onCardDragStart={() => undefined}
          onDeleteTask={() => undefined}
          onDropTask={() => undefined}
          onInterruptTask={onInterruptTask}
          onLoadMoreCards={() => undefined}
          onResumeTask={() => undefined}
        />
      </I18nextProvider>,
    );

    const footer = screen.getByTestId("task-card-footer");
    expect(screen.queryByRole("button", { name: "Open task detail" })).not.toBeInTheDocument();

    const interruptButton = within(footer).getByRole("button", { name: "Interrupt" });
    expect(interruptButton).toHaveAttribute("type", "button");

    fireEvent.click(interruptButton);

    expect(onInterruptTask).toHaveBeenCalledWith("task-1");
    expect(onCardClick).not.toHaveBeenCalled();
  });

  it("treats question-waiting cards as answer-blocked instead of interruptible", () => {
    render(
      <I18nextProvider i18n={appI18n}>
        <KanbanColumn
          actionsDisabled={false}
          cards={[
            {
              ...card,
              actions: {
                ...card.actions,
                canInterrupt: true,
              },
              statusKind: "waiting_question",
              title: "Question task",
            },
          ]}
          column={column}
          dropState="idle"
          hasMoreCards={false}
          isFirstActive={false}
          isLoadingMoreCards={false}
          onCardClick={() => undefined}
          onCardDragEnd={() => undefined}
          onCardDragStart={() => undefined}
          onDeleteTask={() => undefined}
          onDropTask={() => undefined}
          onInterruptTask={() => undefined}
          onLoadMoreCards={() => undefined}
          onResumeTask={() => undefined}
        />
      </I18nextProvider>,
    );

    const questionCard = screen.getByRole("article", { name: "Question task" });

    expect(questionCard).toHaveAttribute("data-task-card-state", "waiting-answer");
    expect(within(questionCard).queryByRole("button", { name: "Interrupt" })).not.toBeInTheDocument();
    expect(within(questionCard).queryByTestId("task-card-active-run-spinner")).not.toBeInTheDocument();
  });

  it("renders bounded card previews, workspace chips only when present, and distinct question/approval border tones", () => {
    render(
      <I18nextProvider i18n={appI18n}>
        <KanbanColumn
          actionsDisabled={false}
          cards={[
            {
              ...card,
              preview: {
                markdown: "Bounded preview from the server: **semantic source remains intact**",
                truncated: true,
              },
              id: "task-question",
              borderTone: "primary",
              statusKind: "waiting_question",
              title: "Question task",
              workspaceChipLabel: null,
            },
            {
              ...card,
              id: "task-approval",
              borderTone: "secondary",
              statusKind: "waiting_approval",
              title: "Approval task",
              workspaceChipLabel: "Other workspace",
            },
          ]}
          column={column}
          dropState="idle"
          hasMoreCards={false}
          isFirstActive={false}
          isLoadingMoreCards={false}
          onCardClick={() => undefined}
          onCardDragEnd={() => undefined}
          onCardDragStart={() => undefined}
          onDeleteTask={() => undefined}
          onDropTask={() => undefined}
          onInterruptTask={() => undefined}
          onLoadMoreCards={() => undefined}
          onResumeTask={() => undefined}
        />
      </I18nextProvider>,
    );

    const questionCard = screen.getByRole("article", { name: "Question task" });
    const approvalCard = screen.getByRole("article", { name: "Approval task" });

    expect(within(questionCard).getByTestId("task-card-body")).toHaveTextContent(
      "Bounded preview from the server: semantic source remains intact",
    );
    expect(within(questionCard).getByTestId("task-card-preview-ellipsis")).toHaveTextContent("…");
    expect(
      within(within(questionCard).getByTestId("task-card-body")).getByText(
        "Bounded preview from the server: semantic source remains intact",
      ),
    ).toHaveClass("markdown-plain-text");
    expect(within(approvalCard).queryByTestId("task-card-preview-ellipsis")).not.toBeInTheDocument();
    expect(within(questionCard).queryByTestId("task-card-chip-slot")).not.toBeInTheDocument();
    expect(within(approvalCard).getByTestId("task-card-chip-slot")).toHaveTextContent("Other workspace");
    expect(questionCard).toHaveAttribute("data-task-card-border-tone", "primary");
    expect(approvalCard).toHaveAttribute("data-task-card-border-tone", "secondary");
  });

  it("shows an active run spinner only for running cards", () => {
    render(
      <I18nextProvider i18n={appI18n}>
        <KanbanColumn
          actionsDisabled={false}
          cards={[
            { ...card, id: "task-running", statusKind: "running", title: "Running task" },
            { ...card, id: "task-question", statusKind: "waiting_question", title: "Question task" },
            { ...card, id: "task-approval", statusKind: "waiting_approval", title: "Approval task" },
            { ...card, id: "task-interrupted", statusKind: "interrupted", title: "Interrupted task" },
            { ...card, id: "task-canceled", statusKind: "canceled", title: "Canceled task" },
          ]}
          column={column}
          dropState="idle"
          hasMoreCards={false}
          isFirstActive={false}
          isLoadingMoreCards={false}
          onCardClick={() => undefined}
          onCardDragEnd={() => undefined}
          onCardDragStart={() => undefined}
          onDeleteTask={() => undefined}
          onDropTask={() => undefined}
          onInterruptTask={() => undefined}
          onLoadMoreCards={() => undefined}
          onResumeTask={() => undefined}
        />
      </I18nextProvider>,
    );

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

    render(
      <I18nextProvider i18n={appI18n}>
        <KanbanColumn
          actionsDisabled={false}
          cards={[card]}
          column={column}
          dropState="idle"
          hasMoreCards={false}
          isFirstActive={false}
          isLoadingMoreCards={false}
          onCardClick={onCardClick}
          onCardDragEnd={() => undefined}
          onCardDragStart={() => undefined}
          onDeleteTask={() => undefined}
          onDropTask={() => undefined}
          onInterruptTask={() => undefined}
          onLoadMoreCards={() => undefined}
          onResumeTask={() => undefined}
        />
      </I18nextProvider>,
    );

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

    render(
      <I18nextProvider i18n={appI18n}>
        <KanbanColumn
          actionsDisabled={false}
          cards={[card]}
          column={column}
          dropState="idle"
          hasMoreCards={false}
          isFirstActive={false}
          isLoadingMoreCards={false}
          onCardClick={onCardClick}
          onCardDragEnd={() => undefined}
          onCardDragStart={() => undefined}
          onDeleteTask={onDeleteTask}
          onDropTask={() => undefined}
          onInterruptTask={() => undefined}
          onLoadMoreCards={() => undefined}
          onResumeTask={() => undefined}
        />
      </I18nextProvider>,
    );

    fireEvent.contextMenu(screen.getByRole("article", { name: "Task" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));

    expect(onDeleteTask).toHaveBeenCalledWith("task-1");
    expect(onCardClick).not.toHaveBeenCalled();
  });

  it("starts override drags for active cards without start or move targets", () => {
    const onCardDragStart = vi.fn();
    const onCardClick = vi.fn();

    render(
      <I18nextProvider i18n={appI18n}>
        <KanbanColumn
          actionsDisabled={false}
          cards={[
            {
              ...card,
              actions: {
                ...card.actions,
                canStart: false,
                manualMoveTargetNodeIDs: [],
              },
            },
          ]}
          column={column}
          dropState="blocked"
          hasMoreCards={false}
          isFirstActive={false}
          isLoadingMoreCards={false}
          onCardClick={onCardClick}
          onCardDragEnd={() => undefined}
          onCardDragStart={onCardDragStart}
          onDeleteTask={() => undefined}
          onDropTask={() => undefined}
          onInterruptTask={() => undefined}
          onLoadMoreCards={() => undefined}
          onResumeTask={() => undefined}
        />
      </I18nextProvider>,
    );

    const dataTransfer = new TestDataTransfer();
    const renderedCard = screen.getByRole("article", { name: "Task" });
    expect(renderedCard).toHaveAttribute("draggable", "true");
    expect(screen.getByRole("listitem", { name: "Backlog" })).toHaveAttribute("data-drop-state", "blocked");

    fireEvent.dragStart(renderedCard, { dataTransfer });

    expect(dataTransfer.getData("text/task-id")).toBe("task-1");
    expect(decodeBoardCardDragPayload(dataTransfer.getData(boardCardDragPayloadType))).toEqual({
      taskID: "task-1",
      canStart: false,
      activeNodeIDs: ["backlog"],
      statusKind: "backlog",
      manualMoveTargetNodeIDs: [],
    });
    expect(dataTransfer.setDragImage).toHaveBeenCalledTimes(1);
    expect(dataTransfer.setDragImage.mock.calls[0]?.[0]).toBeInstanceOf(HTMLElement);
    expect(onCardDragStart).toHaveBeenCalledTimes(1);
    expect(onCardDragStart).toHaveBeenCalledWith({
      instance: { columnID: "backlog", taskID: "task-1" },
      lastVirtualIndex: 0,
      payload: {
        taskID: "task-1",
        canStart: false,
        activeNodeIDs: ["backlog"],
        statusKind: "backlog",
        manualMoveTargetNodeIDs: [],
      },
      snapshot: {
        ...card,
        actions: {
          ...card.actions,
          canStart: false,
          manualMoveTargetNodeIDs: [],
        },
      },
    });
    expect(onCardClick).not.toHaveBeenCalled();
  });

  it("accepts board-card dragover before drop-state rerenders from idle", () => {
    render(
      <I18nextProvider i18n={appI18n}>
        <KanbanColumn
          actionsDisabled={false}
          cards={[]}
          column={column}
          dropState="idle"
          hasMoreCards={false}
          isFirstActive={false}
          isLoadingMoreCards={false}
          onCardClick={() => undefined}
          onCardDragEnd={() => undefined}
          onCardDragStart={() => undefined}
          onDeleteTask={() => undefined}
          onDropTask={() => undefined}
          onInterruptTask={() => undefined}
          onLoadMoreCards={() => undefined}
          onResumeTask={() => undefined}
        />
      </I18nextProvider>,
    );

    const dataTransfer = new TestDataTransfer();
    dataTransfer.setData(boardCardDragPayloadType, "{}");
    const event = createCancelableDragEvent("dragover", dataTransfer);

    screen.getByRole("listitem", { name: "Backlog" }).dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
    expect(dataTransfer.dropEffect).toBe("move");
  });
});

class TestDataTransfer {
  readonly #values = new Map<string, string>();
  effectAllowed = "all";
  dropEffect = "none";
  readonly setDragImage = vi.fn();

  get types(): readonly string[] {
    return [...this.#values.keys()];
  }

  setData(type: string, value: string): void {
    this.#values.set(type, value);
  }

  getData(type: string): string {
    return this.#values.get(type) ?? "";
  }
}

function createCancelableDragEvent(type: string, dataTransfer: TestDataTransfer): Event {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", { value: dataTransfer });
  return event;
}

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
