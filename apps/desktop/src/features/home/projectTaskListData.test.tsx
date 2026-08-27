import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, vi } from "vitest";

import type * as AppFacade from "@/app-facade";
import type {
  ProjectTaskGroupCounts,
  ProjectTaskGroupDefinition,
  TaskListInput,
  TaskListPage,
  WorkflowProjectEvent,
  WorkflowProjectEventHandler,
} from "@/api";
import { queryKeys } from "@/app-facade";
import {
  projectTaskGroupPageSize,
  projectTaskGroupRetainedPages,
  useProjectTaskListData,
  useProjectTaskListEvents,
} from "./projectTaskListData";
import { defaultProjectTaskSort, type ProjectTaskSort } from "./projectTaskSorting";

const projectTaskGroupDefinitions: readonly ProjectTaskGroupDefinition[] = [
  {
    group: "active",
    statusKinds: ["waiting_question", "waiting_approval", "interrupted", "running", "queued", "active"],
  },
  { group: "backlog", statusKinds: ["backlog"] },
  { group: "done", statusKinds: ["done"] },
];

type TaskGroup = "active" | "backlog" | "done";

interface TestState {
  countRequests: number;
  getCounts: () => Promise<ProjectTaskGroupCounts>;
  handlers: WorkflowProjectEventHandler[];
  listPage: (input: TaskListInput) => Promise<TaskListPage>;
  listRequests: TaskListInput[];
  subscriptionCloses: number;
}

const state: TestState = {
  countRequests: 0,
  getCounts: async () => countsResponse(2),
  handlers: [],
  listPage: async (input) => pageResponse(taskGroupForInput(input), input.offset ?? 0),
  listRequests: [],
  subscriptionCloses: 0,
};

const mockedApi = {
  getProjectTaskGroupCounts: async () => {
    state.countRequests += 1;
    return state.getCounts();
  },
  listTasks: async (input: TaskListInput) => {
    state.listRequests.push(input);
    return state.listPage(input);
  },
  subscribeProject: (_projectID: string, handler: WorkflowProjectEventHandler) => {
    state.handlers.push(handler);
    return {
      close() {
        state.subscriptionCloses += 1;
      },
    };
  },
};
const mockedServices = {
  api: mockedApi,
  logger: { append: async () => undefined },
};

vi.mock("@/app-facade", async (importOriginal) => {
  const actual = await importOriginal<typeof AppFacade>();
  return {
    ...actual,
    useAppServices: () => mockedServices,
    useConnectionSnapshot: () => connectionSnapshot,
  };
});

const connectionSnapshot = { generation: 1, phase: "connected" as const };

