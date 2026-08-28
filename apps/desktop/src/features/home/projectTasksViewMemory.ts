import type { ProjectTaskGroupDisclosure } from "./projectTaskListData";
import { defaultProjectTaskSort, type ProjectTaskSort } from "./projectTaskSorting";

export interface ProjectTasksViewMemory {
  read(): Readonly<{
    disclosure: ProjectTaskGroupDisclosure;
    horizontalOffsetPx: number;
    sort: ProjectTaskSort;
    verticalOffsetPx: number;
  }>;
  setDisclosure(disclosure: ProjectTaskGroupDisclosure): void;
  setScrollOffsets(verticalOffsetPx: number, horizontalOffsetPx: number): void;
  setSort(sort: ProjectTaskSort): void;
}

type ProjectTasksViewState = Readonly<{
  disclosure: ProjectTaskGroupDisclosure;
  horizontalOffsetPx: number;
  sort: ProjectTaskSort;
  verticalOffsetPx: number;
}>;

export function createProjectTasksViewMemory(): ProjectTasksViewMemory {
  let value: ProjectTasksViewState = {
    disclosure: { active: true, backlog: false, done: false },
    horizontalOffsetPx: 0,
    sort: defaultProjectTaskSort,
    verticalOffsetPx: 0,
  };
  return {
    read: () => value,
    setDisclosure(disclosure) {
      value = { ...value, disclosure };
    },
    setScrollOffsets(verticalOffsetPx, horizontalOffsetPx) {
      value = { ...value, horizontalOffsetPx, verticalOffsetPx };
    },
    setSort(sort) {
      value = { ...value, sort };
    },
  };
}
