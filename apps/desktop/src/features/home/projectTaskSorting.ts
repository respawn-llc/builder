import type { WorkflowTaskListSort, WorkflowTaskListSortField } from "@/api";

export type ProjectTaskSortField = Exclude<WorkflowTaskListSortField, "column">;
export type ProjectTaskSort = Readonly<{
  field: ProjectTaskSortField;
  direction: WorkflowTaskListSort["direction"];
}>;

export function projectTaskSortsEqual(left: ProjectTaskSort | null, right: ProjectTaskSort): boolean {
  return left !== null && left.field === right.field && left.direction === right.direction;
}

export const defaultProjectTaskSort = {
  direction: "desc",
  field: "updated",
} as const satisfies ProjectTaskSort;

export const projectTaskSortFieldOptions = [
  { value: "updated", labelKey: "board.sort.fields.updated" },
  { value: "created", labelKey: "board.sort.fields.created" },
  { value: "status", labelKey: "board.sort.fields.status" },
  { value: "title", labelKey: "board.sort.fields.title" },
  { value: "labels", labelKey: "board.sort.fields.labels" },
  { value: "short_id", labelKey: "board.sort.fields.short_id" },
] as const satisfies readonly Readonly<{
  labelKey: string;
  value: ProjectTaskSortField;
}>[];
