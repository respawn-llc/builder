import { QueryClient } from "@tanstack/react-query";
import { waitFor } from "@testing-library/react";

import type { ProjectLabelCatalog } from "@/api";
import { queryKeys } from "@/app-facade";
import { createProjectCatalogAuthority, selectOrderedProjectLabels } from "./index";

const labelID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const secondLabelID = "942495c2-5958-4959-8445-94046ad74fbd";
const thirdLabelID = "11111111-1111-4111-8111-111111111111";
const pendingCatalog = deferred<ProjectLabelCatalog>();
const noOpReorderCatalog = async (): Promise<ProjectLabelCatalog> => ({
  projectID: "project-1",
  labels: [],
});
const noOpReorderFailure = (error: unknown): void => {
  void error;
};

describe("Project catalog authority", () => {
  it("uses catalog order instead of assignment mutation order", () => {
    expect(
      selectOrderedProjectLabels(
        [
          { id: labelID, name: "Alpha" },
          { id: secondLabelID, name: "Beta" },
        ],
        [secondLabelID, labelID],
      ),
    ).toEqual([
      { id: labelID, name: "Alpha" },
      { id: secondLabelID, name: "Beta" },
    ]);
  });

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
      onReorderFailure: noOpReorderFailure,
      reorderCatalog: noOpReorderCatalog,
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
      onReorderFailure: noOpReorderFailure,
      reorderCatalog: noOpReorderCatalog,
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

    authority.applyDelete(labelID);
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
      onReorderFailure: noOpReorderFailure,
      reorderCatalog: noOpReorderCatalog,
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
      onReorderFailure: noOpReorderFailure,
      reorderCatalog: noOpReorderCatalog,
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

  it("keeps server order for rename and sends exactly one optimistic reorder", async () => {
    const reorder = deferred<ProjectLabelCatalog>();
    const calls: string[][] = [];
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => ({
        projectID: "project-1",
        labels: [
          { id: labelID, name: "Alpha" },
          { id: secondLabelID, name: "Renamed" },
        ],
      }),
      reorderCatalog: async (labelIDs) => {
        calls.push([...labelIDs]);
        return reorder.promise;
      },
      onReorderFailure: noOpReorderFailure,
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(key, {
      projectID: "project-1",
      labels: [
        { id: labelID, name: "Alpha" },
        { id: secondLabelID, name: "Beta" },
      ],
    });

    authority.applyRename({ id: secondLabelID, name: "Renamed" });
    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
      { id: labelID, name: "Alpha" },
      { id: secondLabelID, name: "Renamed" },
    ]);

    authority.reorder([secondLabelID, labelID]);
    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels.map((label) => label.id)).toEqual([
      secondLabelID,
      labelID,
    ]);
    await waitFor(() => {
      expect(calls).toEqual([[secondLabelID, labelID]]);
    });

    reorder.resolve({
      projectID: "project-1",
      labels: [
        { id: secondLabelID, name: "Renamed" },
        { id: labelID, name: "Alpha" },
      ],
    });
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
        { id: secondLabelID, name: "Renamed" },
        { id: labelID, name: "Alpha" },
      ]);
    });
  });

  it("projects create, rename, and delete in server order without a name comparator", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => pendingCatalog.promise,
      onReorderFailure: noOpReorderFailure,
      reorderCatalog: noOpReorderCatalog,
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(
      key,
      catalog([
        [secondLabelID, "Zulu"],
        [labelID, "Alpha"],
      ]),
    );

    authority.applyCreate({ id: thirdLabelID, name: "Beta" });
    authority.applyRename({ id: labelID, name: "Renamed" });
    authority.applyDelete(secondLabelID);

    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
      { id: labelID, name: "Renamed" },
      { id: thirdLabelID, name: "Beta" },
    ]);
  });

  it("keeps only the newest pending reorder while the first request is in flight", async () => {
    const firstReorder = deferred<ProjectLabelCatalog>();
    const secondReorder = deferred<ProjectLabelCatalog>();
    const reconciliation = deferred<ProjectLabelCatalog>();
    const calls: string[][] = [];
    let readCount = 0;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => {
        readCount += 1;
        return reconciliation.promise;
      },
      reorderCatalog: async (labelIDs) => {
        calls.push([...labelIDs]);
        return calls.length === 1 ? firstReorder.promise : secondReorder.promise;
      },
      onReorderFailure: noOpReorderFailure,
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(
      key,
      catalog([
        [labelID, "Alpha"],
        [secondLabelID, "Beta"],
        [thirdLabelID, "Gamma"],
      ]),
    );

    authority.reorder([secondLabelID, labelID, thirdLabelID]);
    await waitFor(() => {
      expect(calls).toEqual([[secondLabelID, labelID, thirdLabelID]]);
    });
    authority.reorder([thirdLabelID, secondLabelID, labelID]);

    expect(labelIDs(queryClient, key)).toEqual([thirdLabelID, secondLabelID, labelID]);
    firstReorder.resolve(
      catalog([
        [secondLabelID, "Beta"],
        [labelID, "Alpha"],
        [thirdLabelID, "Gamma"],
      ]),
    );
    await waitFor(() => {
      expect(calls).toEqual([
        [secondLabelID, labelID, thirdLabelID],
        [thirdLabelID, secondLabelID, labelID],
      ]);
    });
    expect(labelIDs(queryClient, key)).toEqual([thirdLabelID, secondLabelID, labelID]);

    secondReorder.resolve(
      catalog([
        [thirdLabelID, "Gamma"],
        [secondLabelID, "Beta"],
        [labelID, "Alpha"],
      ]),
    );
    await waitFor(() => {
      expect(readCount).toBe(1);
    });
    reconciliation.resolve(
      catalog([
        [thirdLabelID, "Gamma"],
        [secondLabelID, "Beta"],
        [labelID, "Alpha"],
      ]),
    );
    await waitFor(() => {
      expect(labelIDs(queryClient, key)).toEqual([thirdLabelID, secondLabelID, labelID]);
    });
  });

  it("does not let a late reorder settlement remove a created Label", async () => {
    const reorder = deferred<ProjectLabelCatalog>();
    const firstRefresh = deferred<ProjectLabelCatalog>();
    const finalRefresh = deferred<ProjectLabelCatalog>();
    const failures: unknown[] = [];
    let readCount = 0;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => {
        readCount += 1;
        return readCount === 1 ? firstRefresh.promise : finalRefresh.promise;
      },
      onReorderFailure: (error) => {
        failures.push(error);
      },
      reorderCatalog: async () => reorder.promise,
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(
      key,
      catalog([
        [labelID, "Alpha"],
        [secondLabelID, "Beta"],
      ]),
    );

    authority.reorder([secondLabelID, labelID]);
    await waitFor(() => {
      expect(readCount).toBe(0);
    });
    authority.applyCreate({ id: thirdLabelID, name: "Gamma" });
    expect(labelIDs(queryClient, key)).toEqual([secondLabelID, labelID, thirdLabelID]);
    await waitFor(() => {
      expect(readCount).toBe(1);
    });

    firstRefresh.resolve(
      catalog([
        [secondLabelID, "Beta"],
        [labelID, "Alpha"],
        [thirdLabelID, "Gamma"],
      ]),
    );
    await waitFor(() => {
      expect(labelIDs(queryClient, key)).toEqual([secondLabelID, labelID, thirdLabelID]);
    });
    reorder.resolve(
      catalog([
        [secondLabelID, "Beta"],
        [labelID, "Alpha"],
      ]),
    );
    expect(labelIDs(queryClient, key)).toEqual([secondLabelID, labelID, thirdLabelID]);
    await waitFor(() => {
      expect(readCount).toBe(2);
    });
    finalRefresh.resolve(
      catalog([
        [secondLabelID, "Beta"],
        [labelID, "Alpha"],
        [thirdLabelID, "Gamma"],
      ]),
    );
    await waitFor(() => {
      expect(labelIDs(queryClient, key)).toEqual([secondLabelID, labelID, thirdLabelID]);
    });
    expect(failures).toEqual([]);
  });

  it("does not let a failed reorder resurrect a deleted Label", async () => {
    const reorder = deferred<ProjectLabelCatalog>();
    const firstRefresh = deferred<ProjectLabelCatalog>();
    const finalRefresh = deferred<ProjectLabelCatalog>();
    const failures: unknown[] = [];
    let readCount = 0;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => {
        readCount += 1;
        return readCount === 1 ? firstRefresh.promise : finalRefresh.promise;
      },
      onReorderFailure: (error) => {
        failures.push(error);
      },
      reorderCatalog: async () => reorder.promise,
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(
      key,
      catalog([
        [labelID, "Alpha"],
        [secondLabelID, "Beta"],
      ]),
    );

    authority.reorder([secondLabelID, labelID]);
    authority.applyDelete(labelID);
    expect(labelIDs(queryClient, key)).toEqual([secondLabelID]);
    await waitFor(() => {
      expect(readCount).toBe(1);
    });

    firstRefresh.resolve(catalog([[secondLabelID, "Beta"]]));
    await waitFor(() => {
      expect(labelIDs(queryClient, key)).toEqual([secondLabelID]);
    });
    const failure = new Error("reorder failed");
    reorder.reject(failure);
    expect(labelIDs(queryClient, key)).toEqual([secondLabelID]);
    await waitFor(() => {
      expect(readCount).toBe(2);
    });
    finalRefresh.resolve(catalog([[secondLabelID, "Beta"]]));
    await waitFor(() => {
      expect(labelIDs(queryClient, key)).toEqual([secondLabelID]);
    });
    expect(failures).toEqual([failure]);
  });

  it("preserves a renamed current record when an older reorder succeeds", async () => {
    const reorder = deferred<ProjectLabelCatalog>();
    const firstRefresh = deferred<ProjectLabelCatalog>();
    const finalRefresh = deferred<ProjectLabelCatalog>();
    let readCount = 0;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => {
        readCount += 1;
        return readCount === 1 ? firstRefresh.promise : finalRefresh.promise;
      },
      onReorderFailure: noOpReorderFailure,
      reorderCatalog: async () => reorder.promise,
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(
      key,
      catalog([
        [labelID, "Alpha"],
        [secondLabelID, "Beta"],
      ]),
    );

    authority.reorder([secondLabelID, labelID]);
    authority.applyRename({ id: labelID, name: "Renamed" });
    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
      { id: secondLabelID, name: "Beta" },
      { id: labelID, name: "Renamed" },
    ]);
    await waitFor(() => {
      expect(readCount).toBe(1);
    });

    firstRefresh.resolve(
      catalog([
        [secondLabelID, "Beta"],
        [labelID, "Renamed"],
      ]),
    );
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
        { id: secondLabelID, name: "Beta" },
        { id: labelID, name: "Renamed" },
      ]);
    });
    reorder.resolve(
      catalog([
        [secondLabelID, "Beta"],
        [labelID, "Alpha"],
      ]),
    );
    expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
      { id: secondLabelID, name: "Beta" },
      { id: labelID, name: "Renamed" },
    ]);
    await waitFor(() => {
      expect(readCount).toBe(2);
    });
    finalRefresh.resolve(
      catalog([
        [secondLabelID, "Beta"],
        [labelID, "Renamed"],
      ]),
    );
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
        { id: secondLabelID, name: "Beta" },
        { id: labelID, name: "Renamed" },
      ]);
    });
  });

  it("rolls back a failed pending reorder through refreshed Label records", async () => {
    const firstReorder = deferred<ProjectLabelCatalog>();
    const secondReorder = deferred<ProjectLabelCatalog>();
    const refreshed = deferred<ProjectLabelCatalog>();
    const finalRefresh = deferred<ProjectLabelCatalog>();
    const calls: string[][] = [];
    const failures: unknown[] = [];
    let readCount = 0;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => {
        readCount += 1;
        return readCount === 1 ? refreshed.promise : finalRefresh.promise;
      },
      onReorderFailure: (error) => {
        failures.push(error);
      },
      reorderCatalog: async (labelIDs) => {
        calls.push([...labelIDs]);
        return calls.length === 1 ? firstReorder.promise : secondReorder.promise;
      },
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(
      key,
      catalog([
        [labelID, "Alpha"],
        [secondLabelID, "Beta"],
        [thirdLabelID, "Gamma"],
      ]),
    );

    authority.reorder([secondLabelID, labelID, thirdLabelID]);
    await waitFor(() => {
      expect(calls).toEqual([[secondLabelID, labelID, thirdLabelID]]);
    });
    authority.reorder([thirdLabelID, secondLabelID, labelID]);
    authority.requestRefresh();
    await waitFor(() => {
      expect(readCount).toBe(1);
    });
    refreshed.resolve(
      catalog([
        [labelID, "Alpha"],
        [secondLabelID, "Renamed"],
        [thirdLabelID, "Gamma"],
      ]),
    );
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
        { id: thirdLabelID, name: "Gamma" },
        { id: secondLabelID, name: "Renamed" },
        { id: labelID, name: "Alpha" },
      ]);
    });

    firstReorder.resolve(
      catalog([
        [secondLabelID, "Beta"],
        [labelID, "Alpha"],
        [thirdLabelID, "Gamma"],
      ]),
    );
    await waitFor(() => {
      expect(calls).toEqual([
        [secondLabelID, labelID, thirdLabelID],
        [thirdLabelID, secondLabelID, labelID],
      ]);
    });
    const failure = new Error("pending reorder failed");
    secondReorder.reject(failure);
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
        { id: secondLabelID, name: "Renamed" },
        { id: labelID, name: "Alpha" },
        { id: thirdLabelID, name: "Gamma" },
      ]);
    });
    await waitFor(() => {
      expect(readCount).toBe(2);
    });
    finalRefresh.resolve(
      catalog([
        [secondLabelID, "Renamed"],
        [labelID, "Alpha"],
        [thirdLabelID, "Gamma"],
      ]),
    );
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
        { id: secondLabelID, name: "Renamed" },
        { id: labelID, name: "Alpha" },
        { id: thirdLabelID, name: "Gamma" },
      ]);
    });
    expect(failures).toEqual([failure]);
  });

  it("reschedules a failed reorder after rename without restoring the old name", async () => {
    const firstReorder = deferred<ProjectLabelCatalog>();
    const secondReorder = deferred<ProjectLabelCatalog>();
    const refreshed = deferred<ProjectLabelCatalog>();
    const calls: string[][] = [];
    const failures: unknown[] = [];
    let readCount = 0;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => {
        readCount += 1;
        return refreshed.promise;
      },
      onReorderFailure: (error) => {
        failures.push(error);
      },
      reorderCatalog: async (labelIDs) => {
        calls.push([...labelIDs]);
        return calls.length === 1 ? firstReorder.promise : secondReorder.promise;
      },
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(
      key,
      catalog([
        [labelID, "Alpha"],
        [secondLabelID, "Beta"],
      ]),
    );

    authority.reorder([secondLabelID, labelID]);
    authority.applyRename({ id: labelID, name: "Renamed" });
    await waitFor(() => {
      expect(readCount).toBe(1);
    });
    refreshed.resolve(
      catalog([
        [secondLabelID, "Beta"],
        [labelID, "Renamed"],
      ]),
    );
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
        { id: secondLabelID, name: "Beta" },
        { id: labelID, name: "Renamed" },
      ]);
    });

    const failure = new Error("renamed reorder failed");
    firstReorder.reject(failure);
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
        { id: secondLabelID, name: "Beta" },
        { id: labelID, name: "Renamed" },
      ]);
    });
    await waitFor(() => {
      expect(calls).toEqual([
        [secondLabelID, labelID],
        [secondLabelID, labelID],
      ]);
    });
    secondReorder.resolve(
      catalog([
        [secondLabelID, "Beta"],
        [labelID, "Renamed"],
      ]),
    );
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
        { id: secondLabelID, name: "Beta" },
        { id: labelID, name: "Renamed" },
      ]);
    });
    expect(failures).toEqual([failure]);
  });

  it("supersedes pending reorder on changed-membership refresh without overwriting it later", async () => {
    const firstReorder = deferred<ProjectLabelCatalog>();
    const changedRefresh = deferred<ProjectLabelCatalog>();
    const finalRefresh = deferred<ProjectLabelCatalog>();
    const calls: string[][] = [];
    let readCount = 0;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => {
        readCount += 1;
        return readCount === 1 ? changedRefresh.promise : finalRefresh.promise;
      },
      onReorderFailure: noOpReorderFailure,
      reorderCatalog: async (labelIDs) => {
        calls.push([...labelIDs]);
        return firstReorder.promise;
      },
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(
      key,
      catalog([
        [labelID, "Alpha"],
        [secondLabelID, "Beta"],
        [thirdLabelID, "Gamma"],
      ]),
    );

    authority.reorder([secondLabelID, labelID, thirdLabelID]);
    await waitFor(() => {
      expect(calls).toEqual([[secondLabelID, labelID, thirdLabelID]]);
    });
    authority.reorder([thirdLabelID, secondLabelID, labelID]);
    authority.requestRefresh();
    await waitFor(() => {
      expect(readCount).toBe(1);
    });
    changedRefresh.resolve(
      catalog([
        [labelID, "Alpha"],
        [thirdLabelID, "Gamma"],
      ]),
    );
    await waitFor(() => {
      expect(labelIDs(queryClient, key)).toEqual([labelID, thirdLabelID]);
    });
    expect(calls).toEqual([[secondLabelID, labelID, thirdLabelID]]);

    firstReorder.resolve(
      catalog([
        [secondLabelID, "Beta"],
        [labelID, "Alpha"],
        [thirdLabelID, "Gamma"],
      ]),
    );
    await waitFor(() => {
      expect(readCount).toBe(2);
    });
    finalRefresh.resolve(
      catalog([
        [labelID, "Alpha"],
        [thirdLabelID, "Gamma"],
      ]),
    );
    await waitFor(() => {
      expect(labelIDs(queryClient, key)).toEqual([labelID, thirdLabelID]);
    });
  });

  it("rebases a pending reorder through same-membership refresh and sends it after the first settles", async () => {
    const firstReorder = deferred<ProjectLabelCatalog>();
    const secondReorder = deferred<ProjectLabelCatalog>();
    const refreshed = deferred<ProjectLabelCatalog>();
    const finalRefresh = deferred<ProjectLabelCatalog>();
    const calls: string[][] = [];
    let readCount = 0;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => {
        readCount += 1;
        if (readCount === 1) {
          return refreshed.promise;
        }
        return finalRefresh.promise;
      },
      reorderCatalog: async (labelIDs) => {
        calls.push([...labelIDs]);
        return calls.length === 1 ? firstReorder.promise : secondReorder.promise;
      },
      onReorderFailure: noOpReorderFailure,
    });
    const key = queryKeys.projectLabels("project-1");
    queryClient.setQueryData<ProjectLabelCatalog>(
      key,
      catalog([
        [labelID, "Alpha"],
        [secondLabelID, "Beta"],
        [thirdLabelID, "Gamma"],
      ]),
    );

    authority.reorder([secondLabelID, labelID, thirdLabelID]);
    await waitFor(() => {
      expect(calls).toEqual([[secondLabelID, labelID, thirdLabelID]]);
    });
    authority.reorder([thirdLabelID, secondLabelID, labelID]);
    authority.requestRefresh();
    await waitFor(() => {
      expect(readCount).toBe(1);
    });
    refreshed.resolve(
      catalog([
        [labelID, "Alpha"],
        [secondLabelID, "Renamed"],
        [thirdLabelID, "Gamma"],
      ]),
    );
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
        { id: thirdLabelID, name: "Gamma" },
        { id: secondLabelID, name: "Renamed" },
        { id: labelID, name: "Alpha" },
      ]);
    });

    firstReorder.resolve(
      catalog([
        [secondLabelID, "Beta"],
        [labelID, "Alpha"],
        [thirdLabelID, "Gamma"],
      ]),
    );
    await waitFor(() => {
      expect(calls).toEqual([
        [secondLabelID, labelID, thirdLabelID],
        [thirdLabelID, secondLabelID, labelID],
      ]);
    });
    secondReorder.resolve(
      catalog([
        [thirdLabelID, "Gamma"],
        [secondLabelID, "Renamed"],
        [labelID, "Alpha"],
      ]),
    );
    await waitFor(() => {
      expect(readCount).toBe(2);
    });
    finalRefresh.resolve(
      catalog([
        [thirdLabelID, "Gamma"],
        [secondLabelID, "Renamed"],
        [labelID, "Alpha"],
      ]),
    );
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels).toEqual([
        { id: thirdLabelID, name: "Gamma" },
        { id: secondLabelID, name: "Renamed" },
        { id: labelID, name: "Alpha" },
      ]);
    });
  });
});

function catalog(entries: readonly (readonly [string, string])[]): ProjectLabelCatalog {
  return {
    projectID: "project-1",
    labels: entries.map(([id, name]) => ({ id, name })),
  };
}

function labelIDs(queryClient: QueryClient, key: ReturnType<typeof queryKeys.projectLabels>): string[] {
  return queryClient.getQueryData<ProjectLabelCatalog>(key)?.labels.map((label) => label.id) ?? [];
}

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  resolve(value: T): void;
  reject(error: unknown): void;
}> {
  let resolve: ((value: T) => void) | null = null;
  let reject: ((error: unknown) => void) | null = null;
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve;
    reject = nextReject;
  });
  return {
    promise,
    resolve(value: T): void {
      resolve?.(value);
    },
    reject(error: unknown): void {
      reject?.(error);
    },
  };
}
