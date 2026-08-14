import type { TaskDependencyProgress, TaskStatus } from "./models";

export type ProjectLabel = Readonly<{
  id: string;
  name: string;
}>;

export type ProjectLabelCatalog = Readonly<{
  projectID: string;
  labels: readonly ProjectLabel[];
}>;

export type TaskLabelAssignment = Readonly<{
  taskID: string;
  labelIDs: readonly string[];
}>;

export type TaskLabelFilter =
  | Readonly<{ kind: "none" }>
  | Readonly<{ kind: "unlabeled" }>
  | Readonly<{
      kind: "named";
      mode: "any" | "all";
      labelIDs: readonly string[];
      excludedLabelIDs?: readonly string[];
    }>;

export type CanonicalTaskLabelFilter =
  | Readonly<{ kind: "none" }>
  | Readonly<{ kind: "unlabeled" }>
  | Readonly<{
      kind: "named";
      mode: "any" | "all";
      labelIDs: readonly string[];
      excludedLabelIDs: readonly string[];
    }>;

export const noTaskLabelFilter = { kind: "none" } as const satisfies TaskLabelFilter;

export function canonicalTaskLabelFilter(filter: TaskLabelFilter): CanonicalTaskLabelFilter {
  switch (filter.kind) {
    case "none":
    case "unlabeled":
      return filter;
    case "named":
      return {
        kind: "named",
        mode: filter.mode,
        labelIDs: [...filter.labelIDs].sort(),
        excludedLabelIDs: [...(filter.excludedLabelIDs ?? [])].sort(),
      };
  }
}

export function taskLabelFilterConditionCount(filter: TaskLabelFilter): number {
  const canonical = canonicalTaskLabelFilter(filter);
  return canonical.kind === "named" ? canonical.labelIDs.length + canonical.excludedLabelIDs.length : 0;
}

export function taskLabelFiltersEqual(left: TaskLabelFilter, right: TaskLabelFilter): boolean {
  const canonicalLeft = canonicalTaskLabelFilter(left);
  const canonicalRight = canonicalTaskLabelFilter(right);
  if (canonicalLeft.kind !== canonicalRight.kind) {
    return false;
  }
  if (canonicalLeft.kind !== "named" || canonicalRight.kind !== "named") {
    return true;
  }
  return (
    canonicalLeft.mode === canonicalRight.mode &&
    labelIDListsEqual(canonicalLeft.labelIDs, canonicalRight.labelIDs) &&
    labelIDListsEqual(canonicalLeft.excludedLabelIDs, canonicalRight.excludedLabelIDs)
  );
}

export function labelIDListsEqual(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((labelID, index) => labelID === right[index]);
}

export type TaskListItem = Readonly<{
  id: string;
  shortID: string;
  workflowID: string;
  workflowName: string | null;
  title: string;
  createdAt: number;
  updatedAt: number;
  columnKeys: readonly string[] | null;
  status: TaskStatus;
  labels: readonly ProjectLabel[];
  dependencyProgress: TaskDependencyProgress | null;
}>;

export type TaskListPage = Readonly<{
  scope: Readonly<{
    projectID: string;
    workflowID: string | null;
  }>;
  matchingWorkflowCardinality: "none" | "one" | "multiple";
  nextOffset: number | null;
  generatedAt: number;
  tasks: readonly TaskListItem[];
}>;

export type ProjectTaskGroup = "active" | "backlog" | "done";

export type ProjectTaskGroupCounts = Readonly<{
  projectID: string;
  counts: Readonly<{
    active: number;
    backlog: number;
    done: number;
  }>;
  generatedAt: number;
}>;
