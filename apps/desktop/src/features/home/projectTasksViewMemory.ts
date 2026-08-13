import type { ProjectTaskGroupDisclosure } from "./projectTaskListData";

export interface ProjectTasksViewMemory {
  read(): Readonly<{
    disclosure: ProjectTaskGroupDisclosure;
    horizontalOffsetPx: number;
    verticalOffsetPx: number;
    scrollRequestSequence: number;
  }>;
  setDisclosure(disclosure: ProjectTaskGroupDisclosure): void;
  setScrollOffsets(verticalOffsetPx: number, horizontalOffsetPx: number): void;
}

export function createProjectTasksViewMemory(): ProjectTasksViewMemory {
  let value = {
    disclosure: { active: true, backlog: true, done: false },
    horizontalOffsetPx: 0,
    verticalOffsetPx: 0,
    scrollRequestSequence: 0,
  };
  return {
    read: () => value,
    setDisclosure(disclosure) {
      value = { ...value, disclosure };
    },
    setScrollOffsets(verticalOffsetPx, horizontalOffsetPx) {
      value = { ...value, horizontalOffsetPx, verticalOffsetPx };
    },
  };
}
