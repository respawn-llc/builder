import { CancelledError, QueryClient, QueryObserver } from "@tanstack/react-query";
import { waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { ProjectLabelCatalog, WorkflowProjectEvent } from "@/api";
import { queryKeys } from "@/app-facade";
import { createProjectLabelEffects } from "./labelEventEffects";
import type { LabelFilterAction } from "./labelFilterState";

const alphaID = "38bf0da7-a3f7-4c15-bc5f-c8fca538e667";
const betaID = "942495c2-5958-4959-8445-94046ad74fbd";

describe("Project label event effects", () => {
  it("invalidates the exact retained catalog while only requesting active refetches", async () => {
    const queryClient = queryClientWithCatalog();
    const effects = createEffects(queryClient);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");

    effects.scheduleCatalogRefresh();

    await waitFor(() => {
      expect(invalidate).toHaveBeenCalledWith(
        {
          queryKey: queryKeys.projectLabels("project-1"),
          exact: true,
          refetchType: "active",
        },
        { throwOnError: true },
      );
    });
    expect(cachedQuery(queryClient, queryKeys.projectLabels("project-1")).state.isInvalidated).toBe(true);
  });

  it("reports active catalog refresh failures", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const onBackgroundError = vi.fn();
    const effects = createEffects(queryClient, { onBackgroundError });
    const failure = new Error("refresh failed");
    let refreshFails = false;
    const observer = new QueryObserver(queryClient, {
      queryKey: queryKeys.projectLabels("project-1"),
      queryFn: async () => {
        if (refreshFails) {
          throw failure;
        }
        return { projectID: "project-1", labels: [] };
      },
    });
    const unsubscribe = observer.subscribe(() => undefined);
    await waitFor(() => {
      expect(queryClient.getQueryData(queryKeys.projectLabels("project-1"))).toEqual({
        projectID: "project-1",
        labels: [],
      });
    });

    refreshFails = true;
    effects.scheduleCatalogRefresh();

    await waitFor(() => {
      expect(onBackgroundError).toHaveBeenCalledWith(failure);
    });
    unsubscribe();
  });

  it("does not report canceled catalog refreshes", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const onBackgroundError = vi.fn();
    const effects = createEffects(queryClient, { onBackgroundError });
    const invalidate = vi
      .spyOn(queryClient, "invalidateQueries")
      .mockRejectedValue(new CancelledError());

    effects.scheduleCatalogRefresh();

    await waitFor(() => {
      expect(invalidate).toHaveBeenCalled();
    });
    expect(onBackgroundError).not.toHaveBeenCalled();
  });

  it("reports ordinary batched refresh failures when another refresh is canceled", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const onBackgroundError = vi.fn();
    const effects = createEffects(queryClient, { onBackgroundError });
    const failure = new Error("membership refresh failed");
    vi.spyOn(queryClient, "invalidateQueries")
      .mockRejectedValueOnce(new CancelledError())
      .mockRejectedValueOnce(failure)
      .mockResolvedValue(undefined);

    effects.scheduleReorderRefresh();

    await waitFor(() => {
      expect(onBackgroundError).toHaveBeenCalledWith(failure);
    });
    expect(onBackgroundError).toHaveBeenCalledOnce();
  });

  it("reports active Task assignment refresh failures", async () => {
    const queryClient = queryClientWithMembership();
    const onBackgroundError = vi.fn();
    const effects = createEffects(queryClient, { onBackgroundError });
    const failure = new Error("assignment refresh failed");
    let refreshFails = false;
    const observer = new QueryObserver(queryClient, {
      queryKey: queryKeys.taskLabels("task-1"),
      queryFn: async () => {
        if (refreshFails) {
          throw failure;
        }
        return { taskID: "task-1", labelIDs: [] };
      },
    });
    const unsubscribe = observer.subscribe(() => undefined);
    await waitFor(() => {
      expect(queryClient.getQueryData(queryKeys.taskLabels("task-1"))).toEqual({
        taskID: "task-1",
        labelIDs: [],
      });
    });

    refreshFails = true;
    effects.scheduleTaskAssignmentRefresh("task-1");

    await waitFor(() => {
      expect(onBackgroundError).toHaveBeenCalledWith(failure);
    });
    unsubscribe();
  });

  it("refreshes catalog, board-card pages, and Task lists after reorder without board counts", async () => {
    const queryClient = queryClientWithMembership();
    const effects = createEffects(queryClient);

    effects.scheduleReorderRefresh();

    await waitFor(() => {
      expectInvalidated(queryClient, queryKeys.projectLabels("project-1"));
      expectInvalidated(queryClient, queryKeys.projectBoardNodeCardsRoot("project-1"));
      expectInvalidated(queryClient, queryKeys.projectTaskListsRoot("project-1"));
    });
    expectInvalidated(queryClient, queryKeys.projectBoardsRoot("project-1"), false);
  });

  it("prunes a deleted Label synchronously and invalidates every retained membership projection", async () => {
    const queryClient = queryClientWithMembership();
    queryClient.setQueryData<ProjectLabelCatalog>(queryKeys.projectLabels("project-1"), {
      projectID: "project-1",
      labels: [
        { id: alphaID, name: "Alpha" },
        { id: betaID, name: "Beta" },
      ],
    });
    queryClient.setQueryData(queryKeys.taskLabels("task-1"), {
      taskID: "task-1",
      labelIDs: [alphaID, betaID],
    });
    queryClient.setQueryData(queryKeys.task("task-1"), {
      id: "task-1",
      labelIDs: [alphaID, betaID],
    });
    const filterAction = vi.fn();
    const effects = createEffects(queryClient, { onFilterAction: filterAction });

    const consumed = effects.consumeProjectEvent(labelEvent("deleted", alphaID));

    expect(
      queryClient.getQueryData<ProjectLabelCatalog>(queryKeys.projectLabels("project-1"))?.labels,
    ).toEqual([{ id: betaID, name: "Beta" }]);
    expect(queryClient.getQueryData(queryKeys.taskLabels("task-1"))).toEqual({
      taskID: "task-1",
      labelIDs: [betaID],
    });
    expect(queryClient.getQueryData(queryKeys.task("task-1"))).toEqual({
      id: "task-1",
      labelIDs: [betaID],
    });
    expect(filterAction).toHaveBeenCalledWith({ type: "label.deleted", labelID: alphaID });
    await consumed;
    await waitFor(() => {
      expectInvalidated(queryClient, queryKeys.taskLabels("task-1"));
      expectInvalidated(queryClient, queryKeys.task("task-1"));
      expectInvalidated(queryClient, queryKeys.projectBoardsRoot("project-1"));
      expectInvalidated(queryClient, queryKeys.projectBoardNodeCardsRoot("project-1"));
      expectInvalidated(queryClient, queryKeys.projectTaskListsRoot("project-1"));
    });
  });

  it("maps label create, rename, and reorder events to their exact refresh sets", async () => {
    const queryClient = queryClientWithMembership();
    const effects = createEffects(queryClient);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");

    await effects.consumeProjectEvent(labelEvent("created", alphaID));
    await effects.consumeProjectEvent(labelEvent("renamed", alphaID));
    await effects.consumeProjectEvent(labelEvent("reordered", alphaID));

    await waitFor(() => {
      expect(invalidate).toHaveBeenCalledWith(
        {
          queryKey: queryKeys.projectLabels("project-1"),
          exact: true,
          refetchType: "active",
        },
        { throwOnError: true },
      );
    });
    expect(invalidate).toHaveBeenCalledWith(
      {
        queryKey: queryKeys.projectBoardNodeCardsRoot("project-1"),
        refetchType: "active",
      },
      { throwOnError: true },
    );
    expect(invalidate).toHaveBeenCalledWith(
      {
        queryKey: queryKeys.projectTaskListsRoot("project-1"),
        refetchType: "active",
      },
      { throwOnError: true },
    );
  });

  it("converges active catalog queries in separate Query Clients through the same Project event", async () => {
    let serverCatalog: ProjectLabelCatalog = {
      projectID: "project-1",
      labels: [{ id: alphaID, name: "Before" }],
    };
    const first = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const second = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const key = queryKeys.projectLabels("project-1");
    const firstObserver = new QueryObserver(first, {
      queryKey: key,
      queryFn: async () => serverCatalog,
    });
    const secondObserver = new QueryObserver(second, {
      queryKey: key,
      queryFn: async () => serverCatalog,
    });
    const unsubscribeFirst = firstObserver.subscribe(() => undefined);
    const unsubscribeSecond = secondObserver.subscribe(() => undefined);
    await waitFor(() => {
      expect(first.getQueryData(key)).toEqual(serverCatalog);
      expect(second.getQueryData(key)).toEqual(serverCatalog);
    });

    serverCatalog = {
      projectID: "project-1",
      labels: [{ id: alphaID, name: "After" }],
    };
    await Promise.all([
      createEffects(first).consumeProjectEvent(labelEvent("renamed", alphaID)),
      createEffects(second).consumeProjectEvent(labelEvent("renamed", alphaID)),
    ]);

    expect(first.getQueryData(key)).toEqual(serverCatalog);
    expect(second.getQueryData(key)).toEqual(serverCatalog);
    unsubscribeFirst();
    unsubscribeSecond();
  });

  it("settles event consumption only after its invalidations finish and propagates failure", async () => {
    const queryClient = queryClientWithMembership();
    const refresh = deferred<undefined>();
    vi.spyOn(queryClient, "invalidateQueries").mockReturnValue(refresh.promise);
    const effects = createEffects(queryClient);
    const consumed = effects.consumeProjectEvent(labelEvent("created", alphaID));
    const settled = vi.fn();
    void consumed.then(settled, settled);

    await Promise.resolve();
    expect(settled).not.toHaveBeenCalled();

    const failure = new Error("event refresh failed");
    refresh.reject(failure);
    await expect(consumed).rejects.toBe(failure);
    expect(settled).toHaveBeenCalledOnce();
  });

  it("invalidates exact assignment and Project membership without duplicating Task detail refresh", async () => {
    const queryClient = queryClientWithMembership();
    queryClient.setQueryData(queryKeys.taskLabels("task-1"), {
      taskID: "task-1",
      labelIDs: [alphaID],
    });
    queryClient.setQueryData(queryKeys.task("task-1"), { id: "task-1", labelIDs: [alphaID] });
    queryClient.setQueryData(queryKeys.taskLabels("task-2"), {
      taskID: "task-2",
      labelIDs: [betaID],
    });
    const effects = createEffects(queryClient);

    await effects.consumeProjectEvent(taskEvent("labels_changed", "task-1"));

    await waitFor(() => {
      expectInvalidated(queryClient, queryKeys.taskLabels("task-1"));
      expectInvalidated(queryClient, queryKeys.projectBoardsRoot("project-1"));
      expectInvalidated(queryClient, queryKeys.projectBoardNodeCardsRoot("project-1"));
      expectInvalidated(queryClient, queryKeys.projectTaskListsRoot("project-1"));
    });
    expectInvalidated(queryClient, queryKeys.task("task-1"), false);
    expectInvalidated(queryClient, queryKeys.taskLabels("task-2"), false);
  });

  it("removes deleted Task caches before scheduling membership refresh", async () => {
    const queryClient = queryClientWithMembership();
    queryClient.setQueryData(queryKeys.taskLabels("task-1"), {
      taskID: "task-1",
      labelIDs: [alphaID],
    });
    queryClient.setQueryData(queryKeys.task("task-1"), { id: "task-1", labelIDs: [alphaID] });
    const effects = createEffects(queryClient);

    const consumed = effects.consumeProjectEvent(taskEvent("deleted", "task-1"));

    expect(queryClient.getQueryData(queryKeys.taskLabels("task-1"))).toBeUndefined();
    expect(queryClient.getQueryData(queryKeys.task("task-1"))).toBeUndefined();
    await consumed;
    await waitFor(() => {
      expectInvalidated(queryClient, queryKeys.projectBoardsRoot("project-1"));
    });
  });

  it("refreshes catalog, all retained assignments, and Project membership at subscription boundaries", async () => {
    const queryClient = queryClientWithMembership();
    queryClient.setQueryData(queryKeys.taskLabels("task-1"), {
      taskID: "task-1",
      labelIDs: [alphaID],
    });
    queryClient.setQueryData(queryKeys.taskLabels("task-2"), {
      taskID: "task-2",
      labelIDs: [betaID],
    });
    const effects = createEffects(queryClient);

    await effects.refreshAfterSubscriptionBoundary();

    await waitFor(() => {
      expectInvalidated(queryClient, queryKeys.projectLabels("project-1"));
      expectInvalidated(queryClient, queryKeys.taskLabels("task-1"));
      expectInvalidated(queryClient, queryKeys.taskLabels("task-2"));
      expectInvalidated(queryClient, queryKeys.projectBoardsRoot("project-1"));
      expectInvalidated(queryClient, queryKeys.projectBoardNodeCardsRoot("project-1"));
      expectInvalidated(queryClient, queryKeys.projectTaskListsRoot("project-1"));
    });
  });

  it("keeps an optimistic catalog reorder while refreshing exact assignment and membership", async () => {
    const queryClient = queryClientWithMembership();
    const projectedCatalog: ProjectLabelCatalog = {
      projectID: "project-1",
      labels: [
        { id: betaID, name: "Beta" },
        { id: alphaID, name: "Alpha" },
      ],
    };
    queryClient.setQueryData(queryKeys.projectLabels("project-1"), projectedCatalog);
    queryClient.setQueryData(queryKeys.taskLabels("task-1"), {
      taskID: "task-1",
      labelIDs: [alphaID],
    });
    queryClient.setQueryData(queryKeys.task("task-1"), { id: "task-1", labelIDs: [alphaID] });
    queryClient.setQueryData(queryKeys.taskLabels("task-2"), {
      taskID: "task-2",
      labelIDs: [betaID],
    });
    const effects = createEffects(queryClient);

    effects.scheduleTaskAssignmentRefresh("task-1");

    await waitFor(() => {
      expectInvalidated(queryClient, queryKeys.taskLabels("task-1"));
      expectInvalidated(queryClient, queryKeys.projectBoardsRoot("project-1"));
      expectInvalidated(queryClient, queryKeys.projectBoardNodeCardsRoot("project-1"));
      expectInvalidated(queryClient, queryKeys.projectTaskListsRoot("project-1"));
    });
    expect(queryClient.getQueryData(queryKeys.projectLabels("project-1"))).toEqual(projectedCatalog);
    expectInvalidated(queryClient, queryKeys.projectLabels("project-1"), false);
    expectInvalidated(queryClient, queryKeys.task("task-1"), false);
    expectInvalidated(queryClient, queryKeys.taskLabels("task-2"), false);
  });

  it("ignores events for another Project", async () => {
    const queryClient = queryClientWithMembership();
    const effects = createEffects(queryClient);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");

    await effects.consumeProjectEvent({ ...labelEvent("created", alphaID), projectID: "project-2" });

    expect(invalidate).not.toHaveBeenCalled();
  });
});

