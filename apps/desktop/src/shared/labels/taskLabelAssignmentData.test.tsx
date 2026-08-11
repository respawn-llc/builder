import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { vi } from "vitest";

import type { TaskLabelAssignment } from "@/api";
import { queryKeys } from "@/app-facade";
import { pruneDeletedLabelFromExistingCaches, removeDeletedTaskFromExistingCaches } from "./taskLabelCache";
import { useManagedTaskLabelAssignment } from "./taskLabelAssignmentData";

const taskID = "task-1";
const projectID = "project-1";
const alphaID = "38bf0da7-a3f7-4c15-bc5f-c8fca538e667";
const betaID = "942495c2-5958-4959-8445-94046ad74fbd";
const remoteID = "2d6d42a1-461d-4a31-b8ad-858e98ea4058";

const appServiceMocks = vi.hoisted(() => ({
  getTaskLabels: vi.fn(),
  updateTaskLabels: vi.fn(),
}));

vi.mock("@/app-facade", async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return {
    ...actual,
    useAppServices: () => ({
      api: {
        getTaskLabels: appServiceMocks.getTaskLabels,
        updateTaskLabels: appServiceMocks.updateTaskLabels,
      },
    }),
  };
});

describe("useManagedTaskLabelAssignment", () => {
  beforeEach(() => {
    appServiceMocks.getTaskLabels.mockReset();
    appServiceMocks.updateTaskLabels.mockReset();
  });

  it("cancels the exact assignment, installs the normalized response, and refreshes membership", async () => {
    appServiceMocks.getTaskLabels.mockResolvedValueOnce(assignment([]));
    const update = deferred<TaskLabelAssignment>();
    appServiceMocks.updateTaskLabels.mockReturnValueOnce(update.promise);
    const queryClient = createQueryClient([alphaID, betaID]);
    const scheduleCatalogRefresh = vi.fn();
    const scheduleMembershipRefresh = vi.fn();
    const scheduleTaskAssignmentRefresh = vi.fn();
    const cancel = vi.spyOn(queryClient, "cancelQueries");
    const { result } = renderHook(
      () =>
        useManagedTaskLabelAssignment({
          availableLabelIDs: [alphaID, betaID],
          projectID,
          scheduleCatalogRefresh,
          scheduleMembershipRefresh,
          scheduleTaskAssignmentRefresh,
          taskID,
        }),
      { wrapper: createWrapper(queryClient) },
    );

    await waitFor(() => {
      expect(result.current.selectedLabelIDs).toEqual([]);
    });
    expect(appServiceMocks.getTaskLabels).toHaveBeenCalledWith(taskID);

    act(() => {
      result.current.setSelected(alphaID, true);
    });
    expect(result.current.selectedLabelIDs).toEqual([alphaID]);
    expect(result.current.pendingLabelIDs).toEqual([alphaID]);
    expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledWith(taskID, [alphaID], []);

    update.resolve(assignment([alphaID, betaID]));
    await waitFor(() => {
      expect(result.current.pendingLabelIDs).toEqual([]);
    });
    expect(cancel).toHaveBeenCalledWith(
      { queryKey: queryKeys.taskLabels(taskID), exact: true },
      { revert: false, silent: true },
    );
    expect(queryClient.getQueryData(queryKeys.taskLabels(taskID))).toEqual(assignment([alphaID, betaID]));
    expect(queryClient.getQueryData(queryKeys.task(taskID))).toMatchObject({
      labelIDs: [alphaID, betaID],
    });
    expect(scheduleCatalogRefresh).not.toHaveBeenCalled();
    expect(scheduleMembershipRefresh).toHaveBeenCalledOnce();
    expect(scheduleTaskAssignmentRefresh).not.toHaveBeenCalled();
  });

  it("auto-selects and preserves queued created Labels while the catalog query is still loading", async () => {
    appServiceMocks.getTaskLabels.mockResolvedValueOnce(assignment([]));
    const beta = deferred<TaskLabelAssignment>();
    const alpha = deferred<TaskLabelAssignment>();
    const remote = deferred<TaskLabelAssignment>();
    appServiceMocks.updateTaskLabels
      .mockReturnValueOnce(beta.promise)
      .mockReturnValueOnce(alpha.promise)
      .mockReturnValueOnce(remote.promise);
    const queryClient = createQueryClient([]);
    queryClient.removeQueries({
      queryKey: queryKeys.projectLabels(projectID),
      exact: true,
    });
    const scheduleCatalogRefresh = vi.fn();
    const scheduleMembershipRefresh = vi.fn();
    const scheduleTaskAssignmentRefresh = vi.fn();
    const loadingCatalogLabelIDs: readonly string[] = [];
    const { result, rerender } = renderHook(
      ({ availableLabelIDs }: Readonly<{ availableLabelIDs: readonly string[] }>) =>
        useManagedTaskLabelAssignment({
          availableLabelIDs,
          projectID,
          scheduleCatalogRefresh,
          scheduleMembershipRefresh,
          scheduleTaskAssignmentRefresh,
          taskID,
        }),
      {
        initialProps: { availableLabelIDs: loadingCatalogLabelIDs },
        wrapper: createWrapper(queryClient),
      },
    );
    await waitFor(() => {
      expect(result.current.isPending).toBe(false);
    });

    act(() => {
      result.current.setSelected(betaID, true);
      result.current.setSelected(alphaID, true);
      result.current.setSelected(remoteID, true);
    });
    expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(1);
    expect(appServiceMocks.updateTaskLabels).toHaveBeenLastCalledWith(taskID, [betaID], []);

    await act(async () => {
      beta.resolve(assignment([betaID]));
      await beta.promise;
    });
    await waitFor(() => {
      expect(scheduleCatalogRefresh).toHaveBeenCalledOnce();
      expect(scheduleMembershipRefresh).not.toHaveBeenCalled();
      expect(scheduleTaskAssignmentRefresh).toHaveBeenCalledWith(taskID);
    });
    expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(1);

    queryClient.setQueryData(queryKeys.taskLabels(taskID), assignment([betaID]));
    queryClient.setQueryData(queryKeys.projectLabels(projectID), {
      projectID,
      labels: [
        { id: betaID, name: "Beta" },
        { id: alphaID, name: "Alpha" },
        { id: remoteID, name: "Remote" },
      ],
    });
    rerender({ availableLabelIDs: [betaID, alphaID, remoteID] });

    await waitFor(() => {
      expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(2);
    });
    expect(appServiceMocks.updateTaskLabels).toHaveBeenLastCalledWith(taskID, [alphaID], []);

    await act(async () => {
      alpha.resolve(assignment([alphaID]));
      await alpha.promise;
    });
    await waitFor(() => {
      expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(3);
    });
    expect(appServiceMocks.updateTaskLabels).toHaveBeenLastCalledWith(taskID, [remoteID], []);

    await act(async () => {
      remote.resolve(assignment([betaID, alphaID, remoteID]));
      await remote.promise;
    });
    await waitFor(() => {
      expect(result.current.pendingLabelIDs).toEqual([]);
    });
    expect([...result.current.selectedLabelIDs].sort()).toEqual([alphaID, betaID, remoteID].sort());
    expect(scheduleTaskAssignmentRefresh).toHaveBeenCalledOnce();
    expect(scheduleMembershipRefresh).toHaveBeenCalledTimes(2);
  });

  it("rolls back a failed current intent and retains per-Label Retry", async () => {
    appServiceMocks.getTaskLabels.mockResolvedValueOnce(assignment([]));
    const first = deferred<TaskLabelAssignment>();
    const second = deferred<TaskLabelAssignment>();
    appServiceMocks.updateTaskLabels.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const queryClient = createQueryClient([alphaID]);
    const { result } = renderHook(
      () =>
        useManagedTaskLabelAssignment({
          availableLabelIDs: [alphaID],
          projectID,
          scheduleCatalogRefresh: vi.fn(),
          scheduleMembershipRefresh: vi.fn(),
          scheduleTaskAssignmentRefresh: vi.fn(),
          taskID,
        }),
      { wrapper: createWrapper(queryClient) },
    );
    await waitFor(() => {
      expect(result.current.isPending).toBe(false);
    });

    act(() => {
      result.current.setSelected(alphaID, true);
    });
    expect(result.current.selectedLabelIDs).toEqual([alphaID]);
    const failure = new Error("assignment failed");
    first.reject(failure);

    await waitFor(() => {
      expect(result.current.selectedLabelIDs).toEqual([]);
      expect(result.current.pendingLabelIDs).toEqual([]);
      expect(result.current.failures).toEqual([
        {
          desiredSelected: true,
          error: failure,
          labelID: alphaID,
        },
      ]);
    });

    act(() => {
      result.current.retry(alphaID);
    });
    expect(result.current.selectedLabelIDs).toEqual([alphaID]);
    expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(2);
    second.resolve(assignment([alphaID]));
    await waitFor(() => {
      expect(result.current.failures).toEqual([]);
      expect(result.current.pendingLabelIDs).toEqual([]);
    });
  });

  it("coalesces rapid add-remove-add and serializes another Label behind it", async () => {
    appServiceMocks.getTaskLabels.mockResolvedValueOnce(assignment([]));
    const alpha = deferred<TaskLabelAssignment>();
    const beta = deferred<TaskLabelAssignment>();
    appServiceMocks.updateTaskLabels.mockReturnValueOnce(alpha.promise).mockReturnValueOnce(beta.promise);
    const queryClient = createQueryClient([alphaID, betaID]);
    const { result } = renderHook(
      () =>
        useManagedTaskLabelAssignment({
          availableLabelIDs: [alphaID, betaID],
          projectID,
          scheduleCatalogRefresh: vi.fn(),
          scheduleMembershipRefresh: vi.fn(),
          scheduleTaskAssignmentRefresh: vi.fn(),
          taskID,
        }),
      { wrapper: createWrapper(queryClient) },
    );
    await waitFor(() => {
      expect(result.current.isPending).toBe(false);
    });

    act(() => {
      result.current.setSelected(alphaID, true);
      result.current.setSelected(alphaID, false);
      result.current.setSelected(alphaID, true);
      result.current.setSelected(betaID, true);
    });
    expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(1);
    expect(result.current.selectedLabelIDs).toEqual([alphaID, betaID]);

    alpha.resolve(assignment([alphaID, remoteID]));
    await waitFor(() => {
      expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(2);
    });
    expect(queryClient.getQueryData(queryKeys.taskLabels(taskID))).toEqual(assignment([alphaID]));
    expect(appServiceMocks.updateTaskLabels).toHaveBeenLastCalledWith(taskID, [betaID], []);
    beta.resolve(assignment([alphaID, betaID]));

    await waitFor(() => {
      expect(result.current.pendingLabelIDs).toEqual([]);
    });
    expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(2);
  });

  it("lets a background assignment result normalize queued Labels without replacing the in-flight intent", async () => {
    appServiceMocks.getTaskLabels.mockResolvedValueOnce(assignment([]));
    const update = deferred<TaskLabelAssignment>();
    appServiceMocks.updateTaskLabels.mockReturnValueOnce(update.promise);
    const queryClient = createQueryClient([alphaID, betaID]);
    const { result } = renderHook(
      () =>
        useManagedTaskLabelAssignment({
          availableLabelIDs: [alphaID, betaID],
          projectID,
          scheduleCatalogRefresh: vi.fn(),
          scheduleMembershipRefresh: vi.fn(),
          scheduleTaskAssignmentRefresh: vi.fn(),
          taskID,
        }),
      { wrapper: createWrapper(queryClient) },
    );
    await waitFor(() => {
      expect(result.current.isPending).toBe(false);
    });

    act(() => {
      result.current.setSelected(alphaID, true);
      result.current.setSelected(betaID, true);
    });
    queryClient.setQueryData(queryKeys.taskLabels(taskID), assignment([betaID]));

    await waitFor(() => {
      expect(result.current.selectedLabelIDs).toEqual([betaID, alphaID]);
      expect(result.current.pendingLabelIDs).toEqual([alphaID]);
    });
    expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(1);

    update.resolve(assignment([alphaID, betaID]));
    await waitFor(() => {
      expect(result.current.pendingLabelIDs).toEqual([]);
    });
    expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(1);
  });

  it("shares server query data without sharing destination pending or failure state", async () => {
    appServiceMocks.getTaskLabels.mockResolvedValueOnce(assignment([]));
    const update = deferred<TaskLabelAssignment>();
    appServiceMocks.updateTaskLabels.mockReturnValueOnce(update.promise);
    const queryClient = createQueryClient([alphaID]);
    const { result } = renderHook(
      () =>
        [
          useManagedTaskLabelAssignment({
            availableLabelIDs: [alphaID],
            projectID,
            scheduleCatalogRefresh: vi.fn(),
            scheduleMembershipRefresh: vi.fn(),
            scheduleTaskAssignmentRefresh: vi.fn(),
            taskID,
          }),
          useManagedTaskLabelAssignment({
            availableLabelIDs: [alphaID],
            projectID,
            scheduleCatalogRefresh: vi.fn(),
            scheduleMembershipRefresh: vi.fn(),
            scheduleTaskAssignmentRefresh: vi.fn(),
            taskID,
          }),
        ] as const,
      { wrapper: createWrapper(queryClient) },
    );
    await waitFor(() => {
      expect(result.current[0].isPending).toBe(false);
      expect(result.current[1].isPending).toBe(false);
    });

    act(() => {
      result.current[0].setSelected(alphaID, true);
    });
    expect(result.current[0].pendingLabelIDs).toEqual([alphaID]);
    expect(result.current[1].pendingLabelIDs).toEqual([]);

    const failure = new Error("first destination failed");
    update.reject(failure);
    await waitFor(() => {
      expect(result.current[0].failures).toHaveLength(1);
    });
    expect(result.current[1].failures).toEqual([]);
    expect(queryClient.getQueryData(queryKeys.taskLabels(taskID))).toEqual(assignment([]));
  });

  it("does not restore a Label deleted while an assignment response is late", async () => {
    appServiceMocks.getTaskLabels.mockResolvedValueOnce(assignment([]));
    const update = deferred<TaskLabelAssignment>();
    appServiceMocks.updateTaskLabels.mockReturnValueOnce(update.promise);
    const queryClient = createQueryClient([alphaID]);
    const { result, rerender } = renderHook(
      ({ availableLabelIDs }: Readonly<{ availableLabelIDs: readonly string[] }>) =>
        useManagedTaskLabelAssignment({
          availableLabelIDs,
          projectID,
          scheduleCatalogRefresh: vi.fn(),
          scheduleMembershipRefresh: vi.fn(),
          scheduleTaskAssignmentRefresh: vi.fn(),
          taskID,
        }),
      {
        initialProps: { availableLabelIDs: [alphaID] },
        wrapper: createWrapper(queryClient),
      },
    );
    await waitFor(() => {
      expect(result.current.isPending).toBe(false);
    });

    act(() => {
      result.current.setSelected(alphaID, true);
    });
    queryClient.setQueryData(queryKeys.projectLabels(projectID), {
      projectID,
      labels: [],
    });
    pruneDeletedLabelFromExistingCaches(queryClient, projectID, alphaID);
    rerender({ availableLabelIDs: [] });
    update.resolve(assignment([alphaID]));

    await waitFor(() => {
      expect(result.current.pendingLabelIDs).toEqual([]);
    });
    expect(queryClient.getQueryData(queryKeys.taskLabels(taskID))).toEqual(assignment([]));
    expect(queryClient.getQueryData(queryKeys.task(taskID))).toMatchObject({ labelIDs: [] });
  });

  it("stops a queued assignment when the Task is deleted during the in-flight request", async () => {
    appServiceMocks.getTaskLabels.mockResolvedValueOnce(assignment([]));
    const update = deferred<TaskLabelAssignment>();
    appServiceMocks.updateTaskLabels.mockReturnValueOnce(update.promise);
    const queryClient = createQueryClient([alphaID, betaID]);
    const { result } = renderHook(
      () =>
        useManagedTaskLabelAssignment({
          availableLabelIDs: [alphaID, betaID],
          projectID,
          scheduleCatalogRefresh: vi.fn(),
          scheduleMembershipRefresh: vi.fn(),
          scheduleTaskAssignmentRefresh: vi.fn(),
          taskID,
        }),
      { wrapper: createWrapper(queryClient) },
    );
    await waitFor(() => {
      expect(result.current.isPending).toBe(false);
    });

    act(() => {
      result.current.setSelected(alphaID, true);
      result.current.setSelected(betaID, true);
    });
    expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(1);
    removeDeletedTaskFromExistingCaches(queryClient, taskID);
    update.resolve(assignment([alphaID]));

    await waitFor(() => {
      expect(result.current.pendingLabelIDs).toEqual([]);
      expect(result.current.failures).toEqual([]);
    });
    expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(1);
    expect(queryClient.getQueryData(queryKeys.taskLabels(taskID))).toBeUndefined();
    expect(queryClient.getQueryData(queryKeys.task(taskID))).toBeUndefined();
  });

  it("drops queued work and late success or failure after unmount without touching caches", async () => {
    for (const succeeds of [true, false]) {
      appServiceMocks.getTaskLabels.mockReset();
      appServiceMocks.getTaskLabels.mockResolvedValue(assignment([]));
      appServiceMocks.updateTaskLabels.mockReset();
      const update = deferred<TaskLabelAssignment>();
      appServiceMocks.updateTaskLabels.mockReturnValueOnce(update.promise);
      const queryClient = createQueryClient([alphaID, betaID]);
      const scheduleCatalogRefresh = vi.fn();
      const scheduleMembershipRefresh = vi.fn();
      const scheduleTaskAssignmentRefresh = vi.fn();
      const input = {
        availableLabelIDs: [alphaID, betaID],
        projectID,
        scheduleCatalogRefresh,
        scheduleMembershipRefresh,
        scheduleTaskAssignmentRefresh,
        taskID,
      };
      const view = renderHook(() => useManagedTaskLabelAssignment(input), {
        wrapper: createWrapper(queryClient),
      });
      await waitFor(() => {
        expect(view.result.current.isPending).toBe(false);
      });

      act(() => {
        view.result.current.setSelected(alphaID, true);
        view.result.current.setSelected(betaID, true);
      });
      expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(1);
      view.unmount();
      if (succeeds) {
        update.resolve(assignment([alphaID]));
      } else {
        update.reject(new Error("late failure"));
      }
      await update.promise.catch(() => undefined);
      await new Promise<void>((resolve) => {
        setTimeout(resolve, 0);
      });

      expect(queryClient.getQueryData(queryKeys.taskLabels(taskID))).toEqual(assignment([]));
      expect(queryClient.getQueryData(queryKeys.task(taskID))).toMatchObject({ labelIDs: [] });
      expect(scheduleCatalogRefresh).not.toHaveBeenCalled();
      expect(scheduleMembershipRefresh).not.toHaveBeenCalled();
      expect(scheduleTaskAssignmentRefresh).not.toHaveBeenCalled();
      expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(1);

      const { result: remountedResult, unmount: unmountRemounted } = renderHook(
        () => useManagedTaskLabelAssignment(input),
        {
          wrapper: createWrapper(queryClient),
        },
      );
      await waitFor(() => {
        expect(remountedResult.current.isPending).toBe(false);
      });
      expect(remountedResult.current.pendingLabelIDs).toEqual([]);
      expect(remountedResult.current.failures).toEqual([]);
      expect(appServiceMocks.updateTaskLabels).toHaveBeenCalledTimes(1);
      unmountRemounted();
    }
  });
});

function createQueryClient(labelIDs: readonly string[]): QueryClient {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  queryClient.setQueryData(queryKeys.projectLabels(projectID), {
    projectID,
    labels: labelIDs.map((id) => ({ id, name: id === alphaID ? "Alpha" : "Beta" })),
  });
  queryClient.setQueryData(queryKeys.task(taskID), {
    id: taskID,
    labelIDs: [],
  });
  return queryClient;
}

function createWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: Readonly<{ children: ReactNode }>) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  };
}

function assignment(labelIDs: readonly string[]): TaskLabelAssignment {
  return { taskID, labelIDs };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve;
    reject = promiseReject;
  });
  return { promise, reject, resolve };
}
