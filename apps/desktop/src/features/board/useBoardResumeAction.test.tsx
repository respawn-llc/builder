import { act, renderHook } from "@testing-library/react";
import { vi } from "vitest";

import type { TaskInitiatingActionController } from "@/shared/execution-target";
import { useBoardResumeAction } from "./useBoardResumeAction";

it("routes Board Resume through the target-continuation controller", async () => {
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

  await act(async () => {
    await result.current.execute("task-1");
  });

  expect(run).toHaveBeenCalledOnce();
  expect(run.mock.calls[0]?.[0]).toMatchObject({ kind: "resume", taskID: "task-1" });
  expect(result.current.pendingTaskIDs).toEqual(new Set());
});
