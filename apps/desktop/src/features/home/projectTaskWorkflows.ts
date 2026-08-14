import { useInfiniteQuery, type InfiniteData, type UseInfiniteQueryResult } from "@tanstack/react-query";

import { workflowPageSize, type WorkflowPage, type WorkflowRecord } from "@/api";
import { queryKeys, useAppServices, useRetainedQueryData } from "@/app-facade";

export type ProjectTaskWorkflowItem = Readonly<{
  description: string;
  id: string;
  isProjectDefault: boolean;
  name: string;
}>;

export type ProjectTaskWorkflowPage = Readonly<{
  workflows: readonly ProjectTaskWorkflowItem[];
  nextOffset: number | null;
}>;

const retainedProjectTaskWorkflowPages = 3;

export function useProjectTaskWorkflowPages(
  projectID: string,
): UseInfiniteQueryResult<InfiniteData<ProjectTaskWorkflowPage, number>> {
  const { api } = useAppServices();
  return useInfiniteQuery<
    ProjectTaskWorkflowPage,
    Error,
    InfiniteData<ProjectTaskWorkflowPage, number>,
    readonly unknown[],
    number
  >({
    queryKey: queryKeys.projectTaskWorkflows(projectID),
    queryFn: async ({ pageParam }) =>
      projectTaskWorkflowPage(
        await api.listWorkflows({
          limit: workflowPageSize,
          offset: pageParam,
          projectID,
        }),
      ),
    initialPageParam: 0,
    getPreviousPageParam: (_firstPage, _allPages, firstPageParam) =>
      firstPageParam === 0 ? undefined : Math.max(0, firstPageParam - workflowPageSize),
    getNextPageParam: (lastPage) => lastPage.nextOffset ?? undefined,
    maxPages: retainedProjectTaskWorkflowPages,
    gcTime: 0,
  });
}

function projectTaskWorkflowPage(page: WorkflowPage): ProjectTaskWorkflowPage {
  return {
    nextOffset: page.nextOffset,
    workflows: page.workflows.map(projectTaskWorkflowItem),
  };
}

function projectTaskWorkflowItem(workflow: WorkflowRecord): ProjectTaskWorkflowItem {
  if (workflow.projectLink === undefined) {
    throw new Error(`Project-scoped Workflow ${workflow.id} is missing its Project link.`);
  }
  return {
    description: workflow.description,
    id: workflow.id,
    isProjectDefault: workflow.projectLink.isDefault,
    name: workflow.name,
  };
}

export function projectTaskWorkflowItems(
  data: InfiniteData<ProjectTaskWorkflowPage, number> | undefined,
): readonly ProjectTaskWorkflowItem[] {
  return data?.pages.flatMap((page) => page.workflows) ?? [];
}

export function useProjectTaskNewTaskAvailable(
  projectID: string,
  data: InfiniteData<ProjectTaskWorkflowPage, number> | undefined,
): boolean {
  return (
    useRetainedQueryData(projectID, firstPageNewTaskAvailability(data), (left, right) => left === right) ??
    false
  );
}

function firstPageNewTaskAvailability(
  data: InfiniteData<ProjectTaskWorkflowPage, number> | undefined,
): boolean | undefined {
  const firstPageIndex = data?.pageParams.findIndex((pageParam) => pageParam === 0) ?? -1;
  const firstPage = firstPageIndex < 0 ? undefined : data?.pages[firstPageIndex];
  if (firstPage === undefined) {
    return undefined;
  }
  return (
    (firstPage.workflows.length === 1 && firstPage.nextOffset === null) ||
    firstPage.workflows.some((workflow) => workflow.isProjectDefault)
  );
}
