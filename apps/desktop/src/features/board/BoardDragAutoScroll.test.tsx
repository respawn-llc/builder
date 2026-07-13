import { render, screen } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  BOARD_DRAG_AUTOSCROLL_EDGE_ZONE_PX,
  BOARD_DRAG_AUTOSCROLL_MAX_FRAME_DELTA_MS,
  BOARD_DRAG_AUTOSCROLL_MAX_SPEED_PX_PER_SECOND,
  BoardDragAutoScrollController,
  boardDragEdgeVelocity,
  normalizedBoardDragFrameDeltaMs,
  useBoardDragAutoScroll,
} from "./BoardDragAutoScroll";

describe("board drag auto-scroll velocity", () => {
  it("is zero outside the 72px edge zones and accelerates monotonically toward every edge", () => {
    const start = 100;
    const end = 500;
    expect(boardDragEdgeVelocity(start + BOARD_DRAG_AUTOSCROLL_EDGE_ZONE_PX, start, end)).toBe(0);
    expect(boardDragEdgeVelocity(end - BOARD_DRAG_AUTOSCROLL_EDGE_ZONE_PX, start, end)).toBe(0);

    const leftGentle = boardDragEdgeVelocity(171, 100, 500);
    const leftNear = boardDragEdgeVelocity(136, 100, 500);
    const leftEdge = boardDragEdgeVelocity(100, 100, 500);
    expect(leftGentle).toBeLessThan(0);
    expect(leftNear).toBeLessThan(leftGentle);
    expect(leftEdge).toBeLessThan(leftNear);

    const rightGentle = boardDragEdgeVelocity(429, 100, 500);
    const rightNear = boardDragEdgeVelocity(464, 100, 500);
    const rightEdge = boardDragEdgeVelocity(500, 100, 500);
    expect(rightGentle).toBeGreaterThan(0);
    expect(rightNear).toBeGreaterThan(rightGentle);
    expect(rightEdge).toBeGreaterThan(rightNear);
    expect(Math.abs(leftEdge)).toBeLessThanOrEqual(BOARD_DRAG_AUTOSCROLL_MAX_SPEED_PX_PER_SECOND);
    expect(rightEdge).toBeLessThanOrEqual(BOARD_DRAG_AUTOSCROLL_MAX_SPEED_PX_PER_SECOND);
  });

  it("normalizes frame deltas and caps suspended frames", () => {
    expect(normalizedBoardDragFrameDeltaMs(16)).toBe(16);
    expect(normalizedBoardDragFrameDeltaMs(0)).toBe(0);
    expect(normalizedBoardDragFrameDeltaMs(1_000)).toBe(BOARD_DRAG_AUTOSCROLL_MAX_FRAME_DELTA_MS);
  });
});

