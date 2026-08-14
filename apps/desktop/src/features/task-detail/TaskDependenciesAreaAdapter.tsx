import type { TaskDependencies, TaskDependencyDirection } from "@/api";
import { DependenciesArea, dependencyRelatedTaskIDs, taskDependencyPairForDirection, type TaskDependencyPair } from "@/shared/task-dependencies";

type Props = Readonly<{
  dependencies: TaskDependencies;
  disabled: boolean;
  navigationDisabled: boolean;
  onAdd(direction: TaskDependencyDirection): void;
  onAddExisting(pair: TaskDependencyPair): Promise<unknown>;
  onRemove(pair: TaskDependencyPair): void;
  onSelectTask(taskID: string): void;
  projectID: string;
  taskID: string;
}>;

export function TaskDependenciesArea(props: Props) {
  const { dependencies, disabled, navigationDisabled, onAdd, onAddExisting, onRemove, onSelectTask, projectID, taskID } = props;
  const excludedTaskIDs = new Set([taskID, ...dependencyRelatedTaskIDs(dependencies)]);
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
      onSelectCandidate={async (direction, result) =>
        onAddExisting(taskDependencyPairForDirection(taskID, direction, result.group.taskID))
      }
      onSelectTask={onSelectTask}
      projectID={projectID}
    />
  );
}
