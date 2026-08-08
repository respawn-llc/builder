import { BoardFilterChrome } from "./BoardLabelFilter";
import { TaskSearchProjectTrigger } from "./TaskSearchChrome";

export function BoardFilterRow({
  onOpenTask,
  projectID,
}: Readonly<{
  onOpenTask(taskID: string): void;
  projectID: string;
}>) {
  return (
    <>
      <BoardFilterChrome />
      <TaskSearchProjectTrigger onOpenTask={onOpenTask} projectID={projectID} />
    </>
  );
}
