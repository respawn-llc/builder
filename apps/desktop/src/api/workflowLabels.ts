import type { TaskStatus } from "./models";

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
    }>;

export const noTaskLabelFilter = { kind: "none" } as const satisfies TaskLabelFilter;

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
  runCount: number;
  labelIDs: readonly string[];
}>;

export type TaskListPage = Readonly<{
  scope: Readonly<{
    projectID: string;
    workflowID: string | null;
  }>;
  matchingWorkflowCardinality: "none" | "one" | "multiple";
  nextPageToken: string | null;
  generatedAt: number;
  tasks: readonly TaskListItem[];
}>;
