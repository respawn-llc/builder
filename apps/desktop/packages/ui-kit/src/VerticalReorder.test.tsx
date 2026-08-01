import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { act, useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { VerticalReorder } from "./VerticalReorder";
import { createEdgeScrollDriver, type EdgeScrollMotion } from "./edgeScroll";
import { projectVerticalReorder } from "./reorderProjection";

type ReorderItem = Readonly<{ id: string; label: string }>;

const items: readonly ReorderItem[] = [
  { id: "first", label: "First" },
  { id: "second", label: "Second" },
  { id: "third", label: "Third" },
];

const originalScrollIntoViewDescriptor = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  "scrollIntoView",
);

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

beforeEach(() => {
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
    configurable: true,
    value: vi.fn(),
    writable: true,
  });
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  Object.defineProperty(
    HTMLElement.prototype,
    "scrollIntoView",
    originalScrollIntoViewDescriptor ?? { configurable: true, value: undefined, writable: true },
  );
});

describe("VerticalReorder", () => {
  it("projects a complete row and commits keyboard reordering with stable item IDs", async () => {
    const onCommit = vi.fn();
    const user = userEvent.setup();
    mockRowGeometry();

    render(<TransformedReorderHarness onCommit={onCommit} />);

    const secondHandle = screen.getByRole("button", { name: "Reorder Second" });
    secondHandle.focus();
    await user.keyboard("[Space]");
    expect(onCommit).not.toHaveBeenCalled();
    await user.keyboard("[ArrowDown]");

    expect(screen.getByTestId("reorder-overlay")).toBeInTheDocument();

    await user.keyboard("[Space]");

    await waitFor(() => {
      expect(onCommit).toHaveBeenCalledWith(["first", "third", "second"]);
      expect(screen.queryByTestId("reorder-overlay")).not.toBeInTheDocument();
    });
  });

  it("keeps the source row as one stable placeholder when pointer dragging starts", () => {
    const view = render(<ReorderHarness onCommit={vi.fn()} />);
    const handle = screen.getByRole("button", { name: "Reorder Second" });

    fireEvent.pointerDown(handle, {
      button: 0,
      clientX: 20,
      clientY: 50,
      isPrimary: true,
      pointerId: 1,
    });
    fireEvent.pointerMove(screen.getByTestId("row-third"), {
      buttons: 1,
      clientX: 20,
      clientY: 57,
      isPrimary: true,
      pointerId: 1,
    });

    expect(screen.getByTestId("reorder-overlay")).toHaveAttribute("data-item-id", "second");
    expect(screen.getByTestId("row-second")).not.toBeVisible();
    expect(screen.getByTestId("row-first")).toBeVisible();
    expect(screen.getByTestId("row-third")).toBeVisible();

    act(() => {
      cancelPointerDrag();
    });
    view.unmount();
  });

  it("keeps pointer projection anchored inside a variable-height source row", async () => {
    const onCommit = vi.fn();
    mockRowGeometry({ secondHeight: 80 });

    const view = render(<TransformedReorderHarness onCommit={onCommit} />);
    const handle = screen.getByRole("button", { name: "Reorder Second" });

    fireEvent.pointerDown(handle, {
      button: 0,
      clientX: 20,
      clientY: 110,
      isPrimary: true,
      pointerId: 1,
    });
    fireEvent.pointerMove(screen.getByTestId("row-third"), {
      buttons: 1,
      clientX: 20,
      clientY: 118,
      isPrimary: true,
      pointerId: 1,
    });
    fireEvent.pointerMove(screen.getByTestId("row-third"), {
      buttons: 1,
      clientX: 20,
      clientY: 119,
      isPrimary: true,
      pointerId: 1,
    });
    expect(screen.getByTestId("reorder-overlay")).toHaveAttribute("data-item-id", "second");
    expect(screen.getByTestId("row-second")).not.toBeVisible();
    expect(screen.getByTestId("row-third")).toBeVisible();
    fireEvent.pointerUp(screen.getByTestId("row-third"), {
      clientX: 20,
      clientY: 119,
      isPrimary: true,
      pointerId: 1,
    });

    await waitFor(() => {
      expect(onCommit).not.toHaveBeenCalled();
      expect(screen.queryByTestId("reorder-overlay")).not.toBeInTheDocument();
    });
    view.unmount();
  });

  it("does not commit keyboard activation before one directional move", async () => {
    const onCommit = vi.fn();
    const user = userEvent.setup();
    mockRowGeometry();

    render(<ReorderHarness onCommit={onCommit} />);

    const secondHandle = screen.getByRole("button", { name: "Reorder Second" });
    secondHandle.focus();
    await user.keyboard("[Space]");
    await user.keyboard("[Space]");

    await waitFor(() => {
      expect(onCommit).not.toHaveBeenCalled();
      expect(screen.queryByTestId("reorder-overlay")).not.toBeInTheDocument();
    });
  });

  it("commits a pointer drag to the adjacent row and clears the drag projection", async () => {
    const onCommit = vi.fn();
    mockRowGeometry();

    const view = render(<TransformedReorderHarness onCommit={onCommit} />);
    const handle = screen.getByRole("button", { name: "Reorder Second" });
    const destination = screen.getByTestId("row-third");

    fireEvent.pointerDown(handle, {
      button: 0,
      clientX: 20,
      clientY: 50,
      isPrimary: true,
      pointerId: 1,
    });
    fireEvent.pointerMove(destination, {
      buttons: 1,
      clientX: 27,
      clientY: 60,
      isPrimary: true,
      pointerId: 1,
    });
    fireEvent.pointerMove(destination, {
      buttons: 1,
      clientX: 27,
      clientY: 95,
      isPrimary: true,
      pointerId: 1,
    });
    expect(screen.getByTestId("reorder-overlay")).toBeInTheDocument();

    fireEvent.pointerUp(destination, {
      clientX: 27,
      clientY: 95,
      isPrimary: true,
      pointerId: 1,
    });

    await waitFor(() => {
      expect(onCommit).toHaveBeenCalledWith(["first", "third", "second"]);
      expect(screen.queryByTestId("reorder-overlay")).not.toBeInTheDocument();
    });
    view.unmount();
  });

  it("cancels an active keyboard drag without committing or leaving drag projection behind", async () => {
    const onCommit = vi.fn();
    const user = userEvent.setup();
    mockRowGeometry();

    render(<ReorderHarness onCommit={onCommit} />);

    const secondHandle = screen.getByRole("button", { name: "Reorder Second" });
    secondHandle.focus();
    await user.keyboard("[Space]");
    await user.keyboard("[ArrowDown]");
    await waitFor(() =>
      expect(screen.getByTestId("reorder-overlay")).toHaveAttribute("data-item-id", "second"),
    );
    await user.keyboard("[Escape]");

    await waitFor(() => {
      expect(onCommit).not.toHaveBeenCalled();
      expect(screen.queryByTestId("reorder-overlay")).not.toBeInTheDocument();
    });
  });

  it("keeps one source placeholder and moves one adjacent row after one keyboard Down", async () => {
    const onCommit = vi.fn();
    const user = userEvent.setup();
    mockRowGeometry();

    render(<ReorderHarness onCommit={onCommit} />);

    const secondHandle = screen.getByRole("button", { name: "Reorder Second" });
    secondHandle.focus();
    await user.keyboard("[Space]");

    expect(screen.getByTestId("row-second")).not.toBeVisible();

    await user.keyboard("[ArrowDown]");

    await waitFor(() => {
      expect(screen.getByTestId("reorder-overlay")).toHaveAttribute("data-item-id", "second");
    });
    expect(screen.getByTestId("row-second")).not.toBeVisible();
    await user.keyboard("[Space]");
    await waitFor(() => {
      expect(onCommit).toHaveBeenCalledWith(["first", "third", "second"]);
      expect(screen.queryByTestId("reorder-overlay")).not.toBeInTheDocument();
    });
  });

  it("keeps keyboard projection and final IDs under reduced motion and cleans the listener", async () => {
    const onCommit = vi.fn();
    const user = userEvent.setup();
    const media = {
      addEventListener: vi.fn(),
      matches: true,
      removeEventListener: vi.fn(),
    };
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => media),
    );
    mockRowGeometry();

    const view = render(<ReorderHarness onCommit={onCommit} />);
    const secondHandle = screen.getByRole("button", { name: "Reorder Second" });
    secondHandle.focus();
    await user.keyboard("[Space]");
    await user.keyboard("[ArrowDown]");

    expect(screen.getByTestId("reorder-overlay")).toBeInTheDocument();

    await user.keyboard("[Space]");
    await waitFor(() => {
      expect(onCommit).toHaveBeenCalledWith(["first", "third", "second"]);
    });

    view.unmount();
    expect(media.removeEventListener).toHaveBeenCalledWith("change", expect.anything());
  });

  it("uses only one shared animation frame for pointer edge scrolling", async () => {
    const callbacks = new Map<number, FrameRequestCallback>();
    let nextFrameID = 0;
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
      const frameID = ++nextFrameID;
      callbacks.set(frameID, callback);
      return frameID;
    });
    vi.spyOn(window, "cancelAnimationFrame").mockImplementation((frameID) => {
      callbacks.delete(frameID);
    });
    const interval = vi.spyOn(window, "setInterval");
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({
        addEventListener: vi.fn(),
        matches: true,
        removeEventListener: vi.fn(),
      })),
    );
    mockRowGeometry();

    const view = render(<ReorderHarness onCommit={vi.fn()} scrollable />);
    const scrollport = screen.getByTestId("reorder-scrollport");
    Object.defineProperties(scrollport, {
      clientHeight: { configurable: true, value: 100 },
      scrollHeight: { configurable: true, value: 800 },
      scrollTop: { configurable: true, value: 0, writable: true },
    });
    const handle = screen.getByRole("button", { name: "Reorder Second" });
    act(() => {
      activatePointerDrag(handle);
    });

    expect(screen.getByTestId("reorder-overlay")).toBeInTheDocument();
    expect(interval).not.toHaveBeenCalled();
    expect(callbacks).toHaveLength(1);
    expect(scrollport.scrollTop).toBe(0);

    const pending = firstFrame(callbacks);
    if (pending === undefined) {
      throw new Error("expected a shared edge-scroll frame");
    }
    callbacks.delete(pending[0]);
    act(() => {
      pending[1](16);
    });

    expect(scrollport.scrollTop).toBeGreaterThan(0);
    expect(scrollport.scrollTop).toBeLessThanOrEqual(43.2);
    act(() => {
      cancelPointerDrag();
    });
    view.unmount();
    expect(callbacks).toHaveLength(0);
  });

  it("does not use the document as an edge-scroll fallback for a short catalog", () => {
    const callbacks = new Map<number, FrameRequestCallback>();
    let nextFrameID = 0;
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
      const frameID = ++nextFrameID;
      callbacks.set(frameID, callback);
      return frameID;
    });
    vi.spyOn(window, "cancelAnimationFrame").mockImplementation((frameID) => {
      callbacks.delete(frameID);
    });
    mockRowGeometry();

    const documentScroll = document.documentElement;
    Object.defineProperties(documentScroll, {
      clientHeight: { configurable: true, value: 100 },
      scrollHeight: { configurable: true, value: 800 },
      scrollTop: { configurable: true, value: 17, writable: true },
    });
    Object.defineProperty(document, "scrollingElement", {
      configurable: true,
      value: documentScroll,
    });

    try {
      const view = render(<ReorderHarness onCommit={vi.fn()} />);
      const handle = screen.getByRole("button", { name: "Reorder Second" });
      act(() => {
        activatePointerDrag(handle);
      });

      expect(callbacks).toHaveLength(0);
      expect(documentScroll.scrollTop).toBe(17);

      act(() => {
        cancelPointerDrag();
      });
      view.unmount();
    } finally {
      Reflect.deleteProperty(document, "scrollingElement");
      Reflect.deleteProperty(documentScroll, "clientHeight");
      Reflect.deleteProperty(documentScroll, "scrollHeight");
      Reflect.deleteProperty(documentScroll, "scrollTop");
    }
  });

  it("cancels pointer edge scrolling when the pointer exits horizontally", () => {
    const callbacks = new Map<number, FrameRequestCallback>();
    let nextFrameID = 0;
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
      const frameID = ++nextFrameID;
      callbacks.set(frameID, callback);
      return frameID;
    });
    vi.spyOn(window, "cancelAnimationFrame").mockImplementation((frameID) => {
      callbacks.delete(frameID);
    });
    mockRowGeometry();

    const view = render(<ReorderHarness onCommit={vi.fn()} scrollable />);
    const scrollport = screen.getByTestId("reorder-scrollport");
    Object.defineProperties(scrollport, {
      clientHeight: { configurable: true, value: 100 },
      scrollHeight: { configurable: true, value: 800 },
      scrollTop: { configurable: true, value: 0, writable: true },
    });
    const handle = screen.getByRole("button", { name: "Reorder Second" });
    act(() => {
      activatePointerDrag(handle);
    });

    expect(callbacks).toHaveLength(1);
    const scrollTopBeforeExit = scrollport.scrollTop;
    fireEvent.pointerMove(document, {
      buttons: 1,
      clientX: 300,
      clientY: 95,
      isPrimary: true,
      pointerId: 1,
    });

    expect(callbacks).toHaveLength(0);
    expect(scrollport.scrollTop).toBe(scrollTopBeforeExit);
    act(() => {
      cancelPointerDrag();
    });
    view.unmount();
  });
});

