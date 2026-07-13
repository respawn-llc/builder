import type { BoardColumn, SelectedWorkflowBoard } from "../../api";
import type { KanbanCardVM } from "./BoardColumnViewModel";
import type { BoardSection } from "./BoardModel";

export type BoardCardColumnsSnapshot = ReadonlyMap<string, readonly KanbanCardVM[]>;
export type BoardCardColumnCountSnapshot = ReadonlyMap<string, number>;

export type BoardCardMotionParticipants = Readonly<{
  namesByCardID: ReadonlyMap<string, string>;
  revealCardIDs: ReadonlySet<string>;
}>;

export type PendingBoardCardMove = Readonly<{
  targetColumnID: string;
  taskID: string;
}>;

export function boardCardViewTransitionName(taskID: string): string {
  const encoded = Array.from(taskID, (char) => {
    const codePoint = char.codePointAt(0);
    return codePoint === undefined ? "" : codePoint.toString(16).padStart(2, "0");
  }).join("-");
  return `board-card-${encoded.length > 0 ? encoded : "empty"}`;
}

export function boardCardSnapshotsEqual(
  left: BoardCardColumnsSnapshot,
  right: BoardCardColumnsSnapshot,
): boolean {
  if (left.size !== right.size) {
    return false;
  }
  for (const [columnID, leftCards] of left) {
    const rightCards = right.get(columnID);
    if (rightCards === undefined || !cardListsEqual(leftCards, rightCards)) {
      return false;
    }
  }
  return true;
}

export function boardCardSnapshotFromEntries(
  entries: Iterable<readonly [string, readonly KanbanCardVM[]]>,
): BoardCardColumnsSnapshot {
  return new Map(Array.from(entries, ([columnID, cards]) => [columnID, cards]));
}

export function boardCardMotionParticipants(
  oldSnapshot: BoardCardColumnsSnapshot,
  newSnapshot: BoardCardColumnsSnapshot,
  visibleOldCardIDs: ReadonlySet<string>,
): BoardCardMotionParticipants {
  const oldPositions = cardPositions(oldSnapshot);
  const newPositions = cardPositions(newSnapshot);
  const namesByCardID = new Map<string, string>();
  const revealCardIDs = new Set<string>();
  const cardIDs = new Set([...oldPositions.keys(), ...newPositions.keys()]);

  for (const cardID of cardIDs) {
    const oldPosition = uniqueCardPosition(oldPositions.get(cardID) ?? []);
    const newPosition = uniqueCardPosition(newPositions.get(cardID) ?? []);
    if (oldPosition.kind === "duplicate" || newPosition.kind === "duplicate") {
      continue;
    }
    if (cardMoved(oldPosition, newPosition)) {
      namesByCardID.set(cardID, boardCardViewTransitionName(cardID));
      continue;
    }
    if (cardExited(oldPosition, newPosition) && visibleOldCardIDs.has(cardID)) {
      namesByCardID.set(cardID, boardCardViewTransitionName(cardID));
      continue;
    }
    if (cardEntered(oldPosition, newPosition)) {
      revealCardIDs.add(cardID);
    }
  }

  return { namesByCardID, revealCardIDs };
}

export function dirtyBoardCardColumnIDs(
  currentDisplayed: BoardCardColumnsSnapshot,
  nextDisplayed: BoardCardColumnsSnapshot,
): readonly string[] {
  const columnIDs = new Set([...currentDisplayed.keys(), ...nextDisplayed.keys()]);
  return Array.from(columnIDs).filter((columnID) => {
    const currentCards = currentDisplayed.get(columnID) ?? [];
    const nextCards = nextDisplayed.get(columnID) ?? [];
    return !boardCardSnapshotsEqual(new Map([[columnID, currentCards]]), new Map([[columnID, nextCards]]));
  });
}

export function dirtyBoardCardCountColumnIDs(
  currentCounts: BoardCardColumnCountSnapshot,
  nextCounts: BoardCardColumnCountSnapshot,
): readonly string[] {
  const columnIDs = new Set([...currentCounts.keys(), ...nextCounts.keys()]);
  return Array.from(columnIDs).filter(
    (columnID) => (currentCounts.get(columnID) ?? 0) !== (nextCounts.get(columnID) ?? 0),
  );
}

export function boardCardColumnCountSnapshot(
  board: Readonly<{ columns: readonly Pick<BoardColumn, "id" | "taskCount">[] }>,
): BoardCardColumnCountSnapshot {
  return new Map(board.columns.map((column) => [column.id, column.taskCount]));
}

