import { sidebarPopOutOptions, taskDetailNativeDialogPath } from "./sidebarPopOut";
import type { SidebarDestination } from "./sidebarContext";

describe("sidebarPopOutOptions", () => {
  it("maps a task detail destination to its native window route and params", () => {
    const destination: SidebarDestination = {
      kind: "taskDetail",
      mode: "overlay",
      taskID: "task-123",
    };
    const options = sidebarPopOutOptions(destination, "Task title");
    expect(options).not.toBeNull();
    expect(options?.route).toBe(taskDetailNativeDialogPath);
    expect(options?.params).toEqual({ taskID: "task-123" });
    expect(options?.title).toBe("Task title");
    expect(options?.resizable).toBe(true);
  });

  it("returns null for destinations without a pop-out surface", () => {
    const destination: SidebarDestination = {
      kind: "projectEdit",
      projectID: "project-1",
    };
    expect(sidebarPopOutOptions(destination, "Edit project")).toBeNull();
  });
});
