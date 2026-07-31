import { act } from "@testing-library/react";
import { vi } from "vitest";

export function installAnimationFrameTestSupport(): void {
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    return window.setTimeout(() => {
      callback(performance.now());
    }, 0);
  });
  vi.stubGlobal("cancelAnimationFrame", (handle: number) => {
    window.clearTimeout(handle);
  });
}

export async function waitForMacrotask(): Promise<void> {
  await new Promise<void>((resolve) => {
    window.setTimeout(resolve, 0);
  });
}

export async function flushQueuedWork(): Promise<void> {
  await act(async () => {
    await waitForMacrotask();
  });
}
