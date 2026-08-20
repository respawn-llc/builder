import { useCallback, useMemo, useState, type ReactNode } from "react";

import {
  TaskSearchMemoryContext,
  type TaskSearchMemory,
  type TaskSearchMemorySelection,
} from "./taskSearchMemoryContext";

export function TaskSearchMemoryProvider({ children }: Readonly<{ children: ReactNode }>) {
  const [query, setQuery] = useState("");
  const [selections, setSelections] = useState<ReadonlyMap<string | null, TaskSearchMemorySelection>>(
    () => new Map(),
  );
  const rememberSelection = useCallback(
    (nextSelection: TaskSearchMemorySelection): void => {
      setSelections((current) => {
        const previous = current.get(nextSelection.projectID);
        if (previous?.key === nextSelection.key && previous.query === nextSelection.query) {
          return current;
        }
        const next = new Map(current);
        next.set(nextSelection.projectID, nextSelection);
        return next;
      });
    },
    [setSelections],
  );
  const selectionFor = useCallback(
    (projectID: string | null): TaskSearchMemorySelection | null => selections.get(projectID) ?? null,
    [selections],
  );
  const memory = useMemo<TaskSearchMemory>(
    () => ({ query, rememberSelection, selectionFor, setQuery }),
    [query, rememberSelection, selectionFor, setQuery],
  );
  return <TaskSearchMemoryContext.Provider value={memory}>{children}</TaskSearchMemoryContext.Provider>;
}
