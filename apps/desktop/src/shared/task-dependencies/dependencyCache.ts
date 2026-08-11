import type {
  TaskDependencies,
  TaskDependencyDirectionProjection,
  TaskDependencyItem,
  TaskDetail,
} from "@/api";

export type TaskDependencyPair = Readonly<{
  blockerTaskID: string;
  blockedTaskID: string;
}>;

export function optimisticTaskDependencyRemoval(detail: TaskDetail, pair: TaskDependencyPair): TaskDetail {
  if (detail.id === pair.blockedTaskID) {
    return removeBlockedBy(detail, pair.blockerTaskID);
  }
  if (detail.id === pair.blockerTaskID) {
    return removeBlocks(detail, pair.blockedTaskID);
  }
  return detail;
}

function removeBlockedBy(detail: TaskDetail, blockerTaskID: string): TaskDetail {
  const direction = requiredTaskDependencyDirection(detail.dependencies, "blocked-by");
  const removed = direction.items.find((item) => item.taskID === blockerTaskID);
  if (removed === undefined) {
    return detail;
  }
  const unsatisfiedDelta = removed.satisfaction === "unsatisfied" ? 1 : 0;
  return withDependencies(detail, {
    ...detail.dependencies,
    blockerCount: detail.dependencies.blockerCount - 1,
    unsatisfiedBlockerCount: detail.dependencies.unsatisfiedBlockerCount - unsatisfiedDelta,
    directions: detail.dependencies.directions.map((candidate) =>
      candidate.direction === "blocked-by"
        ? withoutItem(candidate, blockerTaskID, unsatisfiedDelta)
        : candidate,
    ),
  });
}

function removeBlocks(detail: TaskDetail, blockedTaskID: string): TaskDetail {
  const direction = requiredTaskDependencyDirection(detail.dependencies, "blocks");
  if (!direction.items.some((item) => item.taskID === blockedTaskID)) {
    return detail;
  }
  return withDependencies(detail, {
    ...detail.dependencies,
    directlyBlockedTaskCount: detail.dependencies.directlyBlockedTaskCount - 1,
    directions: detail.dependencies.directions.map((candidate) =>
      candidate.direction === "blocks" ? withoutItem(candidate, blockedTaskID, 0) : candidate,
    ),
  });
}

function withoutItem(
  direction: TaskDependencyDirectionProjection,
  taskID: string,
  unsatisfiedDelta: number,
): TaskDependencyDirectionProjection {
  const items = direction.items.filter((item) => item.taskID !== taskID);
  return {
    ...direction,
    totalCount: direction.totalCount - 1,
    unsatisfiedCount:
      direction.unsatisfiedCount === null ? null : direction.unsatisfiedCount - unsatisfiedDelta,
    items,
  };
}

export function requiredTaskDependencyDirection(
  dependencies: TaskDependencies,
  direction: TaskDependencyDirectionProjection["direction"],
): TaskDependencyDirectionProjection {
  const projection = dependencies.directions.find((candidate) => candidate.direction === direction);
  if (projection === undefined) {
    throw new Error(`Task Dependencies projection is missing ${direction}.`);
  }
  return projection;
}

function withDependencies(detail: TaskDetail, dependencies: TaskDependencies): TaskDetail {
  return { ...detail, dependencies };
}

export function dependencyRelatedTaskIDs(dependencies: TaskDependencies): ReadonlySet<string> {
  return new Set(
    dependencies.directions.flatMap((direction) =>
      direction.items.map((item: TaskDependencyItem) => item.taskID),
    ),
  );
}
