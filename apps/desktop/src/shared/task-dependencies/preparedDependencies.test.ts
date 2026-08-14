import type { TaskStatus, TaskStatusKind } from "@/api";
import {
  insertPreparedTaskDependency,
  preparedTaskDependenciesProjection,
  type PreparedTaskDependency,
} from "./preparedDependencies";

describe("prepared Task Dependencies", () => {
  it("derives ordered progress and capacity without changing Draft order", () => {
    const prepared = [
      entry("blocked-by", "KENT-12", "done"),
      entry("blocked-by", "KENT-3"),
      entry("blocks", "KENT-4"),
      entry("blocked-by", "KENT-2"),
      entry("blocks", "KENT-10"),
    ];
    const projection = preparedTaskDependenciesProjection(prepared);

    expect(projection).toMatchObject({
      blockerCount: 3,
      unsatisfiedBlockerCount: 2,
      directlyBlockedTaskCount: 2,
      directions: [
        { totalCount: 3, unsatisfiedCount: 2, addAvailability: { remainingCapacity: 47 } },
        { totalCount: 2, unsatisfiedCount: null, addAvailability: { remainingCapacity: 48 } },
      ],
    });
    expect(projection.directions[0]?.items.map(({ shortID }) => shortID)).toEqual(["KENT-2", "KENT-3", "KENT-12"]);
    expect(projection.directions[1]?.items.map(({ shortID }) => shortID)).toEqual(["KENT-10", "KENT-4"]);
    expect(prepared.map(({ shortID }) => shortID)).toEqual(["KENT-12", "KENT-3", "KENT-4", "KENT-2", "KENT-10"]);
  });

  it("enforces per-direction uniqueness and the 50-entry boundary", () => {
    const blockedBy = entry("blocked-by", "KENT-1");
    const once = insertPreparedTaskDependency([], blockedBy);
    expect(insertPreparedTaskDependency(once, blockedBy)).toBe(once);
    expect(insertPreparedTaskDependency(once, entry("blocks", "KENT-1"))).toHaveLength(2);

    const prepared = Array.from({ length: 49 }, (_, index) => entry("blocked-by", `KENT-${index}`));
    const full = insertPreparedTaskDependency(prepared, entry("blocked-by", "KENT-50"));
    expect(full).toHaveLength(50);
    expect(preparedTaskDependenciesProjection(full).directions[0]?.addAvailability).toEqual({ kind: "limit_reached" });
    expect(insertPreparedTaskDependency(full, entry("blocked-by", "KENT-51"))).toBe(full);
  });
});

function entry(
  direction: PreparedTaskDependency["direction"],
  shortID: string,
  kind: TaskStatusKind = "backlog",
): PreparedTaskDependency {
  const status: TaskStatus = { kind, nativeState: kind === "done" ? "terminal" : "active", nodeIDs: [], attentionTypes: [] };
  return { direction, taskID: shortID, shortID, title: shortID, workflowID: "workflow-1", status };
}
