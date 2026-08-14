import { act, render, screen } from "@testing-library/react";

import { useHomeSidebarMode } from "./useHomeSidebarMode";

it("switches Home sidebars between overlay and shift as available main-pane width changes", () => {
  let availableWidthPx = 1_000;
  let notifyResize: (() => void) | null = null;
  const originalResizeObserver = Object.getOwnPropertyDescriptor(globalThis, "ResizeObserver");
  const geometry = vi
    .spyOn(HTMLElement.prototype, "getBoundingClientRect")
    .mockImplementation(() => domRect(availableWidthPx));
  Object.defineProperty(globalThis, "ResizeObserver", {
    configurable: true,
    value: class ControlledResizeObserver implements ResizeObserver {
      constructor(callback: ResizeObserverCallback) {
        notifyResize = () => {
          callback([], this);
        };
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
    },
  });

  try {
    render(<SidebarModeHarness />);
    expect(screen.getByTestId("main-pane")).toHaveAttribute("data-sidebar-mode", "overlay");

    availableWidthPx = 1_100;
    act(() => {
      notifyResize?.();
    });
    expect(screen.getByTestId("main-pane")).toHaveAttribute("data-sidebar-mode", "shift");
  } finally {
    geometry.mockRestore();
    if (originalResizeObserver === undefined) {
      Reflect.deleteProperty(globalThis, "ResizeObserver");
    } else {
      Object.defineProperty(globalThis, "ResizeObserver", originalResizeObserver);
    }
  }
});

function SidebarModeHarness() {
  const { mainPaneRef, sidebarMode } = useHomeSidebarMode();
  return <section data-sidebar-mode={sidebarMode} data-testid="main-pane" ref={mainPaneRef} />;
}

function domRect(width: number): DOMRect {
  return {
    bottom: 0,
    height: 0,
    left: 0,
    right: width,
    top: 0,
    width,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  };
}
