export type TaskSearchScope =
  | Readonly<{ kind: "global" }>
  | Readonly<{ kind: "project"; projectID: string }>;

export function taskSearchScopesEqual(left: TaskSearchScope, right: TaskSearchScope): boolean {
  return left.kind === "global"
    ? right.kind === "global"
    : right.kind === "project" && left.projectID === right.projectID;
}
