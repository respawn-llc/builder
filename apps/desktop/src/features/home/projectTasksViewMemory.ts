import type {
  ProjectTaskGroupAnchors,
  ProjectTaskGroupDisclosure,
} from "./projectTaskListData";

export interface ProjectTasksViewMemory {
  read(): Readonly<{
    anchors: ProjectTaskGroupAnchors;
    disclosure: ProjectTaskGroupDisclosure;
    horizontalOffsetPx: number;
    verticalOffsetPx: number;
    scrollRequestSequence: number;
  }>;
  setAnchors(anchors: ProjectTaskGroupAnchors): void;
  setDisclosure(disclosure: ProjectTaskGroupDisclosure): void;
  setScrollOffsets(verticalOffsetPx: number, horizontalOffsetPx: number): void;
}

export function createProjectTasksViewMemory(): ProjectTasksViewMemory {
  let value = {
    anchors: { active: 0, backlog: 0, done: 0 },
    disclosure: { active: true, backlog: true, done: false },
    horizontalOffsetPx: 0,
    verticalOffsetPx: 0,
    scrollRequestSequence: 0,
  };
  return {
    read: () => value,
    setAnchors(anchors) {
      value = { ...value, anchors };
    },
    setDisclosure(disclosure) {
      value = { ...value, disclosure };
    },
    setScrollOffsets(verticalOffsetPx, horizontalOffsetPx) {
      value = { ...value, horizontalOffsetPx, verticalOffsetPx };
    },
  };
}
