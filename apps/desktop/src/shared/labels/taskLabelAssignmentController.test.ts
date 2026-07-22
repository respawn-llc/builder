import type { TaskLabelAssignment } from "@/api";
import { waitFor } from "@testing-library/react";
import { createTaskLabelAssignmentController as createController, type TaskLabelUpdateInput } from "./index";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const urgentID = "942495c2-5958-4959-8445-94046ad74fbd";
const availableLabelIDs = [
  priorityID,
  urgentID,
  ...Array.from(
    { length: 98 },
    (_, index) => `00000000-0000-4000-8000-${(index + 1).toString(16).padStart(12, "0")}`,
  ),
];

type ControllerInput = Parameters<typeof createController>[0];

function createTaskLabelAssignmentController(
  input: Omit<ControllerInput, "availableLabelIDs"> & Partial<Pick<ControllerInput, "availableLabelIDs">>,
) {
  return createController({
    ...input,
    availableLabelIDs: input.availableLabelIDs ?? availableLabelIDs,
  });
}

describe("task label assignment controller", () => {
  it("optimistically applies one desired label while one assignment RPC is in flight", async () => {
    const response = deferred<TaskLabelAssignment>();
    const updates: Readonly<{
      addLabelIDs: readonly string[];
      removeLabelIDs: readonly string[];
    }>[] = [];
    const controller = createTaskLabelAssignmentController({
      taskID: "task-1",
      initialLabelIDs: [],
      update: async (input) => {
        updates.push(input);
        return response.promise;
      },
      refetch: async () => ({
        taskID: "task-1",
        labelIDs: [priorityID],
      }),
    });

    controller.setDesired(priorityID, true);

    expect(updates).toEqual([
      {
        addLabelIDs: [priorityID],
        removeLabelIDs: [],
      },
    ]);
    expect(controller.getSnapshot()).toMatchObject({
      authoritativeLabelIDs: [],
      visibleLabelIDs: [priorityID],
      inFlightLabelID: priorityID,
      pendingLabelIDs: [priorityID],
    });

    response.resolve({
      taskID: "task-1",
      labelIDs: [priorityID],
    });

    await waitFor(() => {
      expect(controller.getSnapshot()).toMatchObject({
        authoritativeLabelIDs: [priorityID],
        visibleLabelIDs: [priorityID],
        inFlightLabelID: null,
        pendingLabelIDs: [],
      });
    });
  });

  it("reapplies a newer desired state after an older response replaces the base", async () => {
    const addResponse = deferred<TaskLabelAssignment>();
    const removeResponse = deferred<TaskLabelAssignment>();
    const updates: TaskLabelUpdateInput[] = [];
    const controller = createTaskLabelAssignmentController({
      taskID: "task-1",
      initialLabelIDs: [],
      update: async (input) => {
        updates.push(input);
        return updates.length === 1 ? addResponse.promise : removeResponse.promise;
      },
      refetch: async () => ({
        taskID: "task-1",
        labelIDs: [],
      }),
    });

    controller.setDesired(priorityID, true);
    controller.setDesired(priorityID, false);

    expect(controller.getSnapshot().visibleLabelIDs).toEqual([]);
    expect(updates).toEqual([
      {
        addLabelIDs: [priorityID],
        removeLabelIDs: [],
      },
    ]);

    addResponse.resolve({
      taskID: "task-1",
      labelIDs: [priorityID],
    });
    await waitFor(() => {
      expect(updates).toEqual([
        {
          addLabelIDs: [priorityID],
          removeLabelIDs: [],
        },
        {
          addLabelIDs: [],
          removeLabelIDs: [priorityID],
        },
      ]);
    });
    expect(controller.getSnapshot().visibleLabelIDs).toEqual([]);

    removeResponse.resolve({
      taskID: "task-1",
      labelIDs: [],
    });
    await waitFor(() => {
      expect(controller.getSnapshot()).toMatchObject({
        authoritativeLabelIDs: [],
        visibleLabelIDs: [],
        inFlightLabelID: null,
        pendingLabelIDs: [],
      });
    });
  });

  it("preserves a newer pending intent when an external authoritative update arrives", async () => {
    const addResponse = deferred<TaskLabelAssignment>();
    const removeResponse = deferred<TaskLabelAssignment>();
    const updates: TaskLabelUpdateInput[] = [];
    const controller = createTaskLabelAssignmentController({
      taskID: "task-1",
      initialLabelIDs: [],
      update: async (input) => {
        updates.push(input);
        return updates.length === 1 ? addResponse.promise : removeResponse.promise;
      },
      refetch: async () => ({
        taskID: "task-1",
        labelIDs: [],
      }),
    });

    controller.setDesired(priorityID, true);
    controller.setDesired(priorityID, false);
    controller.replaceAuthoritative({
      taskID: "task-1",
      labelIDs: [priorityID],
    });

    expect(controller.getSnapshot()).toMatchObject({
      authoritativeLabelIDs: [priorityID],
      visibleLabelIDs: [],
      pendingLabelIDs: [priorityID],
      inFlightLabelID: priorityID,
    });

    addResponse.resolve({
      taskID: "task-1",
      labelIDs: [priorityID],
    });
    await waitFor(() => {
      expect(updates).toHaveLength(2);
    });

    removeResponse.resolve({
      taskID: "task-1",
      labelIDs: [],
    });
    await waitFor(() => {
      expect(controller.getSnapshot()).toMatchObject({
        authoritativeLabelIDs: [],
        visibleLabelIDs: [],
        pendingLabelIDs: [],
        inFlightLabelID: null,
      });
    });
  });

  it("rolls back only the failed intent, continues the queue, and keeps Retry", async () => {
    const priorityResponse = deferred<TaskLabelAssignment>();
    const urgentResponse = deferred<TaskLabelAssignment>();
    const retryResponse = deferred<TaskLabelAssignment>();
    const updates: TaskLabelUpdateInput[] = [];
    const controller = createTaskLabelAssignmentController({
      taskID: "task-1",
      initialLabelIDs: [],
      update: async (input) => {
        updates.push(input);
        if (updates.length === 1) {
          return priorityResponse.promise;
        }
        if (updates.length === 2) {
          return urgentResponse.promise;
        }
        return retryResponse.promise;
      },
      refetch: async () => ({
        taskID: "task-1",
        labelIDs: [],
      }),
    });

    controller.setDesired(priorityID, true);
    controller.setDesired(urgentID, true);
    priorityResponse.reject(new Error("offline"));

    await waitFor(() => {
      expect(updates).toHaveLength(2);
    });
    expect(controller.getSnapshot()).toMatchObject({
      visibleLabelIDs: [urgentID],
      inFlightLabelID: urgentID,
      failures: [
        {
          labelID: priorityID,
          desiredSelected: true,
        },
      ],
    });

    urgentResponse.resolve({
      taskID: "task-1",
      labelIDs: [urgentID],
    });
    await waitFor(() => {
      expect(controller.getSnapshot()).toMatchObject({
        authoritativeLabelIDs: [urgentID],
        visibleLabelIDs: [urgentID],
        inFlightLabelID: null,
      });
    });

    controller.retry(priorityID);
    await waitFor(() => {
      expect(updates).toHaveLength(3);
    });
    retryResponse.resolve({
      taskID: "task-1",
      labelIDs: [urgentID, priorityID],
    });
    await waitFor(() => {
      expect(controller.getSnapshot()).toMatchObject({
        authoritativeLabelIDs: [urgentID, priorityID],
        visibleLabelIDs: [urgentID, priorityID],
        failures: [],
      });
    });
  });

  it("coalesces remote events into one assignment refetch after local work drains", async () => {
    const updateResponse = deferred<TaskLabelAssignment>();
    const refetchResponse = deferred<TaskLabelAssignment>();
    let refetchCount = 0;
    const controller = createTaskLabelAssignmentController({
      taskID: "task-1",
      initialLabelIDs: [],
      update: async () => updateResponse.promise,
      refetch: async () => {
        refetchCount += 1;
        return refetchResponse.promise;
      },
    });

    controller.setDesired(priorityID, true);
    controller.markDirty();
    controller.markDirty();

    expect(refetchCount).toBe(0);
    expect(controller.getSnapshot().dirty).toBe(true);

    updateResponse.resolve({
      taskID: "task-1",
      labelIDs: [priorityID],
    });
    await waitFor(() => {
      expect(refetchCount).toBe(1);
    });
    expect(controller.getSnapshot().dirty).toBe(false);

    refetchResponse.resolve({
      taskID: "task-1",
      labelIDs: [priorityID, urgentID],
    });
    await waitFor(() => {
      expect(controller.getSnapshot()).toMatchObject({
        authoritativeLabelIDs: [priorityID, urgentID],
        visibleLabelIDs: [priorityID, urgentID],
        dirty: false,
      });
    });
    expect(refetchCount).toBe(1);
  });

  it("surfaces a failed reconciliation until an explicit Retry succeeds", async () => {
    const retryResponse = deferred<TaskLabelAssignment>();
    const reconciliationError = new Error("offline");
    let refetchCount = 0;
    const controller = createTaskLabelAssignmentController({
      taskID: "task-1",
      initialLabelIDs: [priorityID],
      update: async () => ({
        taskID: "task-1",
        labelIDs: [priorityID],
      }),
      refetch: async () => {
        refetchCount += 1;
        if (refetchCount === 1) {
          throw reconciliationError;
        }
        return retryResponse.promise;
      },
    });

    controller.markDirty();

    await waitFor(() => {
      expect(controller.getSnapshot().reconciliationFailure?.error).toBe(reconciliationError);
    });
    expect(refetchCount).toBe(1);

    controller.retryReconciliation();
    expect(controller.getSnapshot().reconciliationFailure).toBeNull();
    await waitFor(() => {
      expect(refetchCount).toBe(2);
    });

    retryResponse.resolve({
      taskID: "task-1",
      labelIDs: [priorityID, urgentID],
    });
    await waitFor(() => {
      expect(controller.getSnapshot()).toMatchObject({
        authoritativeLabelIDs: [priorityID, urgentID],
        reconciliationFailure: null,
      });
    });
  });

  it("continues local assignment work while reconciliation Retry remains actionable", async () => {
    const updateResponse = deferred<TaskLabelAssignment>();
    const reconciliationError = new Error("offline");
    let updateCount = 0;
    const controller = createTaskLabelAssignmentController({
      taskID: "task-1",
      initialLabelIDs: [],
      update: async () => {
        updateCount += 1;
        return updateResponse.promise;
      },
      refetch: async () => {
        throw reconciliationError;
      },
    });

    controller.markDirty();
    await waitFor(() => {
      expect(controller.getSnapshot().reconciliationFailure?.error).toBe(reconciliationError);
    });

    controller.setDesired(priorityID, true);

    expect(updateCount).toBe(1);
    expect(controller.getSnapshot()).toMatchObject({
      visibleLabelIDs: [priorityID],
      inFlightLabelID: priorityID,
      reconciliationFailure: {
        error: reconciliationError,
      },
    });
  });

  it("prevents a late assignment response from restoring a deleted label", async () => {
    const response = deferred<TaskLabelAssignment>();
    const controller = createTaskLabelAssignmentController({
      taskID: "task-1",
      initialLabelIDs: [],
      update: async () => response.promise,
      refetch: async () => ({
        taskID: "task-1",
        labelIDs: [],
      }),
    });

    controller.setDesired(priorityID, true);
    controller.deleteLabel(priorityID);

    expect(controller.getSnapshot()).toMatchObject({
      authoritativeLabelIDs: [],
      visibleLabelIDs: [],
      pendingLabelIDs: [],
    });

    response.resolve({
      taskID: "task-1",
      labelIDs: [priorityID],
    });
    await waitFor(() => {
      expect(controller.getSnapshot()).toMatchObject({
        authoritativeLabelIDs: [],
        visibleLabelIDs: [],
        inFlightLabelID: null,
      });
    });
  });

  it("clears reconciled delete tombstones so historical catalog churn stays bounded", async () => {
    const controller = createTaskLabelAssignmentController({
      taskID: "task-1",
      initialLabelIDs: [],
      update: async () => ({
        taskID: "task-1",
        labelIDs: [],
      }),
      refetch: async () => ({
        taskID: "task-1",
        labelIDs: [],
      }),
    });

    for (let index = 0; index < 100; index += 1) {
      controller.deleteLabel(`00000000-0000-4000-8000-${(index + 1).toString(16).padStart(12, "0")}`);
    }
    controller.replaceAuthoritative(await controller.readAuthoritative());
    controller.deleteLabel("00000000-0000-4000-8000-000000000065");

    expect(controller.getSnapshot()).toMatchObject({
      authoritativeLabelIDs: [],
      visibleLabelIDs: [],
      closed: false,
    });
  });

  it("rejects a stale response through the current bounded catalog after historical deletions", async () => {
    const response = deferred<TaskLabelAssignment>();
    const controller = createTaskLabelAssignmentController({
      taskID: "task-1",
      initialLabelIDs: [],
      availableLabelIDs: [priorityID],
      update: async () => response.promise,
      refetch: async () => ({
        taskID: "task-1",
        labelIDs: [],
      }),
    });

    controller.setDesired(priorityID, true);
    controller.replaceAvailableLabelIDs([]);
    for (let index = 0; index < 101; index += 1) {
      controller.deleteLabel(`00000000-0000-4000-8000-${(index + 1).toString(16).padStart(12, "0")}`);
    }
    controller.deleteLabel(priorityID);

    response.resolve({
      taskID: "task-1",
      labelIDs: [priorityID],
    });
    await waitFor(() => {
      expect(controller.getSnapshot()).toMatchObject({
        authoritativeLabelIDs: [],
        visibleLabelIDs: [],
        inFlightLabelID: null,
      });
    });
  });

  it("keeps a newer available-label intent when an older stale response arrives", async () => {
    const addResponse = deferred<TaskLabelAssignment>();
    const removeResponse = deferred<TaskLabelAssignment>();
    const updates: TaskLabelUpdateInput[] = [];
    const controller = createTaskLabelAssignmentController({
      taskID: "task-1",
      initialLabelIDs: [],
      update: async (input) => {
        updates.push(input);
        return updates.length === 1 ? addResponse.promise : removeResponse.promise;
      },
      refetch: async () => ({
        taskID: "task-1",
        labelIDs: [],
      }),
    });

    controller.setDesired(priorityID, true);
    controller.setDesired(priorityID, false);
    controller.replaceAvailableLabelIDs(availableLabelIDs);
    controller.replaceAuthoritative({
      taskID: "task-1",
      labelIDs: [priorityID],
    });

    expect(controller.getSnapshot()).toMatchObject({
      authoritativeLabelIDs: [priorityID],
      visibleLabelIDs: [],
      pendingLabelIDs: [priorityID],
      inFlightLabelID: priorityID,
    });
  });

  it("closes terminally on task deletion and ignores late responses and rollback", async () => {
    const response = deferred<TaskLabelAssignment>();
    const controller = createTaskLabelAssignmentController({
      taskID: "task-1",
      initialLabelIDs: [],
      update: async () => response.promise,
      refetch: async () => ({
        taskID: "task-1",
        labelIDs: [],
      }),
    });

    controller.setDesired(priorityID, true);
    controller.deleteTask();

    expect(controller.getSnapshot()).toMatchObject({
      authoritativeLabelIDs: [],
      visibleLabelIDs: [],
      pendingLabelIDs: [],
      inFlightLabelID: null,
      failures: [],
      closed: true,
    });
    controller.setDesired(urgentID, true);
    controller.retry(priorityID);
    controller.markDirty();

    response.reject(new Error("task deleted"));
    await Promise.resolve();

    expect(controller.getSnapshot()).toMatchObject({
      authoritativeLabelIDs: [],
      visibleLabelIDs: [],
      pendingLabelIDs: [],
      inFlightLabelID: null,
      failures: [],
      dirty: false,
      closed: true,
    });
  });

  it("bounds the coalesced pending map at the Project catalog limit", () => {
    const response = deferred<TaskLabelAssignment>();
    let updateCount = 0;
    const controller = createTaskLabelAssignmentController({
      taskID: "task-1",
      initialLabelIDs: [],
      availableLabelIDs: Array.from(
        { length: 100 },
        (_, index) => `00000000-0000-4000-8000-${(index + 1).toString(16).padStart(12, "0")}`,
      ),
      update: async () => {
        updateCount += 1;
        return response.promise;
      },
      refetch: async () => ({
        taskID: "task-1",
        labelIDs: [],
      }),
    });
    const labelIDs = Array.from(
      { length: 101 },
      (_, index) => `00000000-0000-4000-8000-${(index + 1).toString(16).padStart(12, "0")}`,
    );

    for (const labelID of labelIDs.slice(0, 100)) {
      controller.setDesired(labelID, true);
    }

    const unavailableLabelID = labelIDs[100];
    if (unavailableLabelID === undefined) {
      throw new Error("test label ID was not generated");
    }
    controller.setDesired(unavailableLabelID, true);
    expect(controller.getSnapshot().pendingLabelIDs).toHaveLength(100);
    expect(updateCount).toBe(1);
  });

  it("keeps assignment activity and failures isolated by Task", async () => {
    const firstResponse = deferred<TaskLabelAssignment>();
    const secondResponse = deferred<TaskLabelAssignment>();
    const first = createTaskLabelAssignmentController({
      taskID: "task-1",
      initialLabelIDs: [],
      update: async () => firstResponse.promise,
      refetch: async () => ({
        taskID: "task-1",
        labelIDs: [],
      }),
    });
    const second = createTaskLabelAssignmentController({
      taskID: "task-2",
      initialLabelIDs: [],
      update: async () => secondResponse.promise,
      refetch: async () => ({
        taskID: "task-2",
        labelIDs: [],
      }),
    });

    first.setDesired(priorityID, true);
    second.setDesired(urgentID, true);
    firstResponse.reject(new Error("task-1 offline"));

    await waitFor(() => {
      expect(first.getSnapshot().failures).toHaveLength(1);
    });
    expect(second.getSnapshot()).toMatchObject({
      taskID: "task-2",
      visibleLabelIDs: [urgentID],
      inFlightLabelID: urgentID,
      failures: [],
      closed: false,
    });

    first.deleteTask();
    secondResponse.resolve({
      taskID: "task-2",
      labelIDs: [urgentID],
    });

    await waitFor(() => {
      expect(second.getSnapshot()).toMatchObject({
        authoritativeLabelIDs: [urgentID],
        visibleLabelIDs: [urgentID],
        inFlightLabelID: null,
        failures: [],
        closed: false,
      });
    });
  });
});

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  reject(error: unknown): void;
  resolve(value: T): void;
}> {
  let resolve: ((value: T) => void) | null = null;
  let reject: ((error: unknown) => void) | null = null;
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve;
    reject = nextReject;
  });
  return {
    promise,
    reject(error: unknown): void {
      reject?.(error);
    },
    resolve(value: T): void {
      resolve?.(value);
    },
  };
}
