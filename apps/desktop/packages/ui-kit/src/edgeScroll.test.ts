import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createEdgeScrollDriver, type EdgeScrollMotion } from "./edgeScroll";

describe("edge-scroll driver", () => {
  let callbacks: Map<number, FrameRequestCallback>;
  let nextFrameID: number;

  beforeEach(() => {
    callbacks = new Map();
    nextFrameID = 0;
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
      const frameID = ++nextFrameID;
      callbacks.set(frameID, callback);
      return frameID;
    });
    vi.spyOn(window, "cancelAnimationFrame").mockImplementation((frameID) => {
      callbacks.delete(frameID);
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("schedules no frame without motion and only one frame while motion is active", () => {
    let motion: readonly EdgeScrollMotion[] | null = null;
    const driver = createEdgeScrollDriver(() => motion);

    driver.refresh();
    expect(callbacks).toHaveLength(0);

    motion = [{ axis: "y", element: scrollport(), velocity: 900 }];
    driver.refresh();
    driver.refresh();
    expect(callbacks).toHaveLength(1);
  });

  it("applies bounded motion and stops when the target can no longer move", () => {
    const element = scrollport();
    const motion: readonly EdgeScrollMotion[] = [
      { axis: "y", element, velocity: 10_000 },
    ];
    const driver = createEdgeScrollDriver(() => motion);

    driver.refresh();
    releaseFrame(1_000);

    expect(element.scrollTop).toBeLessThanOrEqual(43.2);
    expect(callbacks).toHaveLength(1);

    element.scrollTop = element.scrollHeight - element.clientHeight;
    releaseFrame(2_000);
    expect(callbacks).toHaveLength(0);
  });

  it("moves in the requested direction with a bounded frame delta", () => {
    const element = scrollport();
    element.scrollTop = 200;
    const motion: readonly EdgeScrollMotion[] = [
      { axis: "y", element, velocity: -10_000 },
    ];
    const driver = createEdgeScrollDriver(() => motion);

    driver.refresh();
    releaseFrame(10_000);

    expect(element.scrollTop).toBeGreaterThanOrEqual(156.8);
    expect(element.scrollTop).toBeLessThan(200);
  });

  it("cancels its pending frame and clears future writes on stop", () => {
    const element = scrollport();
    let motion: readonly EdgeScrollMotion[] | null = [
      { axis: "y", element, velocity: 900 },
    ];
    const driver = createEdgeScrollDriver(() => motion);

    driver.refresh();
    driver.stop();
    motion = [{ axis: "y", element, velocity: 900 }];
    releaseAllFrames(1_000);

    expect(element.scrollTop).toBe(0);
    expect(callbacks).toHaveLength(0);
  });

  function releaseFrame(timestamp: number): void {
    const pending = firstFrame(callbacks);
    if (pending === undefined) {
      throw new Error("expected a pending animation frame");
    }
    callbacks.delete(pending[0]);
    pending[1](timestamp);
  }

  function releaseAllFrames(timestamp: number): void {
    for (const [frameID, callback] of callbacks) {
      callbacks.delete(frameID);
      callback(timestamp);
    }
  }
});

function firstFrame(
  callbacks: ReadonlyMap<number, FrameRequestCallback>,
): readonly [number, FrameRequestCallback] | undefined {
  for (const entry of callbacks) {
    return entry;
  }
  return undefined;
}

function scrollport(): HTMLElement {
  const element = document.createElement("div");
  Object.defineProperties(element, {
    clientHeight: { configurable: true, value: 100 },
    scrollHeight: { configurable: true, value: 500 },
    scrollTop: { configurable: true, value: 0, writable: true },
  });
  return element;
}