describe("Project Task-list data ownership", () => {
  afterEach(() => {
    state.countRequests = 0;
    state.getCounts = async () => countsResponse(2);
    state.handlers.length = 0;
    state.listPage = async (input) => pageResponse(taskGroupForInput(input), input.offset ?? 0);
    state.listRequests.length = 0;
    state.subscriptionCloses = 0;
  });

  it("starts counts and expanded first pages in parallel while collapsed groups stay absent", async () => {
    const pendingCounts = deferred<ProjectTaskGroupCounts>();
    state.getCounts = async () => pendingCounts.promise;
    const harness = createHarness();
    const { result, rerender } = renderHook(
      ({ active, backlog, done }) =>
        useProjectTaskListData({
          projectID: "project-1",
          expanded: { active, backlog, done },
        }),
      {
        initialProps: { active: true, backlog: true, done: false },
        wrapper: ({ children }) => harness.render(children),
      },
    );

    await waitFor(() => {
      expect(state.countRequests).toBe(1);
      expect(state.listRequests).toHaveLength(2);
    });
    expect(state.listRequests.map((request) => request.group)).toEqual(["active", "backlog"]);
    pendingCounts.resolve(countsResponse(2));
    await waitFor(() => {
      expect(
        harness.queryClient
          .getQueriesData({
            queryKey: queryKeys.projectTaskListsRoot("project-1"),
          })
          .filter(([, data]) => data !== undefined),
      ).toHaveLength(3);
    });

    rerender({ active: false, backlog: true, done: false });
    await waitFor(() => {
      expect(
        harness.queryClient.getQueryData(
          queryKeys.projectTaskGroup("project-1", "active", defaultProjectTaskSort),
        ),
      ).toBeUndefined();
    });
    expect(result.current.active.tasks).toEqual([]);
    expect(
      harness.queryClient
        .getQueriesData({
          queryKey: queryKeys.projectTaskListsRoot("project-1"),
        })
        .filter(([, data]) => data !== undefined),
    ).toHaveLength(2);
  });

  it("starts at zero and exposes the current bounded window edges", async () => {
    state.listPage = async (input) => {
      const offset = input.offset ?? 0;
      return {
        ...pageResponse(taskGroupForInput(input), offset),
        nextOffset:
          offset < projectTaskGroupPageSize * projectTaskGroupRetainedPages
            ? offset + projectTaskGroupPageSize
            : null,
      };
    };
    const harness = createHarness();
    const { result, unmount } = renderHook(
      () =>
        useProjectTaskListData({
          projectID: "project-1",
          expanded: { active: true, backlog: false, done: false },
        }),
      { wrapper: ({ children }) => harness.render(children) },
    );

    await waitFor(() => {
      expect(state.listRequests[0]).toMatchObject({
        limit: 50,
        offset: 0,
        projectID: "project-1",
        group: "active",
        sort: [{ field: "updated", direction: "desc" }],
      });
    });
    for (let page = 1; page <= projectTaskGroupRetainedPages; page += 1) {
      await act(async () => {
        await result.current.active.fetchNextPage();
      });
    }
    await waitFor(() => {
      expect(result.current.active.pages).toHaveLength(10);
    });
    expect(state.listRequests.map((request) => request.offset)).toEqual([
      0, 50, 100, 150, 200, 250, 300, 350, 400, 450, 500,
    ]);
    expect(result.current.active.tasks.at(0)?.id).toBe("active-50");
    expect(result.current.active.nextRequestGeneration).toBe("project-1:end");

    await act(async () => {
      await result.current.active.fetchPreviousPage();
    });
    await waitFor(() => {
      expect(result.current.active.pages).toHaveLength(10);
    });
    expect(state.listRequests.at(-1)?.offset).toBe(0);
    expect(result.current.active.tasks.at(0)?.id).toBe("active-0");
    expect(result.current.active.nextRequestGeneration).toBe("project-1:500");
    unmount();
    expect(
      harness.queryClient.getQueryData(
        queryKeys.projectTaskGroup("project-1", "active", defaultProjectTaskSort),
      ),
    ).toBeDefined();
  });

  it("sends the selected sort to every expanded group and preserves server row order", async () => {
    const selectedSort = { field: "title", direction: "asc" } as const;
    state.listPage = async (input) => {
      const response = pageResponse(taskGroupForInput(input), input.offset ?? 0);
      const firstTask = response.tasks[0];
      if (firstTask === undefined) throw new Error("Expected a task fixture.");
      return {
        ...response,
        tasks: [
          { ...firstTask, id: `${firstTask.id}-last`, title: "Zeta" },
          { ...firstTask, id: `${firstTask.id}-first`, title: "Alpha" },
        ],
      };
    };
    const harness = createHarness();
    const { result } = renderHook(
      () =>
        useProjectTaskListData({
          expanded: { active: true, backlog: true, done: false },
          projectID: "project-1",
          sort: selectedSort,
        }),
      { wrapper: ({ children }) => harness.render(children) },
    );

    await waitFor(() => {
      expect(state.listRequests).toHaveLength(2);
      expect(result.current.active.tasks).toHaveLength(2);
    });
    expect(state.listRequests).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ group: "active", offset: 0, sort: [selectedSort] }),
        expect.objectContaining({ group: "backlog", offset: 0, sort: [selectedSort] }),
      ]),
    );
    expect(result.current.active.tasks.map((task) => task.id)).toEqual(["active-0-last", "active-0-first"]);
    expect(result.current.done.tasks).toEqual([]);
  });

  it("keeps source rows while the selected sort replacement is pending or failed", async () => {
    const pendingReplacement = deferred<TaskListPage>();
    let requestCount = 0;
    state.listPage = async (input) => {
      requestCount += 1;
      if (input.sort?.[0]?.field === "created") {
        return pendingReplacement.promise;
      }
      return pageResponse(taskGroupForInput(input), input.offset ?? 0);
    };
    const harness = createHarness();
    const initialSort: ProjectTaskSort = defaultProjectTaskSort;
    const { result, rerender } = renderHook(
      ({ sort }) =>
        useProjectTaskListData({
          expanded: { active: true, backlog: false, done: false },
          projectID: "project-1",
          sort,
        }),
      {
        initialProps: { sort: initialSort },
        wrapper: ({ children }) => harness.render(children),
      },
    );
    await waitFor(() => {
      expect(result.current.active.tasks).toHaveLength(1);
    });

    rerender({ sort: { field: "created", direction: "desc" } satisfies ProjectTaskSort });
    await waitFor(() => {
      expect(result.current.active.isSortReplacement).toBe(true);
    });
    expect(result.current.active.tasks[0]?.id).toBe("active-0");

    pendingReplacement.reject(new Error("replacement failed"));
    await waitFor(() => {
      expect(result.current.active.isError).toBe(true);
    });
    expect(result.current.active.isSortReplacement).toBe(true);
    expect(result.current.active.tasks[0]?.id).toBe("active-0");
    expect(requestCount).toBe(2);
  });

  it("evicts a collapsed group and restarts its bounded query at zero", async () => {
    const harness = createHarness();
    const { result, rerender } = renderHook(
      ({ expanded }) =>
        useProjectTaskListData({
          projectID: "project-1",
          expanded: { active: expanded, backlog: false, done: false },
        }),
      {
        initialProps: { expanded: true },
        wrapper: ({ children }) => harness.render(children),
      },
    );
    await waitFor(() => {
      expect(result.current.active.nextRequestGeneration).toBe("project-1:50");
    });

    rerender({ expanded: false });
    await waitFor(() => {
      expect(result.current.active.pages).toEqual([]);
    });
    rerender({ expanded: true });
    await waitFor(() => {
      expect(result.current.active.nextRequestGeneration).toBe("project-1:50");
    });
  });

  it("keeps rows and exact counts visible while replacement requests refresh", async () => {
    const pendingCounts = deferred<ProjectTaskGroupCounts>();
    const pendingPage = deferred<TaskListPage>();
    let countsCall = 0;
    let pageCall = 0;
    state.getCounts = async () => {
      countsCall += 1;
      return countsCall === 1 ? countsResponse(2) : pendingCounts.promise;
    };
    state.listPage = async (input) => {
      pageCall += 1;
      return pageCall === 1 ? pageResponse(taskGroupForInput(input), input.offset ?? 0) : pendingPage.promise;
    };
    const harness = createHarness();
    const { result } = renderHook(
      () =>
        useProjectTaskListData({
          projectID: "project-1",
          expanded: { active: true, backlog: false, done: false },
        }),
      { wrapper: ({ children }) => harness.render(children) },
    );
    await waitFor(() => {
      expect(result.current.counts.data?.counts.active).toBe(2);
      expect(result.current.active.tasks).toHaveLength(1);
    });

    await act(async () => {
      void result.current.counts.refetch();
      void result.current.active.refetch();
    });
    await waitFor(() => {
      expect(result.current.counts.isFetching).toBe(true);
      expect(result.current.active.isFetching).toBe(true);
    });
    expect(result.current.counts.data?.counts.active).toBe(2);
    expect(result.current.active.tasks).toHaveLength(1);

    pendingCounts.resolve(countsResponse(3));
    pendingPage.resolve(pageResponse("active", 0));
    await waitFor(() => {
      expect(result.current.counts.data?.counts.active).toBe(3);
      expect(result.current.active.isFetching).toBe(false);
    });
  });

  it("clears retained rows on collapse so reopen failure is an initial error", async () => {
    let fail = false;
    state.listPage = async (input) => {
      if (fail) throw new Error("reopen failed");
      return pageResponse(taskGroupForInput(input), input.offset ?? 0);
    };
    const harness = createHarness();
    const { result, rerender } = renderHook(
      ({ expanded }) =>
        useProjectTaskListData({
          projectID: "project-1",
          expanded: { active: expanded, backlog: false, done: false },
        }),
      {
        initialProps: { expanded: true },
        wrapper: ({ children }) => harness.render(children),
      },
    );
    await waitFor(() => {
      expect(result.current.active.tasks).toHaveLength(1);
    });

    rerender({ expanded: false });
    expect(result.current.active.tasks).toEqual([]);
    await waitFor(() => {
      expect(
        harness.queryClient.getQueryData(
          queryKeys.projectTaskGroup("project-1", "active", defaultProjectTaskSort),
        ),
      ).toBeUndefined();
    });
    fail = true;
    rerender({ expanded: true });
    await waitFor(() => {
      expect(result.current.active.isError).toBe(true);
    });

    expect(result.current.active.tasks).toEqual([]);
  });

  it("distinguishes a first-page failure from a retained next-edge failure", async () => {
    state.listPage = async () => {
      throw new Error("first page failed");
    };
    const initialHarness = createHarness();
    const { result: initialResult, unmount: unmountInitial } = renderHook(
      () =>
        useProjectTaskListData({
          projectID: "project-1",
          expanded: { active: true, backlog: false, done: false },
        }),
      { wrapper: ({ children }) => initialHarness.render(children) },
    );
    await waitFor(() => {
      expect(initialResult.current.active.isError).toBe(true);
    });
    expect(initialResult.current.active.isFetchNextPageError).toBe(false);
    expect(initialResult.current.active.tasks).toEqual([]);
    unmountInitial();

    let pageCall = 0;
    state.listPage = async (input) => {
      pageCall += 1;
      if (pageCall === 1) {
        return pageResponse(taskGroupForInput(input), input.offset ?? 0);
      }
      throw new Error("next page failed");
    };
    const edgeHarness = createHarness();
    const { result: edgeResult } = renderHook(
      () =>
        useProjectTaskListData({
          projectID: "project-1",
          expanded: { active: true, backlog: false, done: false },
        }),
      { wrapper: ({ children }) => edgeHarness.render(children) },
    );
    await waitFor(() => {
      expect(edgeResult.current.active.tasks).toHaveLength(1);
    });
    await act(async () => {
      await edgeResult.current.active.fetchNextPage();
    });
    await waitFor(() => {
      expect(edgeResult.current.active.isFetchNextPageError).toBe(true);
    });
    expect(edgeResult.current.active.tasks).toHaveLength(1);
  });

  it("owns one typed Project subscription and refreshes only the affected roots", async () => {
    const harness = createHarness();
    const invalidations: (readonly unknown[])[] = [];
    const invalidateSpy = vi
      .spyOn(harness.queryClient, "invalidateQueries")
      .mockImplementation(async (...args) => {
        invalidations.push(args[0]?.queryKey ?? []);
      });
    const { unmount } = renderHook(
      () => {
        useProjectTaskListEvents({ enabled: true, projectID: "project-1" });
      },
      {
        wrapper: ({ children }) => harness.render(children),
      },
    );
    await waitFor(() => {
      expect(state.handlers).toHaveLength(1);
    });

    act(() => {
      state.handlers[0]?.onEvent(workflowEvent("task", "moved", "task-2"));
    });
    await waitFor(() => {
      expect(invalidations).toContainEqual(queryKeys.projectTaskListsRoot("project-1"));
    });
    expect(invalidations).not.toContainEqual(queryKeys.projectLabels("project-1"));

    invalidations.length = 0;
    act(() => {
      state.handlers[0]?.onEvent(workflowEvent("task", "dependencies_changed", "task-2"));
    });
    await waitFor(() => {
      expect(invalidations).toContainEqual(queryKeys.projectTaskListsRoot("project-1"));
    });
    expect(invalidations).not.toContainEqual(queryKeys.projectBoardsRoot("project-1"));

    invalidations.length = 0;
    act(() => {
      state.handlers[0]?.onEvent(workflowEvent("label", "renamed", "label-1"));
    });
    await waitFor(() => {
      expect(invalidations).toContainEqual(queryKeys.projectTaskListsRoot("project-1"));
    });
    expect(invalidations).not.toContainEqual(queryKeys.projectLabels("project-1"));

    invalidations.length = 0;
    act(() => {
      state.handlers[0]?.onEvent(workflowEvent("task", "comment_added", "task-1"));
    });
    await Promise.resolve();
    expect(invalidations).toEqual([]);

    invalidations.length = 0;
    act(() => {
      state.handlers[0]?.onEvent(workflowEvent("task", "labels_changed", "task-1"));
    });
    await waitFor(() => {
      expect(invalidations).toContainEqual(queryKeys.projectTaskListsRoot("project-1"));
    });
    expect(invalidations).not.toContainEqual(queryKeys.taskLabels("task-1"));

    invalidations.length = 0;
    act(() => {
      state.handlers[0]?.onEvent(workflowEvent("workflow", "graph_saved", "workflow-1"));
    });
    await waitFor(() => {
      expect(invalidations).toContainEqual(queryKeys.projectBoardsRoot("project-1"));
    });
    expect(invalidations).toContainEqual(queryKeys.projectTaskListsRoot("project-1"));

    invalidations.length = 0;
    act(() => {
      state.handlers[0]?.onEvent(workflowEvent("workflow_link", "linked", "link-1"));
    });
    await waitFor(() => {
      expect(invalidations).toContainEqual(queryKeys.projectBoardsRoot("project-1"));
    });
    expect(invalidations).toContainEqual(queryKeys.projectTaskListsRoot("project-1"));

    unmount();
    expect(state.subscriptionCloses).toBe(1);
    invalidateSpy.mockRestore();
  });
});

