import type { TaskDependencies, TaskDependencyDirection } from "@/api";
import {
  DependenciesArea,
  dependencyRelatedTaskIDs,
  taskDependencyPairForDirection,
  type TaskDependencyPair,
} from "@/shared/task-dependencies";

export function TaskDependenciesArea({
  dependencies,
  disabled,
  navigationDisabled,
  onAdd,
  onAddExisting,
  onRemove,
  onSelectTask,
  projectID,
  taskID,
}: Readonly<{
  dependencies: TaskDependencies;
  disabled: boolean;
  navigationDisabled: boolean;
  onAdd(direction: TaskDependencyDirection): void;
  onAddExisting(pair: TaskDependencyPair): Promise<unknown>;
  onRemove(pair: TaskDependencyPair): void;
  onSelectTask(taskID: string): void;
  projectID: string;
  taskID: string;
}>) {
  const relatedTaskIDs = dependencyRelatedTaskIDs(dependencies);
  const excludedTaskIDs = new Set([taskID, ...relatedTaskIDs]);
  return (
    <DependenciesArea
      dependencies={dependencies}
      disabled={disabled}
      excludedTaskIDs={() => excludedTaskIDs}
      navigationDisabled={navigationDisabled}
      onAdd={onAdd}
      onRemove={(direction, item) => {
        onRemove(taskDependencyPairForDirection(taskID, direction, item.taskID));
      }}
      onSelectCandidate={(direction, result) =>
        onAddExisting(taskDependencyPairForDirection(taskID, direction, result.group.taskID))
      }
      onSelectTask={onSelectTask}
      projectID={projectID}
    />
  );
}
