import { shouldSkipNativeDialogStartupGate } from "./routes";

describe("native dialog startup gate policy", () => {
  it("only skips startup readiness for event-only native dialogs", () => {
    expect(shouldSkipNativeDialogStartupGate("/native-dialog/workspace-unlink")).toBe(true);
    expect(shouldSkipNativeDialogStartupGate("/native-dialog/project-delete")).toBe(false);
    expect(shouldSkipNativeDialogStartupGate("/native-dialog/project-create")).toBe(false);
    expect(shouldSkipNativeDialogStartupGate("/native-dialog/new-task")).toBe(false);
  });
});
