import type { SidebarDestination } from "@/app-facade";
import {
  deactivateSidebarDestination,
  sameSidebarDestination,
  sidebarDestinationMatches,
  sidebarDestinationProjectID,
  taskDetailSidebarDestination,
} from "./sidebarDestinationAdapter";

const task = (taskID: string, projectID = "project-1"): SidebarDestination =>
  taskDetailSidebarDestination(taskID, projectID);

describe("sidebar destination adapter", () => {
  it("uses typed Task and Project identity without string-derived keys", () => {
    expect(sameSidebarDestination(task("task-1"), task("task-1"))).toBe(true);
    expect(sameSidebarDestination(task("task-1"), task("task-1", "project-2"))).toBe(false);
    expect(sameSidebarDestination(task("task-1"), { kind: "newTask", projectID: "project-1", workflowID: "workflow-1", boardQueryWorkflowID: undefined })).toBe(false);
  });

  it("reports Project membership only from typed destination data", () => {
    expect(sidebarDestinationProjectID(task("task-1", "project-2"))).toBe("project-2");
    expect(
      sidebarDestinationProjectID({
        kind: "workflowCreate",
        projectID: undefined,
      }),
    ).toBeNull();
    expect(
      sidebarDestinationProjectID({
        kind: "workflowEditor",
        workflowID: "workflow-1",
        projectID: "project-2",
      }),
    ).toBe("project-2");
  });

  it("matches typed Task and Project invalidation targets", () => {
    expect(sidebarDestinationMatches(task("task-1"), { kind: "task", taskID: "task-1" })).toBe(true);
    expect(sidebarDestinationMatches(task("task-1"), { kind: "task", taskID: "task-2" })).toBe(false);
    expect(sidebarDestinationMatches(task("task-1", "project-2"), { kind: "project", projectID: "project-2" })).toBe(true);
  });

  it("removes initial focus only after activation", () => {
    const focused = taskDetailSidebarDestination("task-1", "project-1", {
      initialFocus: { kind: "dependencies" },
    });
    expect(deactivateSidebarDestination(focused)).toEqual(task("task-1"));
    expect(deactivateSidebarDestination(task("task-1"))).toEqual(task("task-1"));
  });
});