function createHarness() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return {
    queryClient,
    render(children: ReactNode) {
      return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
    },
  };
}

function countsResponse(active: number): ProjectTaskGroupCounts {
  return {
    projectID: "project-1",
    definitions: projectTaskGroupDefinitions,
    counts: { active, backlog: 1, done: 0 },
    generatedAt: 1,
  };
}

function taskGroupForInput(input: TaskListInput): TaskGroup {
  if (input.group === undefined) throw new Error("Expected a Project Task group.");
  return input.group;
}

function pageResponse(group: TaskGroup, offset: number): TaskListPage {
  return {
    scope: { projectID: "project-1", workflowID: null },
    matchingWorkflowCardinality: "one",
    nextOffset: offset < 100 ? offset + projectTaskGroupPageSize : null,
    generatedAt: 1,
    tasks: [
      {
        id: [group, offset].join("-"),
        shortID: ["KNT", offset + 1].join("-"),
        workflowID: "workflow-1",
        workflowName: "Default",
        title: "Task",
        createdAt: 1,
        updatedAt: 1,
        columnKeys: null,
        status: {
          kind: group === "done" ? "done" : group === "backlog" ? "backlog" : "active",
          nativeState: group,
          nodeIDs: [],
          attentionTypes: [],
        },
        labels: [],
        dependencyProgress: null,
      },
    ],
  };
}

function workflowEvent(
  resource: WorkflowProjectEvent["resource"],
  action: WorkflowProjectEvent["action"],
  primaryEntityID: string,
): WorkflowProjectEvent {
  return {
    action,
    occurredAtUnixMs: 1,
    primaryEntityID,
    projectID: resource === "workflow" ? null : "project-1",
    relatedIDs: [],
    resource,
    workflowID: resource === "label" ? null : resource === "workflow" ? primaryEntityID : "workflow-1",
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve;
    reject = nextReject;
  });
  return { promise, reject, resolve };
}
