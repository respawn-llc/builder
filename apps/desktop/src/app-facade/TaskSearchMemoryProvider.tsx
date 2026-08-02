import { useCallback, useMemo, useState, type ReactNode } from "react";

import {
  TaskSearchMemoryContext,
  type TaskSearchMemory,
  type TaskSearchMemorySelection,
} from "./taskSearchMemoryContext";

export function TaskSearchMemoryProvider({ children }: Readonly<{ children: ReactNode }>) {
  const [query, setQuery] = useState("");
  const [selection, setSelection] = useState<TaskSearchMemorySelection | null>(null);
  const rememberSelection = useCallback(
    (nextSelection: TaskSearchMemorySelection): void => {
      setSelection(nextSelection);
    },
    [setSelection],
  );
  const selectionFor = useCallback(
    (projectID: string): TaskSearchMemorySelection | null =>
      selection?.projectID === projectID ? selection : null,
    [selection],
  );
  const memory = useMemo<TaskSearchMemory>(
    () => ({ query, rememberSelection, selectionFor, setQuery }),
    [query, rememberSelection, selectionFor, setQuery],
  );
  return <TaskSearchMemoryContext.Provider value={memory}>{children}</TaskSearchMemoryContext.Provider>;
}