describe("BoardDragAutoScrollController", () => {
  let frames: FakeAnimationFrames;

  beforeEach(() => {
    frames = new FakeAnimationFrames();
  });

  it("owns one frame loop and scrolls horizontal and selected vertical ports together", () => {
    const root = scrollport({ left: 0, top: 0, width: 500, height: 400, scrollWidth: 1_000, scrollHeight: 400 });
    const column = scrollport({ left: 300, top: 0, width: 180, height: 400, scrollWidth: 180, scrollHeight: 1_000 });
    const controller = controllerFor(root, frames);

    controller.registerColumnScrollport("column-1", column);
    controller.setActive(true);
    controller.updatePointer({ clientX: 476, clientY: 396 });
    controller.updatePointer({ clientX: 476, clientY: 396 });

    expect(frames.pending).toBe(1);
    frames.step(0);
    frames.step(16);

    expect(root.scrollLeft).toBeGreaterThan(0);
    expect(column.scrollTop).toBeGreaterThan(0);
    expect(frames.pending).toBe(1);
  });

  it("keeps horizontal motion when no registered column contains the drag", () => {
    const root = scrollport({ left: 0, top: 0, width: 500, height: 400, scrollWidth: 1_000, scrollHeight: 400 });
    const controller = controllerFor(root, frames);

    controller.setActive(true);
    controller.updatePointer({ clientX: 496, clientY: 200 });
    frames.step(0);
    frames.step(16);

    expect(root.scrollLeft).toBeGreaterThan(0);
  });

  it("keeps vertical motion when the horizontal board reaches its boundary", () => {
    const root = scrollport({ left: 0, top: 0, width: 500, height: 400, scrollWidth: 1_000, scrollHeight: 400 });
    const column = scrollport({ left: 300, top: 0, width: 180, height: 400, scrollWidth: 180, scrollHeight: 1_000 });
    root.scrollLeft = 500;
    const controller = controllerFor(root, frames);

    controller.registerColumnScrollport("column-1", column);
    controller.setActive(true);
    controller.updatePointer({ clientX: 476, clientY: 396 });
    frames.step(0);
    frames.step(16);

    expect(root.scrollLeft).toBe(500);
    expect(column.scrollTop).toBeGreaterThan(0);
  });

  it("switches vertical targets as the pointer crosses columns", () => {
    const root = scrollport({ left: 0, top: 0, width: 500, height: 400, scrollWidth: 1_000, scrollHeight: 400 });
    const upperColumn = scrollport({ left: 300, top: 0, width: 180, height: 190, scrollWidth: 180, scrollHeight: 1_000 });
    const lowerColumn = scrollport({ left: 300, top: 210, width: 180, height: 190, scrollWidth: 180, scrollHeight: 1_000 });
    const controller = controllerFor(root, frames);

    controller.registerColumnScrollport("upper", upperColumn);
    controller.registerColumnScrollport("lower", lowerColumn);
    upperColumn.scrollTop = 300;
    controller.setActive(true);
    controller.updatePointer({ clientX: 350, clientY: 4 });
    frames.step(0);
    frames.step(16);
    expect(upperColumn.scrollTop).toBeLessThan(300);
    expect(lowerColumn.scrollTop).toBe(0);

    controller.updatePointer({ clientX: 350, clientY: 396 });
    frames.step(32);
    expect(lowerColumn.scrollTop).toBeGreaterThan(0);
  });

  it("stops immediately for neutral pointers, scroll bounds, disable, and destroy", () => {
    const root = scrollport({ left: 0, top: 0, width: 500, height: 400, scrollWidth: 1_000, scrollHeight: 400 });
    const controller = controllerFor(root, frames);

    controller.setActive(true);
    controller.updatePointer({ clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(1);
    controller.updatePointer({ clientX: 250, clientY: 200 });
    expect(frames.pending).toBe(0);

    root.scrollLeft = 500;
    controller.updatePointer({ clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(0);

    root.scrollLeft = 0;
    controller.updatePointer({ clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(1);
    controller.setActive(false);
    expect(frames.pending).toBe(0);

    controller.setActive(true);
    controller.updatePointer({ clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(1);
    controller.destroy();
    expect(frames.pending).toBe(0);
  });
});

describe("useBoardDragAutoScroll lifecycle", () => {
  let frames: FakeAnimationFrames;

  beforeEach(() => {
    frames = new FakeAnimationFrames();
    vi.stubGlobal("requestAnimationFrame", frames.request);
    vi.stubGlobal("cancelAnimationFrame", frames.cancel);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("ignores nested root dragleave transitions and stops on board exit", () => {
    render(<AutoScrollHarness active />);
    const root = screen.getByTestId("auto-scroll-root");
    const child = screen.getByTestId("auto-scroll-child");
    setScrollportGeometry(root, { left: 0, top: 0, width: 500, height: 400, scrollWidth: 1_000, scrollHeight: 400 });

    dispatchDrag(root, "dragover", { clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(1);

    dispatchDrag(root, "dragleave", { relatedTarget: child });
    expect(frames.pending).toBe(1);

    dispatchDrag(root, "dragleave", { relatedTarget: document.body });
    expect(frames.pending).toBe(0);
  });

  it.each([
    ["dragover", {}],
    ["dragleave", { relatedTarget: null }],
    ["drop", {}],
  ] as const)("stops from capture-phase document %s", (type, init) => {
    render(<AutoScrollHarness active />);
    const root = screen.getByTestId("auto-scroll-root");
    setScrollportGeometry(root, { left: 0, top: 0, width: 500, height: 400, scrollWidth: 1_000, scrollHeight: 400 });

    dispatchDrag(root, "dragover", { clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(1);

    dispatchDrag(document, type, init);
    expect(frames.pending).toBe(0);
  });

  it.each(["release", "cancellation"])("stops from capture-phase document dragend on %s", () => {
    render(<AutoScrollHarness active />);
    const root = screen.getByTestId("auto-scroll-root");
    setScrollportGeometry(root, { left: 0, top: 0, width: 500, height: 400, scrollWidth: 1_000, scrollHeight: 400 });

    dispatchDrag(root, "dragover", { clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(1);

    dispatchDrag(document, "dragend");
    expect(frames.pending).toBe(0);
  });

  it("stops on hook unmount", () => {
    const view = render(<AutoScrollHarness active />);
    const root = screen.getByTestId("auto-scroll-root");
    setScrollportGeometry(root, { left: 0, top: 0, width: 500, height: 400, scrollWidth: 1_000, scrollHeight: 400 });

    dispatchDrag(root, "dragover", { clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(1);

    view.unmount();
    expect(frames.pending).toBe(0);
  });

  it("treats window blur as supplemental cleanup", () => {
    render(<AutoScrollHarness active />);
    const root = screen.getByTestId("auto-scroll-root");
    setScrollportGeometry(root, { left: 0, top: 0, width: 500, height: 400, scrollWidth: 1_000, scrollHeight: 400 });

    dispatchDrag(root, "dragover", { clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(1);

    window.dispatchEvent(new Event("blur"));
    expect(frames.pending).toBe(0);
  });
});

function AutoScrollHarness({ active }: Readonly<{ active: boolean }>) {
  const rootRef = useRef<HTMLDivElement | null>(null);
  const { onBoardDragLeave, onBoardDragOver, registerColumnScrollport } = useBoardDragAutoScroll({
    active,
    rootRef,
  });
  return (
    <div
      data-testid="auto-scroll-root"
      onDragLeave={onBoardDragLeave}
      onDragOver={onBoardDragOver}
      ref={rootRef}
    >
      <div
        data-testid="auto-scroll-child"
        ref={(element) => {
          registerColumnScrollport("column-1", element);
        }}
      />
    </div>
  );
}

function controllerFor(root: HTMLElement, animationFrames: FakeAnimationFrames): BoardDragAutoScrollController {
  return new BoardDragAutoScrollController({
    cancelFrame: animationFrames.cancel,
    requestFrame: animationFrames.request,
    root,
  });
}

function scrollport(input: Readonly<{
  left: number;
  top: number;
  width: number;
  height: number;
  scrollWidth: number;
  scrollHeight: number;
}>): HTMLElement {
  const element = document.createElement("div");
  setScrollportGeometry(element, input);
  return element;
}

function setScrollportGeometry(
  element: HTMLElement,
  input: Readonly<{
    left: number;
    top: number;
    width: number;
    height: number;
    scrollWidth: number;
    scrollHeight: number;
  }>,
): void {
  Object.defineProperties(element, {
    clientHeight: { configurable: true, value: input.height },
    clientWidth: { configurable: true, value: input.width },
    scrollHeight: { configurable: true, value: input.scrollHeight },
    scrollLeft: { configurable: true, value: 0, writable: true },
    scrollTop: { configurable: true, value: 0, writable: true },
    scrollWidth: { configurable: true, value: input.scrollWidth },
  });
  Object.defineProperty(element, "getBoundingClientRect", {
    configurable: true,
    value: () => new DOMRect(input.left, input.top, input.width, input.height),
  });
}

function dispatchDrag(
  target: EventTarget,
  type: string,
  init: Readonly<{ clientX?: number; clientY?: number; relatedTarget?: EventTarget | null }> = {},
): void {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperties(event, {
    clientX: { value: init.clientX ?? 0 },
    clientY: { value: init.clientY ?? 0 },
    relatedTarget: { value: init.relatedTarget ?? null },
  });
  target.dispatchEvent(event);
}

class FakeAnimationFrames {
  #callbacks = new Map<number, FrameRequestCallback>();
  #nextID = 1;

  readonly request = vi.fn((callback: FrameRequestCallback) => {
    const id = this.#nextID;
    this.#nextID += 1;
    this.#callbacks.set(id, callback);
    return id;
  });

  readonly cancel = vi.fn((id: number) => {
    this.#callbacks.delete(id);
  });

  get pending(): number {
    return this.#callbacks.size;
  }

  step(timestamp: number): void {
    const callbacks = [...this.#callbacks.values()];
    this.#callbacks.clear();
    for (const callback of callbacks) {
      callback(timestamp);
    }
  }
}
