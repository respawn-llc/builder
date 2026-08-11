import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ProjectLabel, ProjectLabelCatalog } from "@/api";
import { queryKeys } from "@/app-facade";
import type { ProjectLabelDataContextValue } from "./projectLabelContext";
import { ProjectLabelDataContext } from "./projectLabelContext";
import type { ProjectLabelEffects } from "./labelEventEffects";
import { createLabelFilterState, type LabelFilterAction } from "./labelFilterState";
import { useProjectLabelCatalogMutations } from "./projectLabelHooks";

const api = vi.hoisted(() => ({
  createProjectLabel: vi.fn(),
  deleteProjectLabel: vi.fn(),
  renameProjectLabel: vi.fn(),
  reorderProjectLabels: vi.fn(),
}));

vi.mock("@/app-facade", () => ({
  queryKeys: {
    allBoardNodeCards: ["board-node-cards"],
    allTaskLabels: ["task-labels"],
    allTaskLists: ["task-list"],
    allTasks: ["task"],
    projectLabels: (projectID: string) => ["project-labels", projectID],
    task: (taskID: string) => ["task", taskID],
    taskLabels: (taskID: string) => ["task-labels", taskID],
  },
  useAppServices: () => ({ api }),
}));

const alphaID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const betaID = "b74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const original: ProjectLabelCatalog = {
  projectID: "project-1",
  labels: [
    { id: alphaID, name: "Alpha" },
    { id: betaID, name: "Beta" },
  ],
};

describe("project label mutations", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("cancels the exact catalog, patches create and rename responses, then schedules refreshes", async () => {
    const created: ProjectLabel = {
      id: "11111111-1111-4111-8111-111111111111",
      name: "Created",
    };
    const renamed: ProjectLabel = { id: betaID, name: "Renamed" };
    api.createProjectLabel.mockResolvedValueOnce(created);
    api.renameProjectLabel.mockResolvedValueOnce(renamed);
    const view = renderMutations(original);
    const cancel = vi.spyOn(view.queryClient, "cancelQueries");

    await act(async () => {
      await view.result.current.create.mutateAsync(created.name);
      await view.result.current.rename.mutateAsync({ labelID: betaID, name: renamed.name });
    });

    expect(cancel).toHaveBeenCalledTimes(2);
    expect(cancel).toHaveBeenCalledWith(
      { queryKey: queryKeys.projectLabels("project-1"), exact: true },
      { revert: false, silent: true },
    );
    expect(catalog(view.queryClient).labels).toEqual([created, original.labels[0], renamed]);
    expect(view.effects.scheduleCatalogRefresh).toHaveBeenCalledTimes(2);
  });

  it("cancels and synchronously prunes a deleted label from retained label projections", async () => {
    api.deleteProjectLabel.mockResolvedValueOnce(alphaID);
    const view = renderMutations(original);
    view.queryClient.setQueryData(queryKeys.taskLabels("task-1"), {
      taskID: "task-1",
      labelIDs: [alphaID, betaID],
    });
    view.queryClient.setQueryData(queryKeys.task("task-1"), {
      id: "task-1",
      labelIDs: [alphaID, betaID],
    });

    await act(async () => {
      await view.result.current.delete.mutateAsync(alphaID);
    });

    expect(catalog(view.queryClient).labels).toEqual([original.labels[1]]);
    expect(view.queryClient.getQueryData(queryKeys.taskLabels("task-1"))).toEqual({
      taskID: "task-1",
      labelIDs: [betaID],
    });
    expect(view.queryClient.getQueryData(queryKeys.task("task-1"))).toEqual({
      id: "task-1",
      labelIDs: [betaID],
    });
    expect(view.filterDispatch).toHaveBeenCalledWith({
      type: "label.deleted",
      labelID: alphaID,
    });
    expect(view.effects.scheduleDeleteRefresh).toHaveBeenCalledOnce();
  });

  it("projects an exact reorder, adopts the validated server catalog, and schedules dependent refreshes", async () => {
    const response = deferred<ProjectLabelCatalog>();
    api.reorderProjectLabels.mockReturnValueOnce(response.promise);
    const view = renderMutations(original);
    const reversedIDs = [betaID, alphaID];
    let mutation!: Promise<ProjectLabelCatalog>;

    act(() => {
      mutation = view.result.current.reorder.mutateAsync(reversedIDs);
    });
    await waitFor(() => {
      expect(catalog(view.queryClient).labels.map((label) => label.id)).toEqual(reversedIDs);
    });
    const authoritative: ProjectLabelCatalog = {
      projectID: "project-1",
      labels: [
        { id: betaID, name: "Server Beta" },
        { id: alphaID, name: "Server Alpha" },
      ],
    };
    response.resolve(authoritative);
    await act(async () => {
      await mutation;
    });

    expect(catalog(view.queryClient)).toEqual(authoritative);
    expect(view.effects.scheduleReorderRefresh).toHaveBeenCalledOnce();
  });

  it("restores a failed reorder only while its own projection is still installed", async () => {
    const failure = deferred<ProjectLabelCatalog>();
    api.reorderProjectLabels.mockReturnValueOnce(failure.promise);
    const view = renderMutations(original);
    const mutation = view.result.current.reorder.mutateAsync([betaID, alphaID]);
    await waitFor(() => {
      expect(catalog(view.queryClient).labels[0]?.id).toBe(betaID);
    });

    const newerCatalog: ProjectLabelCatalog = {
      projectID: "project-1",
      labels: [
        { id: alphaID, name: "Newer" },
        { id: betaID, name: "Beta" },
      ],
    };
    view.queryClient.setQueryData(queryKeys.projectLabels("project-1"), newerCatalog);
    failure.reject(new Error("reorder failed"));
    await act(async () => {
      await expect(mutation).rejects.toThrow("reorder failed");
    });

    expect(catalog(view.queryClient)).toEqual(newerCatalog);
    expect(view.effects.scheduleCatalogRefresh).toHaveBeenCalledOnce();
    expect(view.effects.scheduleReorderRefresh).not.toHaveBeenCalled();
  });

  it("restores its projection on failure and rejects non-permutations before the request", async () => {
    api.reorderProjectLabels.mockRejectedValueOnce(new Error("reorder failed"));
    const view = renderMutations(original);

    await act(async () => {
      await expect(view.result.current.reorder.mutateAsync([betaID, alphaID])).rejects.toThrow(
        "reorder failed",
      );
    });
    expect(catalog(view.queryClient)).toEqual(original);

    await act(async () => {
      await expect(view.result.current.reorder.mutateAsync([alphaID, alphaID])).rejects.toThrow(
        "unknown or duplicate",
      );
    });
    expect(api.reorderProjectLabels).toHaveBeenCalledTimes(1);
    expect(catalog(view.queryClient)).toEqual(original);
  });

  it("rejects another Project's reorder response and restores the projected catalog", async () => {
    api.reorderProjectLabels.mockResolvedValueOnce({
      projectID: "project-2",
      labels: [...original.labels].reverse(),
    });
    const view = renderMutations(original);

    await act(async () => {
      await expect(view.result.current.reorder.mutateAsync([betaID, alphaID])).rejects.toThrow(
        "returned project-2 while serving project-1",
      );
    });

    expect(catalog(view.queryClient)).toEqual(original);
    expect(view.effects.scheduleCatalogRefresh).toHaveBeenCalledOnce();
    expect(view.effects.scheduleReorderRefresh).not.toHaveBeenCalled();
  });
});

