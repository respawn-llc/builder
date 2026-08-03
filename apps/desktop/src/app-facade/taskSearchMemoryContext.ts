import { createContext, useContext } from "react";

export type TaskSearchMemorySelection = Readonly<{
  key: string;
  projectID: string;
  query: string;
}>;

export type TaskSearchMemory = Readonly<{
  query: string;
  rememberSelection(selection: TaskSearchMemorySelection): void;
  selectionFor(projectID: string): TaskSearchMemorySelection | null;
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
