import type { BoardColumn, WorkflowBoard } from "../../api";
import { boardSections } from "./BoardModel";

describe("boardSections", () => {
  it("orders Backlog first, grouped nodes next, and Done last", () => {
    expect(boardSections(board).map((section) => section.id)).toEqual(["backlog", "group-1", "done"]);
  });

  it("places a group where its first member column appears in the workflow column order", () => {
    expect(boardSections(boardWithUngroupedImplementation).map((section) => section.id)).toEqual([
      "backlog",
      "implementation",
      "group-1",
      "done",
    ]);
  });
});

const backlogColumn: BoardColumn = {
  assigneeRole: "",
  groupID: "",
  id: "backlog",
  isBacklog: true,
  isDone: false,
  key: "backlog",
  kind: "start",
  name: "Backlog",
  outputFields: [],
  sortOrder: 0,
  taskCount: 1,
  transitionOutputFields: [],
};

const activeColumn: BoardColumn = {
  assigneeRole: "coder",
  groupID: "group-1",
  id: "node-1",
  isBacklog: false,
  isDone: false,
  key: "implement",
  kind: "agent",
  name: "Implement",
  outputFields: [],
  sortOrder: 1,
  taskCount: 1,
  transitionOutputFields: [],
};

const doneColumn: BoardColumn = {
  assigneeRole: "",
  groupID: "",
  id: "done",
  isBacklog: false,
  isDone: true,
  key: "done",
  kind: "terminal",
  name: "Done",
  outputFields: [],
  sortOrder: 2,
  taskCount: 0,
  transitionOutputFields: [],
};

const implementationColumn: BoardColumn = {
  ...activeColumn,
  groupID: "",
  id: "implementation",
  key: "implementation",
  name: "Implementation",
  sortOrder: 1,
};

const reviewColumn: BoardColumn = {
  ...activeColumn,
  id: "review",
  key: "review",
  name: "Review",
  sortOrder: 2,
};

const qaColumn: BoardColumn = {
  ...activeColumn,
  id: "qa",
  key: "qa",
  name: "QA",
  sortOrder: 3,
};

const board: WorkflowBoard = {
  columns: [doneColumn, activeColumn, backlogColumn],
  generatedAt: 1,
  groups: [{ id: "group-1", key: "core", name: "Core", nodeIDs: ["node-1"], sortOrder: 1 }],
  projectID: "project-1",
  projectKey: "PROJ",
  projectName: "Project",
  defaultWorkspaceID: "workspace-1",
  attachedWorkspaceCount: 1,
  selectedWorkflow: {
    description: "",
    version: 1,
    id: "workflow-1",
    isProjectDefault: true,
    name: "Workflow",
    validForTaskCreation: true,
    validationErrors: [],
  },
  workflows: [],
};

const boardWithUngroupedImplementation: WorkflowBoard = {
  ...board,
  columns: [backlogColumn, implementationColumn, reviewColumn, qaColumn, doneColumn],
  groups: [{ id: "group-1", key: "core", name: "Core", nodeIDs: ["review", "qa"], sortOrder: 1 }],
};