function renderMutations(initialCatalog: ProjectLabelCatalog) {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  queryClient.setQueryData(queryKeys.projectLabels(initialCatalog.projectID), initialCatalog);
  const effects: ProjectLabelEffects = {
    consumeProjectEvent: vi.fn(),
    refreshAfterSubscriptionBoundary: vi.fn(),
    scheduleCatalogRefresh: vi.fn(),
    scheduleDeleteRefresh: vi.fn(),
    scheduleMembershipRefresh: vi.fn(),
    scheduleReorderRefresh: vi.fn(),
    scheduleTaskAssignmentRefresh: vi.fn(),
  };
  const filterDispatch = vi.fn<(action: LabelFilterAction) => void>();
  const result = renderHook(() => useProjectLabelCatalogMutations(), {
    wrapper({ children }: Readonly<{ children: ReactNode }>) {
      return createElement(
        QueryClientProvider,
        { client: queryClient },
        createElement(ContextProvider, {
          catalog: initialCatalog,
          children,
          effects,
          filterDispatch,
        }),
      );
    },
  }).result;
  return { effects, filterDispatch, queryClient, result };
}

function ContextProvider({
  catalog: initialCatalog,
  children,
  effects,
  filterDispatch,
}: Readonly<{
  catalog: ProjectLabelCatalog;
  children: ReactNode;
  effects: ProjectLabelEffects;
  filterDispatch: (action: LabelFilterAction) => void;
}>) {
  const catalogQuery = useQuery({
    queryKey: queryKeys.projectLabels(initialCatalog.projectID),
    queryFn: async () => initialCatalog,
    enabled: false,
  });
  const value: ProjectLabelDataContextValue = {
    catalog: catalogQuery,
    effects,
    filter: {
      dispatch: filterDispatch,
      persistence: { status: "ready" },
      state: createLabelFilterState(),
    },
    projectID: initialCatalog.projectID,
  };
  return createElement(ProjectLabelDataContext.Provider, { value }, children);
}

function catalog(queryClient: QueryClient): ProjectLabelCatalog {
  const value = queryClient.getQueryData<ProjectLabelCatalog>(queryKeys.projectLabels("project-1"));
  if (value === undefined) {
    throw new Error("expected Project label catalog");
  }
  return value;
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
