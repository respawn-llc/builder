import type { BoardColumn, BoardGroup, WorkflowBoard } from "@/api";

export type BoardSection = Readonly<
  | { kind: "column"; id: string; column: BoardColumn }
  | { kind: "group"; id: string; group: BoardGroup; columns: readonly BoardColumn[] }
>;

export function boardSections(board: WorkflowBoard): readonly BoardSection[] {
  const columnsByID = new Map(board.columns.map((column) => [column.id, column]));
  const consumed = new Set<string>();
  const groupsByID = new Map(board.groups.map((group) => [group.id, group]));
  const backlog = board.columns.filter((column) => column.isBacklog);
  const done = board.columns.filter((column) => column.isDone);
  const sections: BoardSection[] = [];

  for (const column of backlog) {
    consumed.add(column.id);
    sections.push({ kind: "column", id: column.id, column });
  }

  for (const column of board.columns) {
    if (column.isBacklog || column.isDone || consumed.has(column.id)) {
      continue;
    }

    const group = groupsByID.get(column.groupID);
    if (group === undefined) {
      consumed.add(column.id);
      sections.push({ kind: "column", id: column.id, column });
      continue;
    }

    const columns = groupColumns(group, columnsByID, board.columns);
    for (const groupedColumn of columns) {
      consumed.add(groupedColumn.id);
    }
    if (columns.length > 0) {
      sections.push({ kind: "group", id: group.id, group, columns });
    }
  }

  for (const column of done) {
    consumed.add(column.id);
    sections.push({ kind: "column", id: column.id, column });
  }

  return sections;
}

function groupColumns(
  group: BoardGroup,
  columnsByID: ReadonlyMap<string, BoardColumn>,
  columns: readonly BoardColumn[],
): readonly BoardColumn[] {
  const groupedColumns = group.nodeIDs
    .map((nodeID) => columnsByID.get(nodeID))
    .filter((column): column is BoardColumn => isActiveGroupColumn(column, group.id));

  if (groupedColumns.length > 0) {
    return groupedColumns;
  }

  return columns.filter((column) => isActiveGroupColumn(column, group.id));
}

function isActiveGroupColumn(column: BoardColumn | undefined, groupID: string): column is BoardColumn {
  return column?.groupID === groupID && !column.isBacklog && !column.isDone;
}
