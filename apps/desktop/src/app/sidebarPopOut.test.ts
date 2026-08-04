import type { SidebarEntryToken } from "@/app-facade";
import { sidebarPopOutOptions, shouldCloseSidebarAfterPopOut } from "./sidebarPopOut";

const token = (lifecycleID: string, entryID: string): SidebarEntryToken => ({
  lifecycleID,
  entryID,
});

describe("sidebar pop-out completion", () => {
  it("closes only when the captured entry is still current", () => {
    const opened = token("lifecycle-1", "entry-1");

    expect(shouldCloseSidebarAfterPopOut(opened, opened)).toBe(true);
    expect(shouldCloseSidebarAfterPopOut(opened, token("lifecycle-1", "entry-2"))).toBe(false);
    expect(shouldCloseSidebarAfterPopOut(opened, token("lifecycle-2", "entry-3"))).toBe(false);
    expect(shouldCloseSidebarAfterPopOut(opened, null)).toBe(false);
  });

  it("keeps Task Detail pop-out options scoped to the current Task", () => {
    expect(
      sidebarPopOutOptions(
        {
          kind: "taskDetail",
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
