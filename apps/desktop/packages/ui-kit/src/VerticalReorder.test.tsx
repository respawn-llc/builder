import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { canScrollEdge, createEdgeScrollDriver, edgeScrollVelocity, type EdgeScrollMotion } from "./edgeScroll";
import { projectVerticalReorder } from "./reorderProjection";

describe("projectVerticalReorder", () => {
  it("keeps activation in the source slot until a destination is crossed", () => {
    expect(projectVerticalReorder(["first", "second", "third"], "second", "second")).toBeNull();
  });

  it("maps one adjacent destination to the committed order", () => {
    expect(projectVerticalReorder(["first", "second", "third"], "second", "third")).toEqual([
      "first",
      "third",
      "second",
    ]);
  });
});

describe("edge scroll calculations", () => {
  it("uses one quadratic edge velocity curve for both scroll directions", () => {
    expect(edgeScrollVelocity(0, 0, 200)).toBe(-900);
    expect(edgeScrollVelocity(36, 0, 200)).toBe(-225);
    expect(edgeScrollVelocity(128, 0, 200)).toBe(0);
    expect(edgeScrollVelocity(164, 0, 200)).toBe(225);
  });

  it("reports whether an edge motion can move its scrollport", () => {
    const element = edgeScrollTestScrollport();
    expect(canScrollEdge(element, "y", -900)).toBe(false);
    expect(canScrollEdge(element, "y", 900)).toBe(true);
    element.scrollTop = element.scrollHeight - element.clientHeight;
    expect(canScrollEdge(element, "y", 900)).toBe(false);
  });
});

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

  it("applies every active motion in one frame", () => {
    const first = edgeScrollTestScrollport();
    const second = edgeScrollTestScrollport();
    const motion: readonly EdgeScrollMotion[] = [
      { axis: "y", element: first, velocity: 900 },
      { axis: "y", element: second, velocity: 900 },
    ];
    const driver = createEdgeScrollDriver(() => motion);

    driver.refresh();
    releaseEdgeScrollFrame(callbacks, 1_000);

    expect(first.scrollTop).toBeGreaterThan(0);
    expect(second.scrollTop).toBeGreaterThan(0);
  });
});

function releaseEdgeScrollFrame(callbacks: Map<number, FrameRequestCallback>, timestamp: number): void {
  const pending = callbacks.entries().next();
  if (pending.done) {
    throw new Error("expected a pending animation frame");
  }
  callbacks.delete(pending.value[0]);
  pending.value[1](timestamp);
}

function edgeScrollTestScrollport(): HTMLElement {
  const element = document.createElement("div");
  Object.defineProperties(element, {
    clientHeight: { configurable: true, value: 100 },
    scrollHeight: { configurable: true, value: 500 },
    scrollTop: { configurable: true, value: 0, writable: true },
  });
  return element;
}
