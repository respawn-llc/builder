import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";

import type { WorkflowListInput, WorkflowRecord } from "@/api";
import { projectTaskWorkflowItems, useProjectTaskWorkflowPages } from "./projectTaskWorkflows";

const projectID = "project-1";
type ProjectTaskWorkflowFixture = {
  requests: number[];
  workflows: WorkflowRecord[];
};

const fixture = vi.hoisted<ProjectTaskWorkflowFixture>(() => ({
  requests: [],
  workflows: Array.from({ length: 130 }, (_value, index): WorkflowRecord => ({
    description: "",
    executionTargetPolicy: { customRef: null, mode: "default_branch" },
    id: `workflow-${index.toString()}`,
    name: `Workflow ${index.toString()}`,
    projectLink: { isDefault: index === 0 },
    version: 1,
  })),
}));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppServices: () => ({
    api: {
      listWorkflows: async (input: WorkflowListInput) => {
        const offset = input.offset ?? 0;
        const limit = input.limit ?? 40;
        fixture.requests.push(offset);
        return {
          nextOffset: offset + limit < fixture.workflows.length ? offset + limit : null,
          workflows: fixture.workflows.slice(offset, offset + limit),
        };
      },
    },
  }),
}));

beforeEach(() => {
  fixture.requests = [];
});

it("keeps a bounded bidirectional window of Project Workflow pages", async () => {
  const view = renderHook(() => useProjectTaskWorkflowPages(projectID), {
    wrapper: queryWrapper(),
  });

  await waitFor(() => {
    expect(view.result.current.isSuccess).toBe(true);
  });
  for (let page = 0; page < 3; page += 1) {
    await act(async () => {
      await view.result.current.fetchNextPage();
    });
  }

  expect(view.result.current.data?.pageParams).toEqual([40, 80, 120]);
  expect(projectTaskWorkflowItems(view.result.current.data)).toHaveLength(90);

  await act(async () => {
    await view.result.current.fetchPreviousPage();
  });

  expect(view.result.current.data?.pageParams).toEqual([0, 40, 80]);
  expect(projectTaskWorkflowItems(view.result.current.data)).toHaveLength(120);
  expect(fixture.requests).toEqual([0, 40, 80, 120, 0]);
});

function queryWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return function QueryWrapper({ children }: Readonly<{ children: ReactNode }>) {
    return createElement(QueryClientProvider, { children, client: queryClient });
  };
}
