import {
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, vi } from "vitest";

import type * as AppFacade from "@/app-facade";
import type {
  ProjectTaskGroupCounts,
  TaskListInput,
  TaskListPage,
  WorkflowProjectEvent,
  WorkflowProjectEventHandler,
} from "@/api";
import { queryKeys } from "@/app-facade";
import {
  projectTaskFinalPageAnchor,
  projectTaskGroupPageSize,
  useProjectTaskListData,
  useProjectTaskListEvents,
} from "./projectTaskListData";

type TaskGroup = NonNullable<TaskListInput["group"]>;

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
  listPage: async (input) => pageResponse(input.group ?? "active", input.offset ?? 0),
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
    state.listPage = async (input) => pageResponse(input.group ?? "active", input.offset ?? 0);
    state.listRequests.length = 0;
    state.subscriptionCloses = 0;
  });

  it("gates counts and explicit group pages behind the board and keeps collapsed groups absent", async () => {
    const pendingCounts = deferred<ProjectTaskGroupCounts>();
    state.getCounts = async () => pendingCounts.promise;
    const harness = createHarness();
    const { result, rerender } = renderHook(
      ({ gateReady, active, backlog, done }) =>
        useProjectTaskListData({
          projectID: "project-1",
          gateReady,
          expanded: { active, backlog, done },
          anchors: { active: 0, backlog: 0, done: 0 },
        }),
      {
        initialProps: { gateReady: false, active: true, backlog: true, done: false },
        wrapper: ({ children }) => harness.render(children),
      },
    );

    expect(state.countRequests).toBe(0);
    expect(state.listRequests).toEqual([]);

    rerender({ gateReady: true, active: true, backlog: true, done: false });
    await waitFor(() => {
      expect(state.countRequests).toBe(1);
    });
    expect(state.listRequests).toEqual([]);
    pendingCounts.resolve(countsResponse(2));
    await waitFor(() => {
      expect(state.listRequests).toHaveLength(2);
    });
    expect(state.listRequests.map((request) => request.group)).toEqual(["active", "backlog"]);
    await waitFor(() => {
      expect(
        harness.queryClient.getQueriesData({
          queryKey: queryKeys.projectTaskListsRoot("project-1"),
        }).filter(([, data]) => data !== undefined),
      ).toHaveLength(3);
    });

    rerender({ gateReady: true, active: false, backlog: true, done: false });
    await waitFor(() => {
      expect(
        harness.queryClient.getQueryData(
          queryKeys.projectTaskGroup("project-1", "active", 0),
        ),
      ).toBeUndefined();
    });
    expect(result.current.active.tasks).toEqual([]);
    expect(
      harness.queryClient.getQueriesData({
        queryKey: queryKeys.projectTaskListsRoot("project-1"),
      }).filter(([, data]) => data !== undefined),
    ).toHaveLength(2);
  });

  it("anchors directly at the final bounded page and retains only three independently paged pages", async () => {
    const harness = createHarness();
    const anchor = projectTaskFinalPageAnchor(105);
    expect(anchor).toBe(100);
    const { result } = renderHook(
      () =>
        useProjectTaskListData({
          projectID: "project-1",
          gateReady: true,
          expanded: { active: true, backlog: false, done: false },
          anchors: { active: anchor, backlog: 0, done: 0 },
        }),
      { wrapper: ({ children }) => harness.render(children) },
    );

    await waitFor(() => {
      expect(state.listRequests[0]).toMatchObject({
        group: "active",
        limit: projectTaskGroupPageSize,
        offset: 100,
        projectID: "project-1",
        sort: [{ field: "updated", direction: "desc" }],
      });
    });
    await act(async () => {
      await result.current.active.fetchPreviousPage();
      await result.current.active.fetchPreviousPage();
      await result.current.active.fetchPreviousPage();
    });
    await waitFor(() => {
      expect(result.current.active.pageParams).toEqual([25, 50, 75]);
    });
    expect(state.listRequests.map((request) => request.offset)).toEqual([100, 75, 50, 25]);
    expect(result.current.active.pages).toHaveLength(3);
    expect(result.current.backlog.pages).toEqual([]);
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
      return pageCall === 1 ? pageResponse(input.group ?? "active", input.offset ?? 0) : pendingPage.promise;
    };
    const harness = createHarness();
    const { result } = renderHook(
      () =>
        useProjectTaskListData({
          projectID: "project-1",
          gateReady: true,
          expanded: { active: true, backlog: false, done: false },
          anchors: { active: 0, backlog: 0, done: 0 },
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

  it("keeps the prior bounded window when a direct replacement anchor fails", async () => {
    state.listPage = async (input) => {
      if (input.offset === 0) {
        return pageResponse(input.group ?? "active", 0);
      }
      throw new Error("final page failed");
    };
    const harness = createHarness();
    const { result, rerender } = renderHook(
      ({ anchor }) =>
        useProjectTaskListData({
          projectID: "project-1",
          gateReady: true,
          expanded: { active: true, backlog: false, done: false },
          anchors: { active: anchor, backlog: 0, done: 0 },
        }),
      {
        initialProps: { anchor: 0 },
        wrapper: ({ children }) => harness.render(children),
      },
    );
    await waitFor(() => {
      expect(result.current.active.tasks).toHaveLength(1);
    });

    rerender({ anchor: projectTaskGroupPageSize });
    await waitFor(() => {
      expect(result.current.active.isError).toBe(true);
    });

    expect(result.current.active.tasks).toHaveLength(1);
    expect(result.current.active.pageParams).toEqual([0]);
    expect(result.current.active.isPlaceholderData).toBe(true);
  });

  it("clears retained rows on collapse so reopen failure is an initial error", async () => {
    let fail = false;
    state.listPage = async (input) => {
      if (fail) throw new Error("reopen failed");
      return pageResponse(input.group ?? "active", input.offset ?? 0);
    };
    const harness = createHarness();
    const { result, rerender } = renderHook(
      ({ expanded }) =>
        useProjectTaskListData({
          projectID: "project-1",
          gateReady: true,
          expanded: { active: expanded, backlog: false, done: false },
          anchors: { active: 0, backlog: 0, done: 0 },
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
          queryKeys.projectTaskGroup("project-1", "active", 0),
        ),
      ).toBeUndefined();
    });
    fail = true;
    rerender({ expanded: true });
    await waitFor(() => {
      expect(result.current.active.isError).toBe(true);
    });

    expect(result.current.active.tasks).toEqual([]);
    expect(result.current.active.isPlaceholderData).toBe(false);
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
          gateReady: true,
          expanded: { active: true, backlog: false, done: false },
          anchors: { active: 0, backlog: 0, done: 0 },
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
        return pageResponse(input.group ?? "active", input.offset ?? 0);
      }
      throw new Error("next page failed");
    };
    const edgeHarness = createHarness();
    const { result: edgeResult } = renderHook(
      () =>
        useProjectTaskListData({
          projectID: "project-1",
          gateReady: true,
          expanded: { active: true, backlog: false, done: false },
          anchors: { active: 0, backlog: 0, done: 0 },
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

  it("keeps retained rows when loading the previous page edge fails", async () => {
    let pageCall = 0;
    state.listPage = async (input) => {
      pageCall += 1;
      if (pageCall === 1) {
        return pageResponse(input.group ?? "active", input.offset ?? 50);
      }
      throw new Error("previous page failed");
    };
    const harness = createHarness();
    const { result } = renderHook(
      () =>
        useProjectTaskListData({
          projectID: "project-1",
          gateReady: true,
          expanded: { active: true, backlog: false, done: false },
          anchors: { active: 50, backlog: 0, done: 0 },
        }),
      { wrapper: ({ children }) => harness.render(children) },
    );
    await waitFor(() => {
      expect(result.current.active.tasks).toHaveLength(1);
    });
    await act(async () => {
      await result.current.active.fetchPreviousPage();
    });
    await waitFor(() => {
      expect(result.current.active.isFetchPreviousPageError).toBe(true);
    });
    expect(result.current.active.tasks).toHaveLength(1);
  });

  it("owns one typed Project subscription and refreshes only the affected roots", async () => {
    const harness = createHarness();
    const invalidations: (readonly unknown[])[] = [];
    const invalidateSpy = vi
      .spyOn(harness.queryClient, "invalidateQueries")
      .mockImplementation(async (...args) => {
        invalidations.push(args[0]?.queryKey ?? []);
      });
    const initialProps: { editorTaskID: string | null } = { editorTaskID: "task-1" };
    const { rerender, unmount } = renderHook(
      ({ editorTaskID }: { editorTaskID: string | null }) => {
        useProjectTaskListEvents({
          enabled: true,
          projectID: "project-1",
          labelEditorTaskID: editorTaskID,
        });
      },
      {
        initialProps,
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
      expect(invalidations).toContainEqual(queryKeys.projectLabels("project-1"));
    });
    expect(invalidations).toContainEqual(queryKeys.projectTaskListsRoot("project-1"));

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
      expect(invalidations).toContainEqual(queryKeys.taskLabels("task-1"));
    });
    expect(invalidations).toContainEqual(queryKeys.projectTaskListsRoot("project-1"));

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

    rerender({ editorTaskID: null });
    expect(state.handlers).toHaveLength(1);
    invalidations.length = 0;
    act(() => {
      state.handlers[0]?.onEvent(workflowEvent("label", "renamed", "label-1"));
    });
    await waitFor(() => {
      expect(invalidations).toContainEqual(queryKeys.projectTaskListsRoot("project-1"));
    });
    expect(invalidations).not.toContainEqual(queryKeys.projectLabels("project-1"));
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
    counts: { active, backlog: 1, done: 0 },
    generatedAt: 1,
  };
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
    workflowID:
      resource === "label"
        ? null
        : resource === "workflow"
          ? primaryEntityID
          : "workflow-1",
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return { promise, resolve };
}
