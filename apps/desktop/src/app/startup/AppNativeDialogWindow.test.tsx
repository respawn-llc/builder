import { createBrowserNativeBridge, type NativeBridge } from "@app/native-bridge";
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, vi } from "vitest";

import { App } from "./App";
import { createTestServices, startupRoutes } from "@/test-support/app-services";

describe("App native dialog window sizing", () => {
  const originalGetBoundingClientRect = HTMLElement.prototype.getBoundingClientRect.bind(
    HTMLElement.prototype,
  );
  const originalResizeObserver = globalThis.ResizeObserver;

  afterEach(() => {
    HTMLElement.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    globalThis.ResizeObserver = originalResizeObserver;
  });

  it("fits native dialog window to rendered dialog content", async () => {
    const fittedSizes: { width: number; height: number }[] = [];
    HTMLElement.prototype.getBoundingClientRect = vi.fn(() => dialogRect(584, 320));
    window.history.pushState(
      null,
      "",
      "/native-dialog/project-create?name=Example&key=EXP&workspaceRoot=%2Ftmp%2Fexample",
    );

    render(<App services={createTestServices(startupRoutes, fitRecorderBridge(fittedSizes))} />);

    expect(await screen.findByTestId("native-dialog-content")).toBeInTheDocument();
    await waitFor(() => {
      expect(fittedSizes).toContainEqual({ height: 320, width: 584 });
    });
  });

  it("re-fits native dialog window when rendered content size changes", async () => {
    const fittedSizes: { width: number; height: number }[] = [];
    const observers: TestResizeObserver[] = [];
    let measuredHeight = 320;
    HTMLElement.prototype.getBoundingClientRect = vi.fn(() => dialogRect(584, measuredHeight));
    globalThis.ResizeObserver = class extends TestResizeObserver {
      constructor(callback: ResizeObserverCallback) {
        super(callback);
        observers.push(this);
      }
    };
    window.history.pushState(
      null,
      "",
      "/native-dialog/project-create?name=Example&key=EXP&workspaceRoot=%2Ftmp%2Fexample",
    );

    render(<App services={createTestServices(startupRoutes, fitRecorderBridge(fittedSizes))} />);

    expect(await screen.findByTestId("native-dialog-content")).toBeInTheDocument();
    await waitFor(() => {
      expect(fittedSizes).toContainEqual({ height: 320, width: 584 });
    });

    measuredHeight = 372;
    for (const observer of observers) {
      observer.trigger();
    }

    await waitFor(() => {
      expect(fittedSizes).toContainEqual({ height: 372, width: 584 });
    });
  });
});

function fitRecorderBridge(fittedSizes: { width: number; height: number }[]): NativeBridge {
  const base = createBrowserNativeBridge();
  return {
    ...base,
    window: {
      ...base.window,
      async fitCurrentToContent(size: { width: number; height: number }): Promise<void> {
        fittedSizes.push(size);
      },
    },
  };
}

function dialogRect(width: number, height: number): DOMRect {
  return {
    bottom: height,
    height,
    left: 0,
    right: width,
    top: 0,
    width,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  };
}

class TestResizeObserver implements ResizeObserver {
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

  trigger(): void {
    this.#callback([], this);
  }

  unobserve(): void {
    return;
  }
}
