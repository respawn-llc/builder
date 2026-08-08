import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { waitFor } from "@testing-library/react";
import { vi } from "vitest";

import type { ProjectLabelCatalog, WorkflowProjectEvent } from "@/api";
import { queryKeys } from "@/app-facade";
import { createProjectCatalogAuthority, createProjectLabelEffects } from "./index";

const alphaID = "38bf0da7-a3f7-4c15-bc5f-c8fca538e667";
const betaID = "942495c2-5958-4959-8445-94046ad74fbd";

describe("Project label event effects", () => {
  it("retains an authoritative local create across repeated event echoes and an older catalog read", async () => {
    const staleRead = deferred<ProjectLabelCatalog>();
    const currentRead = deferred<ProjectLabelCatalog>();
    let readCount = 0;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => {
        readCount += 1;
        return readCount === 1 ? staleRead.promise : currentRead.promise;
      },
    });
    const effects = createProjectLabelEffects({
      authority,
      onBackgroundError: vi.fn(),
      projectID: "project-1",
      queryClient,
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(key, {
      projectID: "project-1",
      labels: [],
    });

    await effects.applyLocalCreate({ id: alphaID, name: "Alpha" });
    const createdEvent = effects.consumeProjectEvent(labelEvent("created", alphaID));
    const renamedEvent = effects.consumeProjectEvent(labelEvent("renamed", alphaID));

    expect(readCount).toBe(1);
    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
      { id: alphaID, name: "Alpha" },
    ]);

    staleRead.resolve({
      projectID: "project-1",
      labels: [],
    });
    await waitFor(() => {
      expect(readCount).toBe(2);
    });
    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
      { id: alphaID, name: "Alpha" },
    ]);

    currentRead.resolve({
      projectID: "project-1",
      labels: [{ id: alphaID, name: "Alpha" }],
    });
    await Promise.all([createdEvent, renamedEvent]);
  });

  it("refreshes the catalog after a Project label reorder event", async () => {
    const refreshed = deferred<ProjectLabelCatalog>();
    let reads = 0;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => {
        reads += 1;
        return refreshed.promise;
      },
    });
    const effects = createProjectLabelEffects({
      authority,
      onBackgroundError: vi.fn(),
      projectID: "project-1",
      queryClient,
    });

    const reorderEvent = effects.consumeProjectEvent(labelEvent("reordered", alphaID));

    await waitFor(() => {
      expect(reads).toBe(1);
    });
    refreshed.resolve({
      projectID: "project-1",
      labels: [
        { id: betaID, name: "Beta" },
        { id: alphaID, name: "Alpha" },
      ],
    });
    await reorderEvent;
  });

  it("invalidates active board card queries after a committed local reorder without refreshing counts", async () => {
    const queryClient = new QueryClient();
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => ({ projectID: "project-1", labels: [] }),
    });
    const effects = createProjectLabelEffects({
      authority,
      onBackgroundError: vi.fn(),
      projectID: "project-1",
      queryClient,
    });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const catalog = {
      projectID: "project-1",
      labels: [{ id: alphaID, name: "Alpha" }],
    };

    await effects.applyLocalReorder(catalog, authority.supersedeReads());

    expect(invalidate).toHaveBeenCalledWith({
      queryKey: queryKeys.projectBoardNodeCardsRoot("project-1"),
      refetchType: "active",
    });
    expect(invalidate).not.toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: queryKeys.projectBoardsRoot("project-1") }),
    );
    expect(invalidate).not.toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: queryKeys.projectTaskListsRoot("project-1") }),
    );
  });

  it("settles a local reorder before board refetch completion and reports refresh failures", async () => {
    const queryClient = new QueryClient();
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => ({ projectID: "project-1", labels: [] }),
    });
    const refresh = deferred<undefined>();
    const reportBackgroundError = vi.fn();
    vi.spyOn(queryClient, "invalidateQueries").mockImplementation(async () => refresh.promise);
    const effects = createProjectLabelEffects({
      authority,
      onBackgroundError: reportBackgroundError,
      projectID: "project-1",
      queryClient,
    });
    const catalog = {
      projectID: "project-1",
      labels: [{ id: alphaID, name: "Alpha" }],
    };

    await effects.applyLocalReorder(catalog, authority.supersedeReads());
    expect(queryClient.getQueryData<ProjectLabelCatalog>(queryKeys.projectLabels("project-1"))).toEqual(
      catalog,
    );
    expect(reportBackgroundError).not.toHaveBeenCalled();

    const refreshFailure = new Error("board refresh failed");
    vi.spyOn(queryClient, "invalidateQueries").mockRejectedValueOnce(refreshFailure);
    await effects.applyLocalReorder(catalog, authority.supersedeReads());
    await waitFor(() => {
      expect(reportBackgroundError).toHaveBeenCalledWith(refreshFailure);
    });
    refresh.resolve(undefined);
  });

  it("invalidates active board card queries after a subscribed reorder event", async () => {
    const queryClient = new QueryClient();
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => ({ projectID: "project-1", labels: [] }),
    });
    const effects = createProjectLabelEffects({
      authority,
      onBackgroundError: vi.fn(),
      projectID: "project-1",
      queryClient,
    });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");

    await effects.consumeProjectEvent(labelEvent("reordered", alphaID));

    expect(invalidate).toHaveBeenCalledWith({
      queryKey: queryKeys.projectBoardNodeCardsRoot("project-1"),
      refetchType: "active",
    });
    expect(invalidate).not.toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: queryKeys.projectBoardsRoot("project-1") }),
    );
    expect(invalidate).not.toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: queryKeys.projectTaskListsRoot("project-1") }),
    );
  });

  it("keeps local delete and its event echo pruned before membership refresh", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => pendingCatalog.promise,
    });
    const filterActions: unknown[] = [];
    const membershipEffects: unknown[] = [];
    const effects = createProjectLabelEffects({
      authority,
      onBackgroundError: vi.fn(),
      onFilterAction: (action) => {
        filterActions.push(action);
      },
      onMembershipRefresh: (effect) => {
        membershipEffects.push(effect);
      },
      projectID: "project-1",
      queryClient,
    });
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
    queryClient.setQueryData(queryKeys.taskLabels("task-2"), {
      taskID: "task-2",
      labelIDs: [betaID],
    });

    await effects.applyLocalDelete(alphaID);
    await effects.consumeProjectEvent(labelEvent("deleted", alphaID));

    expect(
      queryClient.getQueryData<ProjectLabelCatalog>(queryKeys.projectLabels("project-1"))?.labels,
    ).toEqual([{ id: betaID, name: "Beta" }]);
    expect(queryClient.getQueryData(queryKeys.taskLabels("task-1"))).toEqual({
      taskID: "task-1",
      labelIDs: [betaID],
    });
    expect(queryClient.getQueryData(queryKeys.taskLabels("task-2"))).toEqual({
      taskID: "task-2",
      labelIDs: [betaID],
    });
    expect(filterActions).toEqual([
      { type: "label.deleted", labelID: alphaID },
      { type: "label.deleted", labelID: alphaID },
    ]);
    expect(membershipEffects).toEqual([
      {
        kind: "catalog.deleted",
        labelID: alphaID,
        projectID: "project-1",
      },
      {
        kind: "catalog.deleted",
        labelID: alphaID,
        projectID: "project-1",
      },
    ]);
  });

  it("refreshes membership reads without invalidating the removed assignment read", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => pendingCatalog.promise,
    });
    const membershipEffects: unknown[] = [];
    const effects = createProjectLabelEffects({
      authority,
      onBackgroundError: vi.fn(),
      onMembershipRefresh: (effect) => {
        membershipEffects.push(effect);
      },
      projectID: "project-1",
      queryClient,
    });
    const assignmentKey = queryKeys.taskLabels("task-1");
    const boardKey = queryKeys.board("project-1", "11111111-1111-4111-8111-111111111111", { kind: "none" });
    const unrelatedBoardKey = queryKeys.board("project-2", "11111111-1111-4111-8111-111111111111", {
      kind: "none",
    });
    const taskListKey = queryKeys.projectTaskListsRoot("project-1");
    const unrelatedTaskListKey = queryKeys.projectTaskListsRoot("project-2");
    queryClient.setQueryData(assignmentKey, { taskID: "task-1", labelIDs: [alphaID] });
    queryClient.setQueryData(boardKey, { projectID: "project-1" });
    queryClient.setQueryData(unrelatedBoardKey, { projectID: "project-2" });
    queryClient.setQueryData(taskListKey, { tasks: [] });
    queryClient.setQueryData(unrelatedTaskListKey, { tasks: [] });

    await effects.consumeProjectEvent(taskEvent("labels_changed", "task-1"));

    expect(
      queryClient.getQueryCache().find({ queryKey: assignmentKey, exact: true })?.state.isInvalidated,
    ).toBe(false);
    expect(queryClient.getQueryCache().find({ queryKey: boardKey, exact: true })?.state.isInvalidated).toBe(
      true,
    );
    expect(
      queryClient.getQueryCache().find({ queryKey: taskListKey, exact: true })?.state.isInvalidated,
    ).toBe(true);
    expect(
      queryClient.getQueryCache().find({ queryKey: unrelatedBoardKey, exact: true })?.state.isInvalidated,
    ).toBe(false);
    expect(
      queryClient.getQueryCache().find({ queryKey: unrelatedTaskListKey, exact: true })?.state.isInvalidated,
    ).toBe(false);
    expect(membershipEffects).toEqual([
      {
        kind: "task.labels_changed",
        projectID: "project-1",
        taskID: "task-1",
        workflowID: "11111111-1111-4111-8111-111111111111",
      },
    ]);

    await effects.consumeProjectEvent(taskEvent("updated", "task-2"));
    expect(membershipEffects).toHaveLength(1);
  });

  it("lets the host close request admission before broad membership invalidation refetches", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => pendingCatalog.promise,
    });
    let admissionClosed = false;
    let admittedTransportCalls = 0;
    const boardKey = queryKeys.board("project-1", "11111111-1111-4111-8111-111111111111", { kind: "none" });
    const observer = new QueryObserver(queryClient, {
      queryKey: boardKey,
      queryFn: async () => {
        if (!admissionClosed) {
          admittedTransportCalls += 1;
        }
        return { projectID: "project-1" };
      },
    });
    const unsubscribe = observer.subscribe(() => undefined);
    await observer.refetch();
    admittedTransportCalls = 0;
    const effects = createProjectLabelEffects({
      authority,
      onBackgroundError: vi.fn(),
      onMembershipRefresh: () => {
        admissionClosed = true;
      },
      projectID: "project-1",
      queryClient,
    });

    await effects.consumeProjectEvent(taskEvent("labels_changed", "task-1"));

    expect(admittedTransportCalls).toBe(0);
    unsubscribe();
  });

  it("removes a deleted task before refreshing membership", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => pendingCatalog.promise,
    });
    const membershipEffects: unknown[] = [];
    const effects = createProjectLabelEffects({
      authority,
      onBackgroundError: vi.fn(),
      onMembershipRefresh: (effect) => {
        membershipEffects.push(effect);
      },
      projectID: "project-1",
      queryClient,
    });
    queryClient.setQueryData(queryKeys.task("task-1"), { id: "task-1" });
    queryClient.setQueryData(queryKeys.taskLabels("task-1"), {
      taskID: "task-1",
      labelIDs: [alphaID],
    });

    await effects.consumeProjectEvent(taskEvent("deleted", "task-1"));

    expect(queryClient.getQueryData(queryKeys.task("task-1"))).toBeUndefined();
    expect(queryClient.getQueryData(queryKeys.taskLabels("task-1"))).toBeUndefined();
    expect(membershipEffects).toEqual([
      {
        kind: "task.deleted",
        projectID: "project-1",
        taskID: "task-1",
        workflowID: "11111111-1111-4111-8111-111111111111",
      },
    ]);
  });
});

describe("Project catalog authority projections", () => {
  it("prepends creates, replaces renames in place, and preserves delete survivor order", () => {
    const queryClient = new QueryClient();
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => ({ projectID: "project-1", labels: [] }),
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(key, {
      projectID: "project-1",
      labels: [
        { id: alphaID, name: "First" },
        { id: betaID, name: "Second" },
      ],
    });

    authority.applyCreate({ id: "11111111-1111-4111-8111-111111111111", name: "New" });
    authority.applyRename({ id: betaID, name: "Renamed" });
    authority.applyDelete(alphaID);

    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)).toEqual({
      projectID: "project-1",
      labels: [
        { id: "11111111-1111-4111-8111-111111111111", name: "New" },
        { id: betaID, name: "Renamed" },
      ],
    });
  });
});

const pendingCatalog = deferred<ProjectLabelCatalog>();

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

function taskEvent(action: "deleted" | "labels_changed" | "updated", taskID: string): WorkflowProjectEvent {
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
  resolve(value: T): void;
}> {
  let resolve: ((value: T) => void) | null = null;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return {
    promise,
    resolve(value: T): void {
      resolve?.(value);
    },
  };
}
