import { createContext, useContext } from "react";

import type { TaskSearchScope } from "./taskSearchScope";

export type TaskSearchMemorySelection = Readonly<{
  key: string;
  scope: TaskSearchScope;
  query: string;
}>;

export type TaskSearchMemory = Readonly<{
  query: string;
  rememberSelection(selection: TaskSearchMemorySelection): void;
  selectionFor(scope: TaskSearchScope): TaskSearchMemorySelection | null;
  setQuery(query: string): void;
}>;

export const TaskSearchMemoryContext = createContext<TaskSearchMemory | null>(null);

export function useTaskSearchMemory(): TaskSearchMemory {
  const memory = useContext(TaskSearchMemoryContext);
  if (memory === null) {
    throw new Error("Task Search memory requires TaskSearchMemoryProvider.");
  }
  return memory;
}