function createEffects(
  queryClient: QueryClient,
  overrides: Readonly<{
    onBackgroundError?: (error: unknown) => void;
    onFilterAction?: (action: LabelFilterAction) => void;
  }> = {},
) {
  return createProjectLabelEffects({
    onBackgroundError: overrides.onBackgroundError ?? vi.fn(),
    onFilterAction: overrides.onFilterAction,
    projectID: "project-1",
    queryClient,
  });
}

function queryClientWithCatalog(): QueryClient {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(queryKeys.projectLabels("project-1"), {
    projectID: "project-1",
    labels: [],
  });
  return queryClient;
}

function queryClientWithMembership(): QueryClient {
  const queryClient = queryClientWithCatalog();
  queryClient.setQueryData(queryKeys.projectBoardsRoot("project-1"), { projectID: "project-1" });
  queryClient.setQueryData(queryKeys.projectBoardNodeCardsRoot("project-1"), { pages: [] });
  queryClient.setQueryData(queryKeys.projectTaskListsRoot("project-1"), { tasks: [] });
  queryClient.setQueryData(queryKeys.projectBoardsRoot("project-2"), { projectID: "project-2" });
  return queryClient;
}

function cachedQuery(queryClient: QueryClient, queryKey: readonly unknown[]) {
  const query = queryClient.getQueryCache().find({ queryKey, exact: true });
  if (query === undefined) {
    throw new Error(`expected retained query ${JSON.stringify(queryKey)}`);
  }
  return query;
}

function expectInvalidated(queryClient: QueryClient, queryKey: readonly unknown[], expected = true): void {
  expect(cachedQuery(queryClient, queryKey).state.isInvalidated).toBe(expected);
}

function labelEvent(
  action: "created" | "renamed" | "deleted" | "reordered",
  labelID: string,
): WorkflowProjectEvent {
  return {
    action,
    occurredAtUnixMs: 1,
    primaryEntityID: labelID,
    projectID: "project-1",
    relatedIDs: [],
    resource: "label",
    workflowID: null,
  };
}

function taskEvent(action: "deleted" | "labels_changed", taskID: string): WorkflowProjectEvent {
  return {
    action,
    occurredAtUnixMs: 1,
    primaryEntityID: taskID,
    projectID: "project-1",
    relatedIDs: [],
    resource: "task",
    workflowID: "11111111-1111-4111-8111-111111111111",
  };
}

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  reject(error: unknown): void;
  resolve(value: T): void;
}> {
  let reject: ((error: unknown) => void) | undefined;
  let resolve: ((value: T) => void) | undefined;
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve;
    reject = nextReject;
  });
  return {
    promise,
    reject(error) {
      reject?.(error);
    },
    resolve(value) {
      resolve?.(value);
    },
  };
}