export function boardCardColumnIDsWithCards(snapshot: BoardCardColumnsSnapshot): ReadonlySet<string> {
  return new Set(
    Array.from(snapshot, ([columnID, cards]) => (cards.length > 0 ? columnID : "")).filter(Boolean),
  );
}

export function boardRailLayoutSignature(
  board: SelectedWorkflowBoard,
  sections: readonly BoardSection[],
  firstActiveID: string | undefined,
): string {
  const sectionSignature = sections
    .map((section) =>
      section.kind === "group"
        ? `group:${section.group.id}:${section.columns.map((column) => column.id).join(",")}`
        : `column:${section.column.id}`,
    )
    .join("|");
  return `${board.projectID}:${board.selectedWorkflow.id}:${firstActiveID ?? ""}:${sectionSignature}`;
}

export function cardBelongsToColumn(column: BoardColumn, card: KanbanCardVM): boolean {
  if (column.isBacklog) {
    return card.statusKind === "backlog";
  }
  if (column.isDone) {
    return (
      card.statusKind === "done" || card.statusKind === "canceled" || card.activeNodeIDs.includes(column.id)
    );
  }
  return card.activeNodeIDs.includes(column.id);
}

function cardListsEqual(left: readonly KanbanCardVM[], right: readonly KanbanCardVM[]): boolean {
  if (left.length !== right.length) {
    return false;
  }
  return left.every((card, index) => cardsEqual(card, right[index]));
}

function cardsEqual(left: KanbanCardVM, right: KanbanCardVM | undefined): boolean {
  return right?.id === left.id && cardContentEqual(left, right) && cardActionsEqual(left, right);
}

function cardContentEqual(left: KanbanCardVM, right: KanbanCardVM): boolean {
  return (
    left.shortID === right.shortID &&
    left.title === right.title &&
    left.preview.markdown === right.preview.markdown &&
    left.preview.truncated === right.preview.truncated &&
    left.updatedAt === right.updatedAt &&
    left.statusKind === right.statusKind &&
    arrayEqual(left.statusRunIDs, right.statusRunIDs) &&
    left.workspaceChipLabel === right.workspaceChipLabel &&
    left.borderTone === right.borderTone &&
    arrayEqual(left.activeNodeIDs, right.activeNodeIDs)
  );
}

function cardActionsEqual(left: KanbanCardVM, right: KanbanCardVM): boolean {
  return (
    left.actions.canInterrupt === right.actions.canInterrupt &&
    left.actions.canResume === right.actions.canResume &&
    left.actions.canStart === right.actions.canStart &&
    arrayEqual(left.actions.manualMoveTargetNodeIDs, right.actions.manualMoveTargetNodeIDs)
  );
}

function arrayEqual(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

type CardPosition = Readonly<{
  columnID: string;
  index: number;
}>;

type UniqueCardPosition =
  | Readonly<{ kind: "absent" }>
  | Readonly<{ kind: "duplicate" }>
  | Readonly<{ kind: "present"; position: CardPosition }>;

function cardPositions(snapshot: BoardCardColumnsSnapshot): ReadonlyMap<string, readonly CardPosition[]> {
  const positions = new Map<string, CardPosition[]>();
  for (const [columnID, cards] of snapshot) {
    cards.forEach((card, index) => {
      const existing = positions.get(card.id) ?? [];
      positions.set(card.id, [...existing, { columnID, index }]);
    });
  }
  return positions;
}

function cardPositionEqual(left: CardPosition, right: CardPosition): boolean {
  return left.columnID === right.columnID && left.index === right.index;
}

function uniqueCardPosition(positions: readonly CardPosition[]): UniqueCardPosition {
  const position = positions[0];
  if (position === undefined) {
    return { kind: "absent" };
  }
  if (positions.length > 1) {
    return { kind: "duplicate" };
  }
  return { kind: "present", position };
}

function cardMoved(oldPosition: UniqueCardPosition, newPosition: UniqueCardPosition): boolean {
  return (
    oldPosition.kind === "present" &&
    newPosition.kind === "present" &&
    !cardPositionEqual(oldPosition.position, newPosition.position)
  );
}

function cardExited(oldPosition: UniqueCardPosition, newPosition: UniqueCardPosition): boolean {
  return oldPosition.kind === "present" && newPosition.kind === "absent";
}

function cardEntered(oldPosition: UniqueCardPosition, newPosition: UniqueCardPosition): boolean {
  return oldPosition.kind === "absent" && newPosition.kind === "present";
}
