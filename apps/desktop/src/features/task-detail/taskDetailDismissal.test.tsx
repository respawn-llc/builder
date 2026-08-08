import { act, renderHook } from "@testing-library/react";
import { vi } from "vitest";

import { useExactTaskDetailDeleteDismissal } from "./taskDetailDismissal";

it("dismisses the current standalone Task host and propagates navigation failure", async () => {
  const dismiss = vi.fn(async () => undefined);
  const { result } = renderHook(() => useExactTaskDetailDeleteDismissal("task-1", dismiss));

  await expect(result.current()).resolves.toEqual({ kind: "accepted" });
  expect(dismiss).toHaveBeenCalledOnce();

  dismiss.mockRejectedValueOnce(new Error("navigation failed"));
  const failed = await result.current();
  expect(failed.kind).toBe("failed");
  if (failed.kind !== "failed") {
    throw new Error("Expected failed dismissal.");
  }
  expect(failed.error).toBeInstanceOf(Error);
  if (!(failed.error instanceof Error)) {
    throw new Error("Expected Error dismissal cause.");
  }
  expect(failed.error.message).toBe("navigation failed");
});

it("returns stale after the native Task host is replaced or released", async () => {
  const dismiss = vi.fn(async () => undefined);
  const { result, rerender, unmount } = renderHook(
    ({ taskID }) => useExactTaskDetailDeleteDismissal(taskID, dismiss),
    { initialProps: { taskID: "task-1" } },
  );
  const replacedHostDismissal = result.current;

  rerender({ taskID: "task-2" });
  await expect(replacedHostDismissal()).resolves.toEqual({ kind: "stale" });
  expect(dismiss).not.toHaveBeenCalled();

  const releasedHostDismissal = result.current;
  act(() => {
    unmount();
  });
  await expect(releasedHostDismissal()).resolves.toEqual({ kind: "stale" });
  expect(dismiss).not.toHaveBeenCalled();
});
