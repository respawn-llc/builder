import { sidebarPopOutOptions } from "./sidebarPopOut";

describe("sidebar pop-out options", () => {
  it("keeps Task Detail pop-out options scoped to the current Task", () => {
    expect(
      sidebarPopOutOptions(
        {
          kind: "taskDetail",
          projectID: "project-1",
          taskID: "task-1",
        },
        "Task 1",
      ),
    ).toMatchObject({
      params: { taskID: "task-1" },
      title: "Task 1",
    });
  });
});
