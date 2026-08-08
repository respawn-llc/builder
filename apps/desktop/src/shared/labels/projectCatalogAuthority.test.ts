import { QueryClient } from "@tanstack/react-query";
import { waitFor } from "@testing-library/react";

import type { ProjectLabelCatalog } from "@/api";
import { queryKeys } from "@/app-facade";
import { createProjectCatalogAuthority } from "./index";

const labelID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";

describe("Project catalog authority", () => {
  it("keeps an authoritative local rename ahead of an older catalog read", async () => {
    const oldRead = deferred<ProjectLabelCatalog>();
    const reconciliation = deferred<ProjectLabelCatalog>();
    let readCount = 0;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => {
        readCount += 1;
        return readCount === 1 ? oldRead.promise : reconciliation.promise;
      },
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(key, {
      projectID: "project-1",
      labels: [{ id: labelID, name: "Before" }],
    });

    const initialRead = queryClient.fetchQuery({
      queryKey: key,
      queryFn: async ({ signal }) => authority.read(signal),
      staleTime: 0,
    });
    await waitFor(() => {
      expect(readCount).toBe(1);
    });

    authority.applyRename({ id: labelID, name: "After" });

    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
      { id: labelID, name: "After" },
    ]);
    oldRead.resolve({
      projectID: "project-1",
      labels: [{ id: labelID, name: "Before" }],
    });
    await initialRead.catch(() => undefined);
    await waitFor(() => {
      expect(readCount).toBe(2);
    });
    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
      { id: labelID, name: "After" },
    ]);

    reconciliation.resolve({
      projectID: "project-1",
      labels: [{ id: labelID, name: "After" }],
    });
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
        { id: labelID, name: "After" },
      ]);
    });
  });

  it("keeps a deleted label pruned until a current-generation read is accepted", async () => {
    const oldRead = deferred<ProjectLabelCatalog>();
    const reconciliation = deferred<ProjectLabelCatalog>();
    let readCount = 0;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => {
        readCount += 1;
        return readCount === 1 ? oldRead.promise : reconciliation.promise;
      },
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(key, {
      projectID: "project-1",
      labels: [{ id: labelID, name: "Delete me" }],
    });
    const initialRead = queryClient.fetchQuery({
      queryKey: key,
      queryFn: async ({ signal }) => authority.read(signal),
      staleTime: 0,
    });
    await waitFor(() => {
      expect(readCount).toBe(1);
    });

    const staleGeneration = authority.supersedeReads();
    authority.applyDelete(labelID);
    authority.installCatalog({ projectID: "project-1", labels: [{ id: labelID, name: "Before" }] }, staleGeneration);
    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([]);

    oldRead.resolve({
      projectID: "project-1",
      labels: [{ id: labelID, name: "Delete me" }],
    });
    await initialRead.catch(() => undefined);
    await waitFor(() => {
      expect(readCount).toBe(2);
    });
    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([]);

    reconciliation.resolve({
      projectID: "project-1",
      labels: [],
    });
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([]);
    });
  });

  it("coalesces repeated renames and event echoes behind one refresh bit", async () => {
    const oldRead = deferred<ProjectLabelCatalog>();
    const reconciliation = deferred<ProjectLabelCatalog>();
    let readCount = 0;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => {
        readCount += 1;
        return readCount === 1 ? oldRead.promise : reconciliation.promise;
      },
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(key, {
      projectID: "project-1",
      labels: [{ id: labelID, name: "Before" }],
    });
    const initialRead = queryClient.fetchQuery({
      queryKey: key,
      queryFn: async ({ signal }) => authority.read(signal),
      staleTime: 0,
    });
    await waitFor(() => {
      expect(readCount).toBe(1);
    });

    authority.applyRename({ id: labelID, name: "Middle" });
    authority.applyRename({ id: labelID, name: "Latest" });
    authority.requestRefresh();

    expect(readCount).toBe(1);
    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
      { id: labelID, name: "Latest" },
    ]);

    oldRead.resolve({
      projectID: "project-1",
      labels: [{ id: labelID, name: "Before" }],
    });
    await initialRead.catch(() => undefined);
    await waitFor(() => {
      expect(readCount).toBe(2);
    });
    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
      { id: labelID, name: "Latest" },
    ]);

    reconciliation.resolve({
      projectID: "project-1",
      labels: [{ id: labelID, name: "Latest" }],
    });
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
        { id: labelID, name: "Latest" },
      ]);
    });
    expect(readCount).toBe(2);
  });

  it("lets a current-generation read clear delete tombstones for later catalog data", async () => {
    const deletedCatalog = deferred<ProjectLabelCatalog>();
    const laterCatalog = deferred<ProjectLabelCatalog>();
    let readCount = 0;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => {
        readCount += 1;
        return readCount === 1 ? deletedCatalog.promise : laterCatalog.promise;
      },
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(key, {
      projectID: "project-1",
      labels: [{ id: labelID, name: "Before" }],
    });

    authority.applyDelete(labelID);
    await waitFor(() => {
      expect(readCount).toBe(1);
    });
    deletedCatalog.resolve({
      projectID: "project-1",
      labels: [],
    });
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([]);
    });

    const laterRead = queryClient.fetchQuery({
      queryKey: key,
      queryFn: async ({ signal }) => authority.read(signal),
      staleTime: 0,
    });
    await waitFor(() => {
      expect(readCount).toBe(2);
    });
    laterCatalog.resolve({
      projectID: "project-1",
      labels: [{ id: labelID, name: "Recreated" }],
    });
    await laterRead;

    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
      { id: labelID, name: "Recreated" },
    ]);
  });
});

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
