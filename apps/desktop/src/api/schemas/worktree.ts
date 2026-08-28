import type {
  CreateTargetResolution,
  DeletePreviewSuccess,
  ListEntry,
  SwitchOperation,
} from "@app/server-api-contract/gen/kent/api/worktree/worktree_pb";

import type { SetupOperationID } from "../setupOperationID";

type DeepReadonly<Value> = Value extends (...args: never[]) => unknown
  ? Value
  : Value extends readonly (infer Item)[]
    ? readonly DeepReadonly<Item>[]
    : Value extends object
      ? { readonly [Key in keyof Value]: DeepReadonly<Value[Key]> }
      : Value;

type AuthorityKind = "switch" | "delete" | "create";
const authorities = new WeakMap<object, AuthorityKind>();

export type WorktreeSwitch = DeepReadonly<SwitchOperation>;
export type WorktreeDeletePreview = DeepReadonly<DeletePreviewSuccess>;
export type WorktreeCreateTargetResolution = DeepReadonly<CreateTargetResolution>;
export type WorktreeDeleteConfirmationChoice = "confirm" | "confirm_and_branch";
export type WorktreeCreateInput = Readonly<{
  sessionID: string;
  setupOperationID: SetupOperationID;
  resolution: WorktreeCreateTargetResolution;
  baseRef: string | null;
}>;

export function authorizeWorktreeListEntry(entry: ListEntry): void {
  const operation = entry.projection?.switch;
  if (operation !== undefined) authorize("switch", operation);
}

export function authorizeWorktreeDeletePreview(value: DeletePreviewSuccess): WorktreeDeletePreview {
  authorize("delete", value);
  return value;
}

export function authorizeWorktreeCreateTargetResolution(
  value: CreateTargetResolution,
): WorktreeCreateTargetResolution {
  authorize("create", value);
  return value;
}

export function requireWorktreeAuthority(value: WorktreeSwitch, authority: "switch"): WorktreeSwitch;
export function requireWorktreeAuthority(
  value: WorktreeDeletePreview,
  authority: "delete",
): WorktreeDeletePreview;
export function requireWorktreeAuthority(
  value: WorktreeCreateTargetResolution,
  authority: "create",
): WorktreeCreateTargetResolution;
export function requireWorktreeAuthority(value: object, authority: AuthorityKind): object {
  if (authorities.get(value) !== authority) {
    throw new TypeError(`Worktree ${authority} requires decoded authority.`);
  }
  return value;
}

function authorize(kind: AuthorityKind, value: object): void {
  deepFreeze(value);
  authorities.set(value, kind);
}

function deepFreeze<Value extends object>(value: Value, visited = new WeakSet()): Value {
  if (visited.has(value)) return value;
  visited.add(value);
  for (const fact of Object.values(value)) {
    if (fact instanceof Object) deepFreeze(fact, visited);
  }
  return Object.freeze(value);
}
