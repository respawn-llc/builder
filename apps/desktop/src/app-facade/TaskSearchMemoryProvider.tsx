import { useCallback, useMemo, useState, type ReactNode } from "react";

import {
  TaskSearchMemoryContext,
  type TaskSearchMemory,
  type TaskSearchMemorySelection,
} from "./taskSearchMemoryContext";
import type { TaskSearchScope } from "./taskSearchScope";

export function TaskSearchMemoryProvider({ children }: Readonly<{ children: ReactNode }>) {
  const [query, setQuery] = useState("");
  const [globalSelection, setGlobalSelection] = useState<TaskSearchMemorySelection | null>(null);
  const [projectSelections, setProjectSelections] =
    useState<ReadonlyMap<string, TaskSearchMemorySelection>>(() => new Map());
  const rememberSelection = useCallback(
    (nextSelection: TaskSearchMemorySelection): void => {
      if (nextSelection.scope.kind === "global") {
        setGlobalSelection((current) =>
          current?.key === nextSelection.key && current.query === nextSelection.query
            ? current
            : nextSelection,
        );
        return;
      }
      const projectID = nextSelection.scope.projectID;
      setProjectSelections((current) => {
        const previous = current.get(projectID);
        if (previous?.key === nextSelection.key && previous.query === nextSelection.query) {
          return current;
        }
        const next = new Map(current);
        next.set(projectID, nextSelection);
        return next;
      });
    },
    [],
  );
  const selectionFor = useCallback(
    (scope: TaskSearchScope): TaskSearchMemorySelection | null =>
      scope.kind === "global" ? globalSelection : (projectSelections.get(scope.projectID) ?? null),
    [globalSelection, projectSelections],
  );
  const memory = useMemo<TaskSearchMemory>(
    () => ({ query, rememberSelection, selectionFor, setQuery }),
    [query, rememberSelection, selectionFor, setQuery],
  );
  return <TaskSearchMemoryContext.Provider value={memory}>{children}</TaskSearchMemoryContext.Provider>;
}
