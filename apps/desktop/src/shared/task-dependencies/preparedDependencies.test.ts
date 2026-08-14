import type { TaskStatus } from "@/api";
import {
  insertPreparedTaskDependency,
  preparedTaskDependenciesProjection,
  type PreparedTaskDependency,
} from "./preparedDependencies";

const backlog: TaskStatus = {
  kind: "backlog",
  nativeState: "active",
  nodeIDs: [],
  attentionTypes: [],
};
const done: TaskStatus = {
  kind: "done",
  nativeState: "terminal",
  nodeIDs: [],
  attentionTypes: [],
};

describe("prepared Task Dependencies", () => {
  it("derives counts, preview progress, order, and remaining capacity", () => {
    const prepared: readonly PreparedTaskDependency[] = [
      entry("blocked-by", "task-12", "KENT-12", done),
      entry("blocked-by", "task-3", "KENT-3", backlog),
      entry("blocks", "task-20", "KENT-20", done),
      entry("blocks", "task-4", "KENT-4", backlog),
      entry("blocked-by", "task-2", "KENT-2", backlog),
      entry("blocks", "task-10", "KENT-10", backlog),
      entry("blocked-by", "task-1", "KENT-1", done),
    ];

    expect(preparedTaskDependenciesProjection(prepared)).toEqual({
      blockerCount: 4,
      unsatisfiedBlockerCount: 2,
      directlyBlockedTaskCount: 3,
      directions: [
        {
          direction: "blocked-by",
          totalCount: 4,
          unsatisfiedCount: 2,
          addAvailability: { kind: "available", remainingCapacity: 46 },
          items: [
            expect.objectContaining({ taskID: "task-2", satisfaction: "unsatisfied" }),
            expect.objectContaining({ taskID: "task-3", satisfaction: "unsatisfied" }),
            expect.objectContaining({ taskID: "task-1", satisfaction: "satisfied" }),
            expect.objectContaining({ taskID: "task-12", satisfaction: "satisfied" }),
          ],
        },
        {
          direction: "blocks",
          totalCount: 3,
          unsatisfiedCount: null,
          addAvailability: { kind: "available", remainingCapacity: 47 },
          items: [
            expect.objectContaining({ taskID: "task-10", satisfaction: null }),
            expect.objectContaining({ taskID: "task-4", satisfaction: null }),
            expect.objectContaining({ taskID: "task-20", satisfaction: null }),
          ],
        },
      ],
    });
  });

  it("keeps non-done Tasks before done Tasks when Short IDs interleave", () => {
    const prepared: readonly PreparedTaskDependency[] = [
      entry("blocked-by", "task-2", "KENT-2", done),
      entry("blocked-by", "task-10", "KENT-10", backlog),
    ];

    expect(preparedTaskDependenciesProjection(prepared).directions[0]?.items).toEqual([
      expect.objectContaining({ taskID: "task-10", satisfaction: "unsatisfied" }),
      expect.objectContaining({ taskID: "task-2", satisfaction: "satisfied" }),
    ]);
  });

  it("enforces uniqueness within a direction while permitting the same Task in both directions", () => {
    const blockedBy = entry("blocked-by", "task-2", "KENT-2", backlog);
    const once = insertPreparedTaskDependency([], blockedBy);
    expect(insertPreparedTaskDependency(once, blockedBy)).toBe(once);

    const opposite = insertPreparedTaskDependency(once, entry("blocks", "task-2", "KENT-2", backlog));
    expect(opposite).toHaveLength(2);
  });

  it("accepts the 50th entry and returns the unchanged collection for every later insertion", () => {
    let prepared: readonly PreparedTaskDependency[] = Array.from({ length: 49 }, (_, index) =>
      entry("blocked-by", `task-${index}`, `KENT-${index}`, backlog),
    );
    prepared = insertPreparedTaskDependency(prepared, entry("blocked-by", "task-50", "KENT-50", backlog));
    expect(prepared).toHaveLength(50);
    expect(preparedTaskDependenciesProjection(prepared).directions[0]?.addAvailability).toEqual({
      kind: "limit_reached",
    });

    expect(insertPreparedTaskDependency(prepared, entry("blocked-by", "task-51", "KENT-51", backlog))).toBe(
      prepared,
    );
  });
});

function entry(
  direction: PreparedTaskDependency["direction"],
  taskID: string,
  shortID: string,
  status: TaskStatus,
): PreparedTaskDependency {
  return {
    direction,
    taskID,
    shortID,
    title: `Task ${shortID}`,
    workflowID: "workflow-1",
    status,
  };
}