function activatePointerDrag(handle: HTMLElement): void {
  fireEvent.pointerDown(handle, { button: 0, clientX: 20, clientY: 50, isPrimary: true, pointerId: 1 });
  fireEvent.pointerMove(document, { buttons: 1, clientX: 20, clientY: 60, isPrimary: true, pointerId: 1 });
  fireEvent.pointerMove(document, { buttons: 1, clientX: 20, clientY: 95, isPrimary: true, pointerId: 1 });
}

function cancelPointerDrag(): void {
  fireEvent.pointerCancel(document, { clientX: 20, clientY: 95, pointerId: 1 });
}

type ReorderHarnessProps = Readonly<{
  onCommit: (orderedIDs: readonly string[]) => void;
  scrollable?: boolean;
}>;

function TransformedReorderHarness(props: ReorderHarnessProps) {
  return (
    <div data-testid="transformed-surface" style={{ transform: "translateY(50px)" }}>
      <ReorderHarness {...props} />
    </div>
  );
}

function ReorderHarness({ onCommit, scrollable = false }: ReorderHarnessProps) {
  const [orderedItems, setOrderedItems] = useState(items);
  const reorder = (
    <VerticalReorder
      getItemID={(item) => item.id}
      items={orderedItems}
      onCommit={(orderedIDs) => {
        onCommit(orderedIDs);
        setOrderedItems((current) => move(current, orderedIDs));
      }}
      renderActivator={(item) => (
        <button aria-label={`Reorder ${item.label}`} type="button">
          {item.label}
        </button>
      )}
      renderItem={(item, row) => (
        <div
          data-item-id={item.id}
          data-row-id={row.isOverlay ? undefined : item.id}
          data-testid={row.isOverlay ? "reorder-overlay" : `row-${item.id}`}
        >
          {row.activator}
        </div>
      )}
    />
  );
  const list = <div role="list">{reorder}</div>;
  return scrollable ? (
    <div data-testid="reorder-scrollport" style={{ overflowY: "auto" }}>
      {list}
    </div>
  ) : (
    list
  );
}

