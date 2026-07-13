import {
  memo,
  useCallback,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type DragEvent,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";
import { Maximize2 } from "lucide-react";

import { formatRelativeTime } from "../../app/formatters";
import {
  Badge,
  Button,
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuTrigger,
  InfiniteListBoundary,
  MarkdownPlainText,
  Spinner,
  VirtualizedInfiniteList,
  type VirtualizedInfiniteListBoundaryState,
} from "../../ui";
import { cx } from "../../ui/classes";
import {
  type BoardColumnDropState,
  boardCardDragPayloadType,
  encodeBoardCardDragPayload,
} from "./BoardDragTypes";
import type { ActiveBoardCardDrag } from "./BoardDragState";
import { boardCardInstanceKey, type BoardCardInstance, type BoardCardInstanceKey } from "./BoardCardInstance";
import { useBoardCardInstanceVisibility } from "./BoardCardVisibilityRegistry";
import type { KanbanCardVM, KanbanColumnVM, KanbanGroupVM } from "./BoardColumnViewModel";
import { useBoardCardMotion } from "./BoardCardMotionContext";

export type KanbanColumnProps = Readonly<{
  cards: readonly KanbanCardVM[];
  column: KanbanColumnVM;
  hasMoreCards: boolean;
  hasPreviousCards?: boolean | undefined;
  isLoadingMoreCards: boolean;
  isLoadingPreviousCards?: boolean | undefined;
  initialBoundary?: VirtualizedInfiniteListBoundaryState | undefined;
  previousBoundary?: VirtualizedInfiniteListBoundaryState | undefined;
  nextBoundary?: VirtualizedInfiniteListBoundaryState | undefined;
  isFirstActive: boolean;
  isCollapsed?: boolean;
  dropState: BoardColumnDropState;
  actionsDisabled: boolean;
  columnRef?: (element: HTMLElement | null) => void;
  scrollportRef?: (element: HTMLElement | null) => void;
  onCardClick: (taskID: string) => void;
  onCardDragEnd: () => void;
  onCardDragStart: (drag: ActiveBoardCardDrag) => void;
  onDeleteTask: (taskID: string) => void;
  onDropTask: (event: DragEvent<HTMLElement>) => void;
  onExpandColumn?: () => void;
  onInterruptTask: (taskID: string) => void;
  onLoadMoreCards: () => void;
  onLoadPreviousCards?: (() => void) | undefined;
  onResumeTask: (taskID: string) => void;
  pinnedItemKeys?: ReadonlySet<string> | undefined;
}>;

export function KanbanGroup({
  group,
  hideHeader = false,
  children,
}: Readonly<{
  group: KanbanGroupVM;
  hideHeader?: boolean;
  children: ReactNode;
}>) {
  return (
    <section
      className={cx(
        "inline-grid h-full min-h-0 w-max align-top",
        hideHeader
          ? "grid-rows-[0_minmax(0,1fr)] gap-0"
          : "grid-rows-[auto_minmax(0,1fr)] gap-[var(--space-2)]",
      )}
      role="listitem"
    >
      <header
        aria-hidden={hideHeader ? true : undefined}
        className={hideHeader ? "invisible w-0 min-w-0 max-w-0 overflow-hidden" : undefined}
        data-testid={`kanban-group-header-${group.id}`}
      >
        <h2 className="m-0 text-[1rem] font-bold">{group.name}</h2>
      </header>
      <div className="flex h-full min-h-0 gap-[var(--space-2)]">{children}</div>
    </section>
  );
}

export function KanbanColumn({
  cards,
  column,
  hasMoreCards,
  hasPreviousCards = false,
  isLoadingMoreCards,
  isLoadingPreviousCards = false,
  initialBoundary,
  previousBoundary,
  nextBoundary,
  isFirstActive,
  isCollapsed = false,
  dropState,
  actionsDisabled,
  columnRef,
  scrollportRef,
  onCardClick,
  onCardDragEnd,
  onCardDragStart,
  onDeleteTask,
  onDropTask,
  onExpandColumn,
  onInterruptTask,
  onLoadMoreCards,
  onLoadPreviousCards,
  onResumeTask,
  pinnedItemKeys,
}: KanbanColumnProps) {
  const { t } = useTranslation();
  const headerRef = useRef<HTMLElement | null>(null);
  const [headerHeight, setHeaderHeight] = useState(0);
  useLayoutEffect(() => {
    const header = headerRef.current;
    if (header === null) {
      return;
    }
    const measure = () => {
      setHeaderHeight(header.getBoundingClientRect().height);
    };
    measure();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(header);
    return () => {
      observer?.disconnect();
    };
  }, [isCollapsed]);
  const columnStyle: CSSProperties & Readonly<Record<"--board-column-header-height", string>> = {
    "--board-column-header-height": `${headerHeight.toString()}px`,
  };
  const columnClassName = isCollapsed
    ? `island-glass board-column-morph board-column-collapsed board-column-drop-${dropState} flex h-full min-h-0 w-[64px] shrink-0 rounded-[var(--radius-xl)] p-[var(--space-2)] align-top`
    : `island-glass board-column-morph board-column-drop-${dropState} relative h-full min-h-0 w-[min(420px,80vw)] shrink-0 overflow-hidden rounded-[var(--radius-xl)] align-top`;
  const virtualCards = useMemo<readonly BoardVirtualCard[]>(
    () =>
      cards.map((card, virtualIndex) => {
        const instance = { columnID: column.id, taskID: card.id };
        return {
          card,
          instance,
          key: boardCardInstanceKey(instance),
          virtualIndex,
        };
      }),
    [cards, column.id],
  );
  const emptyContent = initialBoundaryContent(initialBoundary);
  const listHeader = boardDropHint(isFirstActive, t("board.dropToStart"));
  const visiblePreviousBoundary = readyBoundary(initialBoundary, previousBoundary);
  const visibleNextBoundary = readyBoundary(initialBoundary, nextBoundary);
  return (
    <section
      aria-label={column.name}
      className={columnClassName}
      data-collapsed={isCollapsed ? "true" : "false"}
      data-drop-state={dropState}
      ref={columnRef}
      style={columnStyle}
      onDragOver={(event) => {
        if (dropState === "idle" && !hasBoardCardDragData(event.dataTransfer)) {
          return;
        }
        event.preventDefault();
        event.dataTransfer.dropEffect = "move";
      }}
      onDrop={(event) => {
        onDropTask(event);
      }}
      role="listitem"
    >
      {isCollapsed ? (
        <CollapsedColumnHeader column={column} onExpand={onExpandColumn} />
      ) : (
        <>
          <header
            className="pointer-events-none absolute top-0 right-0 left-0 z-10 flex items-start justify-between gap-[var(--space-2)] px-[var(--space-3)] pt-[var(--space-3)] pb-[var(--space-3)]"
            ref={headerRef}
          >
            <div>
              <h2 className="m-0 text-[1rem]">{column.name}</h2>
              {column.assigneeRole.length > 0 ? (
                <p className="m-0 font-mono text-sm text-[var(--color-muted)]">{column.assigneeRole}</p>
              ) : null}
            </div>
            <Badge
              title={t("board.taskCount", { count: column.taskCount })}
              tone={isFirstActive ? "info" : "neutral"}
            >
              <span data-testid={`kanban-column-task-count-${column.id}`}>
                {t("board.taskCount", { count: column.taskCount })}
              </span>
            </Badge>
          </header>
          <VirtualizedInfiniteList
            ariaLabel={column.name}
            className="board-column-scroll absolute inset-0 min-h-0 overflow-y-auto px-[var(--space-3)] hide-scrollbar"
            empty={emptyContent}
            estimateSize={estimateBoardCardRowSize}
            getItemKey={getBoardVirtualCardKey}
            hasNextPage={autoLoadAvailable(hasMoreCards, nextBoundary)}
            hasPreviousPage={autoLoadAvailable(hasPreviousCards, previousBoundary)}
            header={listHeader}
            isFetchingNextPage={isLoadingMoreCards}
            isFetchingPreviousPage={isLoadingPreviousCards}
            items={virtualCards}
            loadingLabel={t("app.loadingMore")}
            nextBoundary={visibleNextBoundary}
            onLoadMore={onLoadMoreCards}
            onLoadPrevious={onLoadPreviousCards}
            onScrollElementChange={scrollportRef}
            paddingStart={headerHeight}
            pinnedItemKeys={pinnedItemKeys}
            previousBoundary={visiblePreviousBoundary}
            renderItem={(item) => (
              <TaskCard
                actionsDisabled={actionsDisabled}
                card={item.card}
                instance={item.instance}
                onCardClick={onCardClick}
                onCardDragEnd={onCardDragEnd}
                onCardDragStart={onCardDragStart}
                onDeleteTask={onDeleteTask}
                onInterruptTask={onInterruptTask}
                onResumeTask={onResumeTask}
                virtualIndex={item.virtualIndex}
              />
            )}
            rowSpacing="compact"
            testId={`kanban-column-scroll-${column.id}`}
          />
        </>
      )}
    </section>
  );
}

function CollapsedColumnHeader({
  column,
  onExpand,
}: Readonly<{
  column: KanbanColumnVM;
  onExpand: (() => void) | undefined;
}>) {
  const { t } = useTranslation();
  return (
    <div className="grid h-full min-h-0 w-full grid-rows-[auto_minmax(0,1fr)] justify-items-center gap-[var(--space-2)]">
      <button
        aria-label={t("board.expandColumn", { name: column.name })}
        className="grid size-[28px] place-items-center rounded-full text-[var(--color-on-island)] opacity-60 outline-none transition-[background-color,box-shadow,opacity] duration-150 hover:bg-[var(--color-island-2)] hover:opacity-85 focus-visible:opacity-100 focus-visible:shadow-[0_0_0_3px_color-mix(in_srgb,var(--color-primary)_26%,transparent)]"
        onClick={onExpand}
        type="button"
      >
        <Maximize2 aria-hidden="true" size={16} strokeWidth={1.7} />
      </button>
      <div className="relative min-h-0 w-full overflow-hidden">
        <div className="board-column-collapsed-label flex items-center justify-start text-left">
          <h2 className="m-0 max-w-[180px] truncate text-[1rem] leading-none">{column.name}</h2>
        </div>
      </div>
    </div>
  );
}

type BoardVirtualCard = Readonly<{
  card: KanbanCardVM;
  instance: BoardCardInstance;
  key: BoardCardInstanceKey;
  virtualIndex: number;
}>;

function estimateBoardCardRowSize(): number {
  return 164;
}

function getBoardVirtualCardKey(item: BoardVirtualCard): string {
  return item.key;
}

function initialBoundaryContent(
  state: VirtualizedInfiniteListBoundaryState | undefined,
): ReactNode | undefined {
  return state === undefined ? undefined : <InfiniteListBoundary direction="initial" state={state} />;
}

function boardDropHint(enabled: boolean, label: string): ReactNode | undefined {
  return enabled ? (
    <p className="m-0 rounded-[var(--radius-m)] border border-dashed border-[var(--color-outline)] p-[var(--space-2)] text-sm text-[var(--color-muted)]">
      {label}
    </p>
  ) : undefined;
}

function readyBoundary(
  initial: VirtualizedInfiniteListBoundaryState | undefined,
  directional: VirtualizedInfiniteListBoundaryState | undefined,
): VirtualizedInfiniteListBoundaryState | undefined {
  return initial === undefined ? directional : undefined;
}

function autoLoadAvailable(
  available: boolean,
  boundary: VirtualizedInfiniteListBoundaryState | undefined,
): boolean {
  return available && boundary?.state !== "error";
}

const TaskCard = memo(function TaskCard({
  actionsDisabled,
  card,
  instance,
  onCardClick,
  onCardDragEnd,
  onCardDragStart,
  onDeleteTask,
  onInterruptTask,
  onResumeTask,
  virtualIndex,
}: Readonly<{
  card: KanbanCardVM;
  instance: BoardCardInstance;
  actionsDisabled: boolean;
  onCardClick: (taskID: string) => void;
  onCardDragEnd: () => void;
  onCardDragStart: (drag: ActiveBoardCardDrag) => void;
  onDeleteTask: (taskID: string) => void;
  onInterruptTask: (taskID: string) => void;
  onResumeTask: (taskID: string) => void;
  virtualIndex: number;
}>) {
  const { t } = useTranslation();
  const { cardClassName, cardStyle, registerCard: registerMotionCard } = useBoardCardMotion();
  const registerCard = useCallback(
    (element: HTMLElement | null) => {
      registerMotionCard(instance, element);
    },
    [instance, registerMotionCard],
  );
  const canDrag = !actionsDisabled && card.statusKind !== "canceled";
  const waitingForAnswer = isWaitingForAnswer(card.statusKind);
  const dragPayload = {
    taskID: card.id,
    canStart: card.actions.canStart,
    activeNodeIDs: card.activeNodeIDs,
    statusKind: card.statusKind,
    manualMoveTargetNodeIDs: card.actions.manualMoveTargetNodeIDs,
  };
  return (
    <ContextMenu>
      <ContextMenuTrigger asChild>
        <article
          aria-label={card.title}
          className={cx(
            "board-task-card grid cursor-pointer gap-[var(--space-2)] rounded-[var(--radius-l)] border border-[var(--color-outline)] bg-[var(--color-island-1)] p-[var(--space-3)] outline-none focus-visible:border-[var(--color-primary)] focus-visible:shadow-[0_0_0_3px_color-mix(in_srgb,var(--color-primary)_26%,transparent)]",
            cardClassName(card.id),
          )}
          data-task-card-border-tone={card.borderTone}
          data-task-card-state={waitingForAnswer ? "waiting-answer" : card.statusKind}
          data-testid="task-card"
          draggable={canDrag}
          onClick={() => {
            onCardClick(card.id);
          }}
          onDragEnd={onCardDragEnd}
          onDragStart={(event) => {
            if (!canDrag) {
              event.preventDefault();
              return;
            }
            event.dataTransfer.setData("text/task-id", card.id);
            event.dataTransfer.setData("text/plain", card.id);
            event.dataTransfer.setData(boardCardDragPayloadType, encodeBoardCardDragPayload(dragPayload));
            event.dataTransfer.effectAllowed = "move";
            setBoardCardDragImage(event.currentTarget, event.dataTransfer);
            onCardDragStart({
              instance,
              lastVirtualIndex: virtualIndex,
              payload: dragPayload,
              snapshot: card,
            });
          }}
          onKeyDown={(event) => {
            activateCardFromKeyboard(event, () => {
              onCardClick(card.id);
            });
          }}
          ref={registerCard}
          style={cardStyle(card.id)}
          tabIndex={0}
        >
          <span className="flex min-w-0 items-center justify-between gap-[var(--space-2)] text-left text-[var(--color-on-island)]">
            <span className="shrink-0 font-mono text-[0.78rem] text-[var(--color-muted)]">
              {card.shortID}
            </span>
            <span className="min-w-0 truncate text-right text-sm text-[var(--color-muted)]">
              {formatRelativeTime(card.updatedAt)}
            </span>
          </span>
          <strong
            className="task-card-title text-left text-[var(--color-on-island)]"
            data-testid="task-card-title"
          >
            {card.title}
          </strong>
          <span
            className="task-card-body-preview text-sm text-[var(--color-muted)]"
            data-testid="task-card-body"
          >
            <TaskCardPreview instance={instance} preview={card.preview} />
          </span>
          <div
            className="task-card-footer flex items-start justify-between gap-[var(--space-2)]"
            data-testid="task-card-footer"
          >
            <div
              className="task-card-chip-row flex min-w-0 flex-1 flex-wrap items-center gap-[var(--space-2)] text-sm text-[var(--color-muted)]"
              data-testid="task-card-chips"
            >
              {card.statusKind === "running" ? (
                <Spinner
                  className="h-[18px] w-[18px]"
                  strokeWidth={1.5}
                  testID="task-card-active-run-spinner"
                />
              ) : null}
              {card.workspaceChipLabel !== null ? (
                <span
                  className="task-card-chip-slot inline-flex items-center"
                  data-testid="task-card-chip-slot"
                >
                  <Badge tone="neutral">{card.workspaceChipLabel}</Badge>
                </span>
              ) : null}
            </div>
            <TaskCardActions
              actionsDisabled={actionsDisabled}
              card={card}
              onInterrupt={onInterruptTask}
              onResume={onResumeTask}
            />
          </div>
        </article>
      </ContextMenuTrigger>
      <ContextMenuContent>
        <ContextMenuItem
          className="text-[var(--color-error)]"
          disabled={actionsDisabled}
          onSelect={() => {
            onDeleteTask(card.id);
          }}
        >
          {t("board.deleteTask")}
        </ContextMenuItem>
      </ContextMenuContent>
    </ContextMenu>
  );
});

const TaskCardPreview = memo(function TaskCardPreview({
  instance,
  preview,
}: Readonly<{
  instance: BoardCardInstance;
  preview: KanbanCardVM["preview"];
}>) {
  const visible = useBoardCardInstanceVisibility(instance);
  if (!visible) {
    return null;
  }
  return (
    <>
      <MarkdownPlainText value={preview.markdown} />
      {preview.truncated ? (
        <span aria-hidden="true" data-testid="task-card-preview-ellipsis">
          …
        </span>
      ) : null}
    </>
  );
});

function setBoardCardDragImage(cardElement: HTMLElement, dataTransfer: DataTransfer): void {
  const rect = cardElement.getBoundingClientRect();
  const dragImage = cardElement.cloneNode(true);
  if (!(dragImage instanceof HTMLElement)) {
    return;
  }
  dragImage.classList.add("board-card-drag-image");
  dragImage.style.width = `${rect.width.toString()}px`;
  dragImage.style.height = `${rect.height.toString()}px`;
  document.body.append(dragImage);
  dataTransfer.setDragImage(dragImage, rect.width / 2, Math.min(24, rect.height / 2));
  window.setTimeout(() => {
    dragImage.remove();
  }, 0);
}

function hasBoardCardDragData(dataTransfer: DataTransfer): boolean {
  const types = Array.from(dataTransfer.types);
  return types.includes(boardCardDragPayloadType) || types.includes("text/task-id");
}

function activateCardFromKeyboard(event: KeyboardEvent<HTMLElement>, onClick: () => void): void {
  if (event.defaultPrevented) {
    return;
  }
  if (isInteractiveEventTarget(event.target)) {
    return;
  }
  if (event.key !== "Enter" && event.key !== " ") {
    return;
  }
  event.preventDefault();
  onClick();
}

function isInteractiveEventTarget(target: EventTarget): boolean {
  if (!(target instanceof Element)) {
    return false;
  }
  return target.closest("button,a,input,select,textarea,[role='button']") !== null;
}

function TaskCardActions({
  card,
  actionsDisabled,
  onInterrupt,
  onResume,
}: Readonly<{
  card: KanbanCardVM;
  actionsDisabled: boolean;
  onInterrupt: (taskID: string) => void;
  onResume: (taskID: string) => void;
}>) {
  const { t } = useTranslation();
  const canInterrupt = card.actions.canInterrupt && !isWaitingForAnswer(card.statusKind);
  if (!canInterrupt && !card.actions.canResume) {
    return null;
  }
  return (
    <div className="flex shrink-0 flex-wrap justify-end gap-[var(--space-2)]">
      {card.actions.canResume ? (
        <Button
          onClick={(event) => {
            event.stopPropagation();
            onResume(card.id);
          }}
          disabled={actionsDisabled}
          variant="primary"
        >
          {t("board.resume")}
        </Button>
      ) : null}
      {canInterrupt ? (
        <Button
          onClick={(event) => {
            event.stopPropagation();
            onInterrupt(card.id);
          }}
          disabled={actionsDisabled}
          variant="danger"
        >
          {t("board.interrupt")}
        </Button>
      ) : null}
    </div>
  );
}

function isWaitingForAnswer(statusKind: string): boolean {
  return statusKind === "waiting_question";
}
