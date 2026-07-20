import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";

import type { ProjectLabelCatalog } from "@/api";
import { AppServicesProvider, BrowserStorageError, queryKeys } from "@/app-facade";
import { createTestServices } from "@/test-support/app-services";
import { installTestStorage } from "@/test-support/storage";
import {
  ProjectLabelsProvider,
  readPersistedLabelFilterState,
  useProjectLabelCatalog,
  useProjectLabelCatalogMutations,
  useProjectLabelFilter,
} from "./index";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";

describe("Project label data", () => {
  beforeEach(() => {
    installTestStorage("localStorage");
  });

  it("loads the complete Project catalog through the shared capability", async () => {
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityID, name: "Priority" }],
          },
        },
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const wrapper = ({ children }: Readonly<{ children: ReactNode }>) =>
      createElement(
        QueryClientProvider,
        { client: queryClient },
        createElement(AppServicesProvider, {
          services,
          children: createElement(ProjectLabelsProvider, {
            projectID: "project-1",
            children,
          }),
        }),
      );

    const view = renderHook(() => useProjectLabelCatalog(), { wrapper });

    await waitFor(() => {
      expect(view.result.current.data).toEqual({
        projectID: "project-1",
        labels: [{ id: priorityID, name: "Priority" }],
      });
    });
    expect(
      services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
    ).toHaveLength(1);
  });

  it("patches an authoritative create result before catalog reconciliation", async () => {
    const reconciliation = deferred<unknown>();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        handler: async (_params, callIndex) =>
          callIndex === 0
            ? {
                catalog: {
                  project_id: "project-1",
                  labels: [{ id: priorityID, name: "Priority" }],
                },
              }
            : reconciliation.promise,
      },
      {
        method: "workflow.project.label.create",
        result: {
          label: {
            id: "942495c2-5958-4959-8445-94046ad74fbd",
            name: "Alpha",
          },
        },
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const wrapper = projectLabelsWrapper(services, queryClient);
    const view = renderHook(
      () => ({
        catalog: useProjectLabelCatalog(),
        mutations: useProjectLabelCatalogMutations(),
      }),
      { wrapper },
    );
    await waitFor(() => {
      expect(view.result.current.catalog.data?.labels).toHaveLength(1);
    });

    await act(async () => {
      await view.result.current.mutations.create.mutateAsync("Alpha");
    });

    const expectedLabels = [
      {
        id: "942495c2-5958-4959-8445-94046ad74fbd",
        name: "Alpha",
      },
      { id: priorityID, name: "Priority" },
    ];
    expect(
      queryClient.getQueryData<ProjectLabelCatalog>(queryKeys.projectLabels("project-1"))?.labels,
    ).toEqual(expectedLabels);
    await waitFor(() => {
      expect(view.result.current.catalog.data?.labels).toEqual(expectedLabels);
    });
    expect(
      services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
    ).toHaveLength(2);

    reconciliation.resolve({
      catalog: {
        project_id: "project-1",
        labels: [
          {
            id: "942495c2-5958-4959-8445-94046ad74fbd",
            name: "Alpha",
          },
          { id: priorityID, name: "Priority" },
        ],
      },
    });
  });

  it("restores one Project filter across provider relaunches", async () => {
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityID, name: "Priority" }],
          },
        },
      },
    ]);
    const firstQueryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const { result: firstResult, unmount } = renderHook(() => useProjectLabelFilter(), {
      wrapper: projectLabelsWrapper(services, firstQueryClient),
    });
    await waitFor(() => {
      expect(firstResult.current.persistence.status).toBe("ready");
    });

    act(() => {
      firstResult.current.dispatch({
        type: "named.toggle",
        labelID: priorityID,
      });
    });
    await waitFor(() => {
      expect(firstResult.current.state.filter).toEqual({
        kind: "named",
        mode: "any",
        labelIDs: [priorityID],
      });
    });
    unmount();

    const secondQueryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const { result: secondResult } = renderHook(() => useProjectLabelFilter(), {
      wrapper: projectLabelsWrapper(services, secondQueryClient),
    });
    await waitFor(() => {
      expect(secondResult.current.state.filter).toEqual({
        kind: "named",
        mode: "any",
        labelIDs: [priorityID],
      });
    });
  });

  it("prunes a deleted label from the active and persisted Project filter", async () => {
    const reconciliation = deferred<unknown>();
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        handler: async (_params, callIndex) =>
          callIndex === 0
            ? {
                catalog: {
                  project_id: "project-1",
                  labels: [{ id: priorityID, name: "Priority" }],
                },
              }
            : reconciliation.promise,
      },
      {
        method: "workflow.project.label.delete",
        result: { label_id: priorityID },
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const view = renderHook(
      () => ({
        filter: useProjectLabelFilter(),
        mutations: useProjectLabelCatalogMutations(),
      }),
      { wrapper: projectLabelsWrapper(services, queryClient) },
    );
    await waitFor(() => {
      expect(view.result.current.filter.persistence.status).toBe("ready");
    });
    act(() => {
      view.result.current.filter.dispatch({
        type: "named.toggle",
        labelID: priorityID,
      });
    });
    await waitFor(() => {
      expect(view.result.current.filter.state.filter.kind).toBe("named");
    });

    await act(async () => {
      await view.result.current.mutations.delete.mutateAsync(priorityID);
    });

    expect(view.result.current.filter.state.filter).toEqual({ kind: "none" });
    const storageNamespace = services.storageNamespace;
    if (storageNamespace === null) {
      throw new Error("test services did not provide a storage namespace");
    }
    await waitFor(() => {
      expect(readPersistedLabelFilterState(storageNamespace, "project-1", [priorityID])).toEqual({
        ok: true,
        state: {
          filter: { kind: "none" },
          namedMode: "any",
        },
      });
    });

    reconciliation.resolve({
      catalog: {
        project_id: "project-1",
        labels: [],
      },
    });
  });

  it("surfaces browser storage failures through the Project filter controller", async () => {
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      get() {
        throw new DOMException("blocked", "SecurityError");
      },
    });
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityID, name: "Priority" }],
          },
        },
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const { result } = renderHook(() => useProjectLabelFilter(), {
      wrapper: projectLabelsWrapper(services, queryClient),
    });

    await waitFor(() => {
      expect(result.current.persistence.status).toBe("error");
    });
    if (result.current.persistence.status !== "error") {
      throw new Error("expected Project label filter persistence to fail");
    }
    expect(result.current.persistence.error).toBeInstanceOf(BrowserStorageError);
  });
});

function projectLabelsWrapper(services: ReturnType<typeof createTestServices>, queryClient: QueryClient) {
  return ({ children }: Readonly<{ children: ReactNode }>) =>
    createElement(
      QueryClientProvider,
      { client: queryClient },
      createElement(AppServicesProvider, {
        services,
        children: createElement(ProjectLabelsProvider, {
          projectID: "project-1",
          children,
        }),
      }),
    );
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
