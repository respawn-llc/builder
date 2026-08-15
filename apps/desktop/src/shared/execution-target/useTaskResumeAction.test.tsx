import { act, renderHook } from "@testing-library/react";
import { vi } from "vitest";

import { resumeTaskInitiatingAction, type TaskInitiatingActionController } from "@/shared/execution-target";
import { useTaskResumeAction } from "./useTaskResumeAction";

it("routes Resume through the target-continuation controller", async () => {
  const run = vi.fn<TaskInitiatingActionController["run"]>().mockResolvedValue(undefined);
  const controller: TaskInitiatingActionController = {
    pending: null,
    running: false,
    run,
    close: vi.fn(),
    selectMode: vi.fn(),
    setCustomRef: vi.fn(),
  };
  const { result } = renderHook(() => useTaskResumeAction(controller));

  await act(async () => {
    await result.current.execute("task-1");
  });

  expect(run).toHaveBeenCalledOnce();
  expect(run.mock.calls[0]?.[0]).toMatchObject({ kind: "resume", taskID: "task-1" });
  expect(result.current.pendingTaskIDs).toEqual(new Set());
});

it("continues Resume with the original action and selected target", async () => {
  const run = vi.fn<TaskInitiatingActionController["run"]>().mockResolvedValue(undefined);
  const controller: TaskInitiatingActionController = {
    pending: null,
    running: false,
    run,
    close: vi.fn(),
    selectMode: vi.fn(),
    setCustomRef: vi.fn(),
  };
  const { result } = renderHook(() => useTaskResumeAction(controller));
  const action = resumeTaskInitiatingAction("task-1");
  const selection = { mode: "custom_ref", customRef: "release/v1" } as const;

  await act(async () => {
    await result.current.continueExecution(action, selection);
  });

  expect(run).toHaveBeenCalledWith(action, selection);
  expect(result.current.pendingTaskIDs).toEqual(new Set());
});
