import { isUUIDv4 } from "./setupOperationID";

export class WorktreeOperationID {
  readonly #value: string;

  private constructor(value: string) {
    this.#value = value;
  }

  static parse(value: string): WorktreeOperationID {
    if (!isUUIDv4(value)) {
      throw new Error("Worktree operation id must be a UUID v4.");
    }
    return new WorktreeOperationID(value);
  }

  toJSONValue(): string {
    return this.#value;
  }
}

export function parseWorktreeOperationID(value: string): WorktreeOperationID {
  return WorktreeOperationID.parse(value);
}
