import type {
  TaskDependencies,
  TaskDependencyDirection,
  TaskDependencyDirectionProjection,
  TaskDependencyItem,
} from "@/api";
import type { NewTaskPreparedDependency } from "@/app-facade";

export const taskDependencyMaxPerDirection = 50;

export type PreparedTaskDependency = NewTaskPreparedDependency;

export function insertPreparedTaskDependency(
  dependencies: readonly PreparedTaskDependency[],
  dependency: PreparedTaskDependency,
): readonly PreparedTaskDependency[] {
  const directionEntries = dependencies.filter((entry) => entry.direction === dependency.direction);
  if (
    directionEntries.length >= taskDependencyMaxPerDirection ||
    directionEntries.some((entry) => entry.taskID === dependency.taskID)
  ) {
    return dependencies;
  }
  return [...dependencies, dependency];
}

export function removePreparedTaskDependency(
  dependencies: readonly PreparedTaskDependency[],
  direction: TaskDependencyDirection,
  taskID: string,
): readonly PreparedTaskDependency[] {
  const next = dependencies.filter((entry) => entry.direction !== direction || entry.taskID !== taskID);
  return next.length === dependencies.length ? dependencies : next;
}

export function preparedTaskDependenciesProjection(
  dependencies: readonly PreparedTaskDependency[],
): TaskDependencies {
  const blockedBy = preparedDirectionProjection(dependencies, "blocked-by");
  const blocks = preparedDirectionProjection(dependencies, "blocks");
  return {
    blockerCount: blockedBy.totalCount,
    unsatisfiedBlockerCount: blockedBy.unsatisfiedCount ?? 0,
    directlyBlockedTaskCount: blocks.totalCount,
    directions: [blockedBy, blocks],
  };
}

function preparedDirectionProjection(
  dependencies: readonly PreparedTaskDependency[],
  direction: TaskDependencyDirection,
): TaskDependencyDirectionProjection {
  const entries = dependencies
    .filter((entry) => entry.direction === direction)
    .sort(comparePreparedDependencyRows);
  if (entries.length > taskDependencyMaxPerDirection) {
    throw new Error(
      `Prepared Task Dependencies ${direction} count exceeds ${taskDependencyMaxPerDirection}.`,
    );
  }
  const items = entries.map((entry): TaskDependencyItem => {
    const satisfaction =
      direction === "blocked-by" ? (entry.status.kind === "done" ? "satisfied" : "unsatisfied") : null;
    return {
      taskID: entry.taskID,
      shortID: entry.shortID,
      title: entry.title,
      workflowID: entry.workflowID,
      status: entry.status,
      satisfaction,
    };
  });
  const unsatisfiedCount =
    direction === "blocked-by"
      ? items.reduce((count, item) => count + (item.satisfaction === "unsatisfied" ? 1 : 0), 0)
      : null;
  const remainingCapacity = taskDependencyMaxPerDirection - items.length;
  return {
    direction,
    totalCount: items.length,
    unsatisfiedCount,
    items,
    addAvailability:
      remainingCapacity === 0 ? { kind: "limit_reached" } : { kind: "available", remainingCapacity },
  };
}

function comparePreparedDependencyRows(left: PreparedTaskDependency, right: PreparedTaskDependency): number {
  const leftDone = left.status.kind === "done";
  const rightDone = right.status.kind === "done";
  if (leftDone !== rightDone) {
    return leftDone ? 1 : -1;
  }
  if (left.shortID !== right.shortID) {
    return left.shortID < right.shortID ? -1 : 1;
  }
  if (left.taskID === right.taskID) {
    return 0;
  }
  return left.taskID < right.taskID ? -1 : 1;
}
