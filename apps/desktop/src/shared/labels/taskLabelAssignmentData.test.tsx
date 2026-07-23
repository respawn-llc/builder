import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, waitFor } from "@testing-library/react";
import { StrictMode, type ReactNode } from "react";
import { vi } from "vitest";

import type { TaskLabelAssignment } from "@/api";
import {
  type TaskLabelAssignmentData,
  useManagedTaskLabelAssignment,
} from "./taskLabelAssignmentData";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const urgentID = "942495c2-5958-4959-8445-94046ad74fbd";
const appServiceMocks = vi.hoisted(() => {
  const updateTaskLabels = vi.fn();
  return {
    services: { api: { updateTaskLabels } },
    updateTaskLabels,
  };
});

vi.mock("@/app-facade", async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return {
    ...actual,
    useAppServices: () => appServiceMocks.services,
  };
});

describe("useManagedTaskLabelAssignment", () => {
  it("exposes the assignment on the first render and retains optimistic work across remounts", async () => {
    const update = deferred<TaskLabelAssignment>();
    appServiceMocks.updateTaskLabels.mockReset();
    appServiceMocks.updateTaskLabels.mockReturnValue(update.promise);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const observations: TaskLabelAssignmentData[] = [];
    const availableLabelIDs = [priorityID, urgentID];
    const initialAssignment = {
      taskID: "task-1",
      labelIDs: [priorityID],
    };

    function Probe(): ReactNode {
      observations.push(
        useManagedTaskLabelAssignment({
          availableLabelIDs,
          initialAssignment,
          projectID: "project-1",
          taskID: "task-1",
          workflowID: "workflow-1",
        }),
      );
      return null;
    }

    function TestRoot({ mounted }: Readonly<{ mounted: boolean }>): ReactNode {
      return (
        <QueryClientProvider client={queryClient}>
          <StrictMode>{mounted ? <Probe /> : null}</StrictMode>
        </QueryClientProvider>
      );
    }

    const view = render(<TestRoot mounted />);

    const firstRender = observations[0];
    if (firstRender === undefined) {
      throw new Error("task label assignment was unavailable on the first render");
    }
    expect(firstRender.snapshot.visibleLabelIDs).toEqual([priorityID]);

    const active = observations.at(-1);
    if (active === undefined) {
      throw new Error("task label assignment was unavailable after mount");
    }
    act(() => {
      active.controller.setDesired(urgentID, true);
    });
    expect(active.controller.getSnapshot()).toMatchObject({
      visibleLabelIDs: [priorityID, urgentID],
      pendingLabelIDs: [urgentID],
    });

    view.rerender(<TestRoot mounted={false} />);
    observations.length = 0;
    view.rerender(<TestRoot mounted />);

    await waitFor(() => {
      expect(observations.at(-1)?.controller).toBe(active.controller);
      expect(observations.at(-1)?.snapshot).toMatchObject({
        visibleLabelIDs: [priorityID, urgentID],
        pendingLabelIDs: [urgentID],
      });
    });

    update.resolve({
      taskID: "task-1",
      labelIDs: [priorityID, urgentID],
    });
    await waitFor(() => {
      expect(active.controller.getSnapshot().pendingLabelIDs).toEqual([]);
    });
  });

  it("keeps disabled consumers independent from an unleased shared controller", async () => {
    appServiceMocks.updateTaskLabels.mockReset();
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const availableLabelIDs = [priorityID, urgentID];
    const enabledAssignment = {
      taskID: "task-2",
      labelIDs: [priorityID],
    };
    const disabledAssignment = {
      taskID: "task-2",
      labelIDs: [urgentID],
    };
    let enabledData: TaskLabelAssignmentData | undefined;
    let disabledData: TaskLabelAssignmentData | undefined;

    function EnabledProbe(): ReactNode {
      enabledData = useManagedTaskLabelAssignment({
        availableLabelIDs,
        initialAssignment: enabledAssignment,
        projectID: "project-1",
        taskID: "task-2",
        workflowID: "workflow-1",
      });
      return null;
    }

    function DisabledProbe(): ReactNode {
      disabledData = useManagedTaskLabelAssignment({
        availableLabelIDs,
        enabled: false,
        initialAssignment: disabledAssignment,
        projectID: "project-1",
        taskID: "task-2",
        workflowID: "workflow-1",
      });
      return null;
    }

    function TestRoot({ enabledMounted }: Readonly<{ enabledMounted: boolean }>): ReactNode {
      return (
        <QueryClientProvider client={queryClient}>
          <StrictMode>
            {enabledMounted ? <EnabledProbe /> : null}
            <DisabledProbe />
          </StrictMode>
        </QueryClientProvider>
      );
    }

    const view = render(<TestRoot enabledMounted />);

    await waitFor(() => {
      expect(enabledData?.snapshot.visibleLabelIDs).toEqual([priorityID]);
      expect(disabledData?.snapshot.visibleLabelIDs).toEqual([urgentID]);
      expect(disabledData?.controller).not.toBe(enabledData?.controller);
    });

    const disabledController = disabledData?.controller;
    view.rerender(<TestRoot enabledMounted={false} />);

    await waitFor(() => {
      expect(disabledData?.controller).toBe(disabledController);
      expect(disabledData?.snapshot.visibleLabelIDs).toEqual([urgentID]);
    });
    expect(appServiceMocks.updateTaskLabels).not.toHaveBeenCalled();
  });
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((promiseResolve) => {
    resolve = promiseResolve;
  });
  return { promise, resolve };
}
