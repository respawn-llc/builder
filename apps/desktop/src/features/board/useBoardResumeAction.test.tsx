import { act, renderHook } from "@testing-library/react";
import { vi } from "vitest";

import { newSetupOperationID } from "@/api";
import type { TaskInitiatingActionController } from "@/shared/execution-target";
import { useBoardResumeAction } from "./useBoardResumeAction";

describe("useBoardResumeAction", () => {
  it("routes Board Resume through the initiating-action controller", async () => {
    const run = vi.fn<TaskInitiatingActionController["run"]>().mockResolvedValue(undefined);
    const controller: TaskInitiatingActionController = {
      pending: null,
      running: false,
      run,
      close: vi.fn(),
      selectMode: vi.fn(),
      setCustomRef: vi.fn(),
    };
    const { result } = renderHook(() => useBoardResumeAction(controller));

    act(() => {
      result.current.execute("task-1");
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(run).toHaveBeenCalledOnce();
    expect(run.mock.calls[0]?.[0]).toMatchObject({
      kind: "resume",
      taskID: "task-1",
    });
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
    const { result } = renderHook(() => useBoardResumeAction(controller));
    const action = {
      kind: "resume" as const,
      taskID: "task-1",
      setupOperationID: newSetupOperationID(),
    };
    const selection = { mode: "none" as const, customRef: null };

    act(() => {
      result.current.continueExecution(action, selection);
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(run).toHaveBeenCalledWith(action, selection);
  });

  it("disables a second Resume while the first controller operation is running", async () => {
    let release!: () => void;
    const run = vi.fn<TaskInitiatingActionController["run"]>().mockImplementation(async () => {
      await new Promise<void>((resolve) => {
        release = resolve;
      });
    });
    const controller: TaskInitiatingActionController = {
      pending: null,
      running: false,
      run,
      close: vi.fn(),
      selectMode: vi.fn(),
      setCustomRef: vi.fn(),
    };
    const { result } = renderHook(() => useBoardResumeAction(controller));

    act(() => {
      result.current.execute("task-1");
      result.current.execute("task-2");
    });

    expect(run).toHaveBeenCalledOnce();
    expect(run.mock.calls[0]?.[0]).toMatchObject({ taskID: "task-1" });
    expect(result.current.actionsDisabled).toBe(true);
    expect(result.current.pendingTaskIDs).toEqual(new Set(["task-1"]));

    await act(async () => {
      release();
      await Promise.resolve();
    });

    expect(result.current.actionsDisabled).toBe(false);
    expect(result.current.pendingTaskIDs).toEqual(new Set());
  });
});
