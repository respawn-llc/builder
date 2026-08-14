import "@testing-library/jest-dom/vitest";
import { vi } from "vitest";

Object.defineProperty(window, "scrollTo", {
  configurable: true,
  value: vi.fn(),
});

class TestResizeObserver implements ResizeObserver {
  disconnect(): void {
    // jsdom has no layout engine; component tests only need the observer API to exist.
  }

  observe(): void {
    // jsdom has no layout engine; component tests only need the observer API to exist.
  }

  unobserve(): void {
    // jsdom has no layout engine; component tests only need the observer API to exist.
  }
}

Object.defineProperty(globalThis, "ResizeObserver", {
  configurable: true,
  value: TestResizeObserver,
  writable: true,
});
