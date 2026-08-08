import type { TaskStatus, TaskStatusKind } from "./models";

export type TaskSearchMode = "literal" | "fts5";

export type TaskSearchInput = Readonly<{
  mode: TaskSearchMode;
  query: string;
  context: number;
  caseSensitive: boolean;
  includeComments: boolean;
  projectIDs?: readonly string[] | undefined;
  statusKinds?: readonly TaskStatusKind[] | undefined;
  pageSize: number;
  offset?: number | undefined;
}>;

export type TaskSearchSource =
  | Readonly<{ kind: "short_id" | "title" | "body" }>
  | Readonly<{ kind: "comment"; commentID: string }>;

export type TaskSearchLiteralMatch = Readonly<{
  before: string;
  match: string;
  after: string;
  leftTruncated: boolean;
  rightTruncated: boolean;
}>;

export type TaskSearchLiteralHit = Readonly<{
  ordinal: number;
  source: TaskSearchSource;
  literal: TaskSearchLiteralMatch;
}>;

export type TaskSearchFTS5Hit = Readonly<{
  ordinal: number;
  source: TaskSearchSource;
  fts5: Readonly<{ snippet: string }>;
}>;

export type TaskSearchHit = TaskSearchLiteralHit | TaskSearchFTS5Hit;

export type TaskSearchGroup = Readonly<{
  projectID: string;
  projectKey: string;
  taskID: string;
  shortID: string;
  workflowID: string;
  title: string;
  status: TaskStatus;
  totalHitCount: number;
  hits: readonly TaskSearchHit[];
}>;

export type TaskSearchResponse = Readonly<{
  mode: TaskSearchMode;
  groups: readonly TaskSearchGroup[];
  nextOffset: number | null;
}>;
