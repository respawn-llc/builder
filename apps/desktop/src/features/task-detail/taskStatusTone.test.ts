import type { TaskStatus } from "../../api";
import { appI18n, initializeI18n } from "../../i18n/setup";
import { taskStatusTone } from "./taskStatusTone";

describe("taskStatusTone", () => {
  beforeAll(async () => {
    await initializeI18n();
  });

  it("maps every typed task status to localized text and a semantic badge tone", () => {
    const cases: readonly [TaskStatus["kind"], ReturnType<typeof taskStatusTone>][] = [
      ["canceled", "danger"],
      ["done", "success"],
      ["waiting_question", "warning"],
      ["waiting_approval", "warning"],
      ["interrupted", "danger"],
      ["running", "info"],
      ["queued", "info"],
      ["backlog", "neutral"],
      ["active", "neutral"],
    ];
    for (const [kind, tone] of cases) {
      const status = taskStatus({ kind, nativeState: kind });
      expect(appI18n.t(`task.statusKinds.${kind}`)).not.toBe(`task.statusKinds.${kind}`);
      expect(taskStatusTone(status)).toBe(tone);
    }
    expect(taskStatusTone(taskStatus({ attentionTypes: ["question"], kind: "active", nativeState: "active" }))).toBe(
      "warning",
    );
  });
});

function taskStatus(overrides: Partial<TaskStatus>): TaskStatus {
  return {
    attentionTypes: [],
    kind: "backlog",
    nativeState: "backlog",
    nodeIDs: [],
    runIDs: [],
    ...overrides,
  };
}
