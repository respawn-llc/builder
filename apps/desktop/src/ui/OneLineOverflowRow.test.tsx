import { act, render, screen } from "@testing-library/react";

import { OneLineOverflowRow } from "./index";

describe("OneLineOverflowRow", () => {
  const originalResizeObserver = globalThis.ResizeObserver;
  const originalGetBoundingClientRect = Object.getOwnPropertyDescriptor(
    HTMLElement.prototype,
    "getBoundingClientRect",
  );

  afterEach(() => {
    globalThis.ResizeObserver = originalResizeObserver;
    if (originalGetBoundingClientRect !== undefined) {
      Object.defineProperty(HTMLElement.prototype, "getBoundingClientRect", originalGetBoundingClientRect);
    }
  });

  it("recomputes complete visible items across narrow and wide widths", () => {
    let availableWidth = 96;
    const observers: MockResizeObserver[] = [];
    globalThis.ResizeObserver = class extends MockResizeObserver {
      constructor(callback: ResizeObserverCallback) {
        super(callback);
        observers.push(this);
      }
    };
    HTMLElement.prototype.getBoundingClientRect = function () {
      const slot = this.getAttribute("data-slot");
      if (slot === "one-line-overflow-row") {
        return rect(availableWidth);
      }
      if (slot === "one-line-overflow-gap-start") {
        return rect(1, 0);
      }
      if (slot === "one-line-overflow-gap-end") {
        return rect(1, 5);
      }
      if (slot === "one-line-overflow-item") {
        return rect(32);
      }
      if (slot === "one-line-overflow-count") {
        return rect(24);
      }
      return rect(0);
    };

    render(
      <OneLineOverflowRow
        ariaLabel="Labels"
        items={[
          { id: "one", content: <span>One</span> },
          { id: "two", content: <span>Two</span> },
          { id: "three", content: <span>Three</span> },
          { id: "four", content: <span>Four</span> },
        ]}
        renderOverflow={(hiddenCount) => <span>+{hiddenCount}</span>}
      />,
    );
    act(() => {
      for (const observer of observers) {
        observer.notify();
      }
    });

    const row = screen.getByRole("group", { name: "Labels" });
    expect(visibleText(row)).toBe("OneTwo+2");

    availableWidth = 140;
    act(() => {
      for (const observer of observers) {
        observer.notify();
      }
    });

    expect(visibleText(row)).toBe("OneTwoThreeFour");
  });
});

class MockResizeObserver implements ResizeObserver {
  readonly #callback: ResizeObserverCallback;

  constructor(callback: ResizeObserverCallback) {
    this.#callback = callback;
  }

  disconnect(): void {
    return;
  }

  observe(): void {
    return;
  }

  unobserve(): void {
    return;
  }

  notify(): void {
    this.#callback([], this);
  }
}

function rect(width: number, left = 0): DOMRect {
  return {
    bottom: 0,
    height: 0,
    left,
    right: left + width,
    top: 0,
    width,
    x: left,
    y: 0,
    toJSON: () => ({}),
  };
}

function visibleText(row: HTMLElement): string {
  return [...row.children]
    .filter((child) => child.getAttribute("aria-hidden") !== "true")
    .map((child) => child.textContent)
    .join("");
}
