import { render, screen } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { dispatchBoardDrag, FakeAnimationFrames, setScrollportGeometry } from "@/test-support/board-drag";
import { useBoardDragAutoScroll } from "./BoardDragAutoScroll";

describe("useBoardDragAutoScroll", () => {
  let frames: FakeAnimationFrames;

  beforeEach(() => {
    frames = new FakeAnimationFrames();
    vi.stubGlobal("requestAnimationFrame", frames.request);
    vi.stubGlobal("cancelAnimationFrame", frames.cancel);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("stays idle in neutral zones and accelerates monotonically toward both horizontal edges", () => {
    const { root } = renderAutoScroll();
    setRootGeometry(root, { left: 100, width: 400 });

    root.scrollLeft = 250;
    dispatchBoardDrag(root, "dragover", { clientX: 250, clientY: 200 });
    expect(frames.pending).toBe(0);

    const rightGentle = scrollDelta(root, frames, { clientX: 429, startAt: 0 });
    const rightNear = scrollDelta(root, frames, { clientX: 464, startAt: 96 });
    const rightEdge = scrollDelta(root, frames, { clientX: 500, startAt: 192 });
    expect(rightGentle).toBeGreaterThan(0);
    expect(rightNear).toBeGreaterThan(rightGentle);
    expect(rightEdge).toBeGreaterThan(rightNear);

    root.scrollLeft = 250;
    const leftGentle = scrollDelta(root, frames, { clientX: 171, startAt: 288 });
    const leftNear = scrollDelta(root, frames, { clientX: 136, startAt: 384 });
    const leftEdge = scrollDelta(root, frames, { clientX: 100, startAt: 480 });
    expect(leftGentle).toBeLessThan(0);
    expect(leftNear).toBeLessThan(leftGentle);
    expect(leftEdge).toBeLessThan(leftNear);
  });

  it("owns one pending frame and scrolls horizontal and hovered vertical ports together", () => {
    const { root } = renderAutoScroll(["column-1"]);
    const column = screen.getByTestId("column-1");

    dispatchBoardDrag(root, "dragover", { clientX: 480, clientY: 400 });
    dispatchBoardDrag(root, "dragover", { clientX: 480, clientY: 400 });

    expect(frames.pending).toBe(1);
    frames.step(0);
    frames.step(16);

    expect(root.scrollLeft).toBeGreaterThan(0);
    expect(column.scrollTop).toBeGreaterThan(0);
    expect(frames.pending).toBe(1);
  });

  it("selects a registered column at its exact left and top edges", () => {
    const { root } = renderAutoScroll(["column-1"]);
    const column = screen.getByTestId("column-1");
    setRootGeometry(root, { scrollWidth: 500 });
    column.scrollTop = 300;

    dispatchBoardDrag(root, "dragover", { clientX: 300, clientY: 0 });
    frames.step(0);
    frames.step(16);

    expect(column.scrollTop).toBeLessThan(300);
  });

  it("keeps each actionable axis independent of the other axis", () => {
    const { root } = renderAutoScroll(["column-1"]);
    const column = screen.getByTestId("column-1");

    dispatchBoardDrag(root, "dragover", { clientX: 496, clientY: 200 });
    frames.step(0);
    frames.step(16);
    expect(root.scrollLeft).toBeGreaterThan(0);
    expect(column.scrollTop).toBe(0);

    dispatchBoardDrag(root, "dragover", { clientX: 250, clientY: 200 });
    root.scrollLeft = 500;
    dispatchBoardDrag(root, "dragover", { clientX: 476, clientY: 396 });
    frames.step(32);
    frames.step(48);
    expect(root.scrollLeft).toBe(500);
    expect(column.scrollTop).toBeGreaterThan(0);
  });

  it("switches vertical targets and stops targeting an unregistered column", () => {
    const { root, view } = renderAutoScroll(["upper", "lower"]);
    const upper = screen.getByTestId("upper");
    const lower = screen.getByTestId("lower");
    setRootGeometry(root, { scrollWidth: 500 });
    setColumnGeometry(upper, { height: 190 });
    setColumnGeometry(lower, { top: 210, height: 190 });

    upper.scrollTop = 300;
    dispatchBoardDrag(root, "dragover", { clientX: 350, clientY: 4 });
    frames.step(0);
    frames.step(16);
    expect(upper.scrollTop).toBeLessThan(300);
    expect(lower.scrollTop).toBe(0);

    dispatchBoardDrag(root, "dragover", { clientX: 350, clientY: 396 });
    frames.step(32);
    expect(lower.scrollTop).toBeGreaterThan(0);

    view.rerender(<AutoScrollHarness active columnIDs={["upper"]} />);
    const lowerScrollTop = lower.scrollTop;
    dispatchBoardDrag(root, "dragover", { clientX: 350, clientY: 396 });
    expect(frames.pending).toBe(0);
    expect(lower.scrollTop).toBe(lowerScrollTop);
  });

  it("clamps long frame gaps before applying scroll", () => {
    const { root } = renderAutoScroll();

    dispatchBoardDrag(root, "dragover", { clientX: 500, clientY: 200 });
    frames.step(0);
    frames.step(1_000);

    expect(root.scrollLeft).toBeCloseTo(43.2);
  });

  it("stops for neutral pointers, scroll bounds, inactive state, and unmount", () => {
    const { root, view } = renderAutoScroll();

    dispatchBoardDrag(root, "dragover", { clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(1);
    dispatchBoardDrag(root, "dragover", { clientX: 250, clientY: 200 });
    expect(frames.pending).toBe(0);

    root.scrollLeft = 500;
    dispatchBoardDrag(root, "dragover", { clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(0);

    root.scrollLeft = 0;
    dispatchBoardDrag(root, "dragover", { clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(1);
    view.rerender(<AutoScrollHarness active={false} />);
    expect(frames.pending).toBe(0);

    view.rerender(<AutoScrollHarness active />);
    dispatchBoardDrag(root, "dragover", { clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(1);
    view.unmount();
    expect(frames.pending).toBe(0);
  });

  it("ignores nested root dragleave transitions and stops on board exit", () => {
    const { root } = renderAutoScroll();
    const child = screen.getByTestId("auto-scroll-child");

    dispatchBoardDrag(root, "dragover", { clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(1);

    dispatchBoardDrag(root, "dragleave", { relatedTarget: child });
    expect(frames.pending).toBe(1);

    dispatchBoardDrag(child, "dragleave", { clientX: 496, clientY: 200, relatedTarget: null });
    expect(frames.pending).toBe(1);

    dispatchBoardDrag(root, "dragleave", { relatedTarget: document.body });
    expect(frames.pending).toBe(0);
  });

  it.each([
    ["left boundary", { clientX: 0, clientY: 200 }],
    ["top boundary", { clientX: 250, clientY: 0 }],
    ["right boundary", { clientX: 500, clientY: 200 }],
    ["bottom boundary", { clientX: 250, clientY: 400 }],
    ["outside", { clientX: 501, clientY: 200 }],
  ] as const)("stops a null-target board leave at the %s", (_location, point) => {
    const { root } = renderAutoScroll();

    dispatchBoardDrag(root, "dragover", { clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(1);

    dispatchBoardDrag(root, "dragleave", { ...point, relatedTarget: null });
    expect(frames.pending).toBe(0);
  });

  it.each([
    ["dragover", {}],
    ["dragleave", { relatedTarget: null }],
  ] as const)("stops from capture-phase document %s", (type, init) => {
    const { root } = renderAutoScroll();

    dispatchBoardDrag(root, "dragover", { clientX: 496, clientY: 200 });
    expect(frames.pending).toBe(1);

    dispatchBoardDrag(document, type, init);
    expect(frames.pending).toBe(0);
  });
});

type ScrollportGeometry = Parameters<typeof setScrollportGeometry>[1];

const rootGeometry = {
  left: 0,
  top: 0,
  width: 500,
  height: 400,
  scrollWidth: 1_000,
  scrollHeight: 400,
} satisfies ScrollportGeometry;

const columnGeometry = {
  left: 300,
  top: 0,
  width: 180,
  height: 400,
  scrollWidth: 180,
  scrollHeight: 1_000,
} satisfies ScrollportGeometry;

function setRootGeometry(root: HTMLElement, overrides: Partial<ScrollportGeometry> = {}): void {
  setScrollportGeometry(root, { ...rootGeometry, ...overrides });
}

function setColumnGeometry(column: HTMLElement, overrides: Partial<ScrollportGeometry> = {}): void {
  setScrollportGeometry(column, { ...columnGeometry, ...overrides });
}

function renderAutoScroll(columnIDs: readonly string[] = []) {
  const view = render(<AutoScrollHarness active columnIDs={columnIDs} />);
  const root = screen.getByTestId("auto-scroll-root");
  setRootGeometry(root);
  for (const columnID of columnIDs) {
    setColumnGeometry(screen.getByTestId(columnID));
  }
  return { root, view };
}

function AutoScrollHarness({
  active,
  columnIDs = [],
}: Readonly<{ active: boolean; columnIDs?: readonly string[] }>) {
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
      <div data-testid="auto-scroll-child" />
      {columnIDs.map((columnID) => (
        <div
          data-testid={columnID}
          key={columnID}
          ref={(element) => {
            registerColumnScrollport(columnID, element);
          }}
        />
      ))}
    </div>
  );
}

function scrollDelta(
  root: HTMLElement,
  frames: FakeAnimationFrames,
  input: Readonly<{ clientX: number; startAt: number }>,
): number {
  const before = root.scrollLeft;
  dispatchBoardDrag(root, "dragover", { clientX: input.clientX, clientY: 200 });
  frames.step(input.startAt);
  frames.step(input.startAt + 48);
  return root.scrollLeft - before;
}
