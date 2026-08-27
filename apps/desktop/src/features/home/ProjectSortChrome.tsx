import { SortChrome } from "@/ui";
import { projectTaskSortFieldOptions, type ProjectTaskSort } from "./projectTaskSorting";

export function ProjectSortChrome({
  onSortChange,
  sort,
}: Readonly<{
  onSortChange(sort: ProjectTaskSort): void;
  sort: ProjectTaskSort;
}>) {
  return <SortChrome fieldOptions={projectTaskSortFieldOptions} onSortChange={onSortChange} sort={sort} />;
}
