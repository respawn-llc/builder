import { taskDetailRouteShouldClose } from "./taskDetailRouteLifecycle";

describe("Task Detail route lifecycle", () => {
  it("closes after the sidebar finishes or the operator closes it", () => {
    expect(taskDetailRouteShouldClose({ destination: "newTask", status: "submitted" })).toBe(true);
    expect(taskDetailRouteShouldClose({ reason: "closed", status: "canceled" })).toBe(true);
  });

  it("stays open while the sidebar lifecycle is replaced", () => {
    expect(taskDetailRouteShouldClose({ reason: "replaced", status: "canceled" })).toBe(false);
  });
});