function move(rows: readonly ReorderItem[], orderedIDs: readonly string[]): readonly ReorderItem[] {
  const itemsByID = new Map(rows.map((row) => [row.id, row]));
  return orderedIDs.flatMap((id) => {
    const item = itemsByID.get(id);
    return item === undefined ? [] : [item];
  });
}

function firstFrame(
  callbacks: ReadonlyMap<number, FrameRequestCallback>,
): readonly [number, FrameRequestCallback] | undefined {
  for (const entry of callbacks) {
    return entry;
  }
  return undefined;
}

function mockRowGeometry({ secondHeight = 32 }: Readonly<{ secondHeight?: number }> = {}): void {
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.dataset.testid === "reorder-scrollport" || this === document.documentElement) {
      return {
        bottom: 100,
        height: 100,
        left: 0,
        right: 240,
        toJSON: () => ({}),
        top: 0,
        width: 240,
        x: 0,
        y: 0,
      };
    }
    const rowID = rowIDForElement(this);
    const rowIndexByID: Readonly<Record<string, number>> = {
      first: 0,
      second: 1,
      third: 2,
    };
    const index = rowID === undefined ? undefined : rowIndexByID[rowID];
    const baseTop = index === undefined ? 0 : index === 2 ? 40 + secondHeight + 8 : index * 40;
    const top = baseTop;
    const height = index === 1 ? secondHeight : 32;
    return {
      bottom: top + height,
      height,
      left: 0,
      right: 240,
      toJSON: () => ({}),
      top,
      width: 240,
      x: 0,
      y: top,
    };
  });
}

