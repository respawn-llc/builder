import type { SidebarDestination } from "@/app-facade";
import { taskDetailSidebarDestination } from "./taskDetailSidebarDestination";

it("preserves host fields only while replacing the same Task Detail", () => {
  const onMutated = vi.fn();
  const current: Extract<SidebarDestination, { kind: "taskDetail" }> = {
    kind: "taskDetail",
    inboxNav: true,
    mode: "overlay",
    onMutated,
    taskID: "task-1",
  };

  expect(
    taskDetailSidebarDestination(current, "task-1", {
      kind: "dependencies",
    }),
  ).toEqual({
    kind: "taskDetail",
    inboxNav: true,
    initialFocus: { kind: "dependencies" },
    mode: "overlay",
    onMutated,
    taskID: "task-1",
  });
  expect(taskDetailSidebarDestination(current, "task-2")).toEqual({
    kind: "taskDetail",
    mode: "overlay",
    taskID: "task-2",
  });
});
