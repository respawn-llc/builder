import { sidebarSizePreference } from "./sidebarDestinationSizing";
import type { SidebarDestination } from "./sidebarContext";

describe("sidebar destination sizing", () => {
  it("uses destination-specific desired widths", () => {
    expect(sidebarSizePreference(taskDetailDestination()).desiredWidthPx).toBe(650);
    expect(sidebarSizePreference(workflowInspectDestination()).desiredWidthPx).toBe(550);
    expect(sidebarSizePreference(workflowEditorDestination()).desiredWidthPx).toBe(550);
    expect(sidebarSizePreference(projectEditDestination()).desiredWidthPx).toBe(500);
    expect(sidebarSizePreference(workflowCreateDestination()).desiredWidthPx).toBe(500);
    expect(sidebarSizePreference(linkWorkflowDestination()).desiredWidthPx).toBe(500);
    expect(sidebarSizePreference(newTaskDestination()).desiredWidthPx).toBe(550);
  });
});

function taskDetailDestination(): SidebarDestination {
  return {
    kind: "taskDetail",
    taskID: "task-1",
  };
}

function workflowInspectDestination(): SidebarDestination {
  return {
    kind: "workflowInspect",
    selection: { kind: "workflow" },
    workflowID: "workflow-1",
  };
}

function workflowEditorDestination(): SidebarDestination {
  return {
    kind: "workflowEditor",
    workflowID: "workflow-1",
  };
}

function projectEditDestination(): SidebarDestination {
  return {
    kind: "projectEdit",
    projectID: "project-1",
  };
}

function workflowCreateDestination(): SidebarDestination {
  return {
    kind: "workflowCreate",
  };
}

function linkWorkflowDestination(): SidebarDestination {
  return {
    kind: "linkWorkflow",
    projectID: "project-1",
  };
}

function newTaskDestination(): SidebarDestination {
  return {
    boardQueryWorkflowID: "workflow-1",
    kind: "newTask",
    projectID: "project-1",
    workflowID: "workflow-1",
  };
}
