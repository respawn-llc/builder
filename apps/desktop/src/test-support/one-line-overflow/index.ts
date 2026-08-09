import { installResizeObserverGeometry } from "../resize-observer";

export type OneLineOverflowGeometryHarness = Readonly<{
  notify(): void;
  restore(): void;
  setAvailableWidth(width: number): void;
}>;

export function installOneLineOverflowGeometry(
  input: Readonly<{
    availableWidth: number;
    gap: number;
    itemWidth: number;
    overflowWidth: number;
  }>,
): OneLineOverflowGeometryHarness {
  const resizeObserverGeometry = installResizeObserverGeometry();
  const originalGetBoundingClientRect = Object.getOwnPropertyDescriptor(
    HTMLElement.prototype,
    "getBoundingClientRect",
  );
  let availableWidth = input.availableWidth;
  Object.defineProperty(HTMLElement.prototype, "getBoundingClientRect", {
    configurable: true,
    value(this: HTMLElement): DOMRect {
      const slot = this.getAttribute("data-slot");
      if (slot === "one-line-overflow-row") {
        return rect(availableWidth);
      }
      if (slot === "one-line-overflow-gap-start") {
        return rect(1, 0);
      }
      if (slot === "one-line-overflow-gap-end") {
        return rect(1, input.gap + 1);
      }
      if (slot === "one-line-overflow-item") {
        return rect(input.itemWidth);
      }
      if (slot === "one-line-overflow-count") {
        return rect(input.overflowWidth);
      }
      return rect(0);
    },
  });
  return {
    notify() {
      resizeObserverGeometry.notify();
    },
    restore() {
      if (originalGetBoundingClientRect === undefined) {
        Reflect.deleteProperty(HTMLElement.prototype, "getBoundingClientRect");
      } else {
        Object.defineProperty(HTMLElement.prototype, "getBoundingClientRect", originalGetBoundingClientRect);
      }
      resizeObserverGeometry.restore();
    },
    setAvailableWidth(width) {
      availableWidth = width;
    },
  };
}

export function visibleOneLineOverflowText(row: HTMLElement): string {
  return [...row.children]
    .filter((child) => child.getAttribute("aria-hidden") !== "true")
    .map((child) => child.textContent)
    .join("");
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