function rowIDForElement(element: HTMLElement): string | undefined {
  return ["first", "second", "third"].find(
    (id) => element.dataset.testid === `row-${id}` || within(element).queryByTestId(`row-${id}`) !== null,
  );
}

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

    motion = [{ axis: "y", element: edgeScrollTestScrollport(), velocity: 900 }];
    driver.refresh();
    driver.refresh();
    expect(callbacks).toHaveLength(1);
  });

  it("applies bounded motion and stops when the target can no longer move", () => {
    const element = edgeScrollTestScrollport();
    const motion: readonly EdgeScrollMotion[] = [{ axis: "y", element, velocity: 10_000 }];
    const driver = createEdgeScrollDriver(() => motion);

    driver.refresh();
    releaseEdgeScrollFrame(callbacks, 1_000);

    expect(element.scrollTop).toBeLessThanOrEqual(43.2);
    expect(callbacks).toHaveLength(1);

    element.scrollTop = element.scrollHeight - element.clientHeight;
    releaseEdgeScrollFrame(callbacks, 2_000);
    expect(callbacks).toHaveLength(0);
  });

  it("moves in the requested direction with a bounded frame delta", () => {
    const element = edgeScrollTestScrollport();
    element.scrollTop = 200;
    const motion: readonly EdgeScrollMotion[] = [{ axis: "y", element, velocity: -10_000 }];
    const driver = createEdgeScrollDriver(() => motion);

    driver.refresh();
    releaseEdgeScrollFrame(callbacks, 10_000);

    expect(element.scrollTop).toBeGreaterThanOrEqual(156.8);
    expect(element.scrollTop).toBeLessThan(200);
  });

  it("cancels its pending frame and clears future writes on stop", () => {
    const element = edgeScrollTestScrollport();
    let motion: readonly EdgeScrollMotion[] | null = [{ axis: "y", element, velocity: 900 }];
    const driver = createEdgeScrollDriver(() => motion);

    driver.refresh();
    driver.stop();
    motion = [{ axis: "y", element, velocity: 900 }];
    for (const [frameID, callback] of callbacks) {
      callbacks.delete(frameID);
      callback(1_000);
    }

    expect(element.scrollTop).toBe(0);
    expect(callbacks).toHaveLength(0);
  });
});

function releaseEdgeScrollFrame(callbacks: Map<number, FrameRequestCallback>, timestamp: number): void {
  const pending = firstFrame(callbacks);
  if (pending === undefined) {
    throw new Error("expected a pending animation frame");
  }
  callbacks.delete(pending[0]);
  pending[1](timestamp);
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
