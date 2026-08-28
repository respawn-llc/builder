import { TaskSortChrome } from "@/shared/task-sorting";
import { projectTaskSortFieldOptions, type ProjectTaskSort } from "./projectTaskSorting";

export function ProjectSortChrome({
  onSortChange,
  sort,
}: Readonly<{
  onSortChange(sort: ProjectTaskSort): void;
  sort: ProjectTaskSort;
}>) {
  return (
    <TaskSortChrome fieldOptions={projectTaskSortFieldOptions} onSortChange={onSortChange} sort={sort} />
  );
}
