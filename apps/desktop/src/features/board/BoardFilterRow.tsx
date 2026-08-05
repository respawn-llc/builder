import { BoardFilterChrome } from "./BoardLabelFilter";
import { BoardTaskSearchChrome } from "./BoardTaskSearch";

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
      <BoardTaskSearchChrome onOpenTask={onOpenTask} projectID={projectID} />
    </>
  );
}
