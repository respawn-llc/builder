export type WorkflowTaskListSortField =
  "created" | "updated" | "status" | "column" | "title" | "labels" | "short_id";

export type WorkflowTaskListSortDirection = "asc" | "desc";

export type WorkflowTaskListSort = Readonly<{
  field: WorkflowTaskListSortField;
  direction: WorkflowTaskListSortDirection;
}>;

export type BoardNodeCardsSort = Readonly<{
  field: Exclude<WorkflowTaskListSortField, "status" | "column" | "title">;
  direction: WorkflowTaskListSortDirection;
}>;

export const defaultBoardNodeCardsSort = {
  field: "updated",
  direction: "desc",
} as const satisfies BoardNodeCardsSort;

export const boardNodeCardsPageSize = 25;
