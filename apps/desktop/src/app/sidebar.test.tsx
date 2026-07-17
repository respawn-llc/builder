import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeAll, vi } from "vitest";

import { appI18n, initializeI18n } from "@/i18n";
import { SidebarHost } from "./sidebar";
import { useSidebar } from "@/app-facade";
import { SidebarProvider } from "./sidebarProvider";

describe("SidebarHost", () => {
  beforeAll(async () => {
    await initializeI18n();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("uses the destination desired width for the initial width", async () => {
    await expectOpenSidebarWidths(<OpenCustomSidebar />, [
      [1600, 550],
      [3000, 550],
    ]);
  });

  it("clamps destination desired width by the global max and content minimum", async () => {
    await expectOpenSidebarWidths(<OpenSizedCustomSidebar />, [
      [1600, 900],
      [700, 595],
    ]);
  });

  it("uses shift layout as the default destination mode", async () => {
    const { sidebar } = await renderOpenSidebar(<OpenCustomSidebar />);
    expect(sidebar).toHaveAttribute("data-mode", "shift");
    expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Settings" })).toBeInTheDocument();
    expect(screen.getByTestId("default-shift-content")).toBeInTheDocument();
  });

  it("keeps overlay destinations out of the flex layout", async () => {
    const { sidebar } = await renderOpenSidebar(<OpenOverlaySidebar />, {
      destinationName: "Create workflow",
      openButtonName: "Open overlay sidebar",
    });
    expect(sidebar).toHaveAttribute("data-mode", "overlay");
    expect(screen.getByTestId("overlay-source-content")).toBeInTheDocument();
  });

  it("resizes from the leading edge without requiring pointer-capture APIs", async () => {
    mockSidebarLayout();

    const { sidebar } = await renderOpenSidebar(<OpenCustomSidebar />);
    const resizeHandle = screen.getByRole("separator", { name: "Resize sidebar" });
    const initialWidth = sidebarWidthStyle(sidebar);
    fireEvent.pointerDown(resizeHandle, { button: 0, clientX: 700, pointerId: 1 });
    fireEvent.pointerMove(resizeHandle, { clientX: 620, pointerId: 1 });
    fireEvent.pointerUp(resizeHandle, { clientX: 620, pointerId: 1 });

    expect(sidebarWidthStyle(sidebar)).toBe(initialWidth + 80);
    expect(resizeHandle).toHaveAttribute("aria-valuenow", String(initialWidth + 80));

    fireEvent.keyDown(resizeHandle, { key: "ArrowRight" });

    expect(sidebarWidthStyle(sidebar)).toBe(initialWidth + 48);
  });

  it("clamps the current width when the app shell narrows", async () => {
    let shellWidth = 1200;
    mockSidebarLayout(() => shellWidth);

    const { sidebar } = await renderOpenSidebar(<OpenCustomSidebar />);
    const resizeHandle = screen.getByRole("separator", { name: "Resize sidebar" });
    fireEvent.keyDown(resizeHandle, { key: "End" });
    await waitFor(() => {
      expect(sidebarWidthStyle(sidebar)).toBe(1020);
    });

    shellWidth = 760;
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });

    await waitFor(() => {
      expect(sidebarWidthStyle(sidebar)).toBe(646);
    });
    expect(resizeHandle).toHaveAttribute("aria-valuemax", "646");
  });
});

function OpenCustomSidebar() {
  const { openSidebar } = useSidebar();

  return (
    <button
      onClick={() => {
        void openSidebar({
          content: <p data-testid="default-shift-content">Default shift content</p>,
          kind: "custom",
          title: "Settings",
        });
      }}
      type="button"
    >
      Open sidebar
    </button>
  );
}

function OpenOverlaySidebar() {
  const { openSidebar } = useSidebar();

  return (
    <>
      <div data-testid="overlay-source-content" />
      <button
        onClick={() => {
          void openSidebar({
            content: <p>Overlay content</p>,
            kind: "custom",
            mode: "overlay",
            title: "Create workflow",
          });
        }}
        type="button"
      >
        Open overlay sidebar
      </button>
    </>
  );
}

function OpenSizedCustomSidebar() {
  const { openSidebar } = useSidebar();

  return (
    <button
      onClick={() => {
        void openSidebar({
          content: <p>Sized content</p>,
          kind: "custom",
          sizing: { desiredWidthPx: 900, minWidthPx: 620 },
          title: "Settings",
        });
      }}
      type="button"
    >
      Open sidebar
    </button>
  );
}

function sidebarWidthStyle(sidebar: HTMLElement): number {
  return Number.parseInt(sidebar.style.getPropertyValue("--app-sidebar-width"), 10);
}

async function renderOpenSidebar(
  opener: ReactNode,
  {
    destinationName = "Settings",
    openButtonName = "Open sidebar",
  }: Readonly<{
    destinationName?: string;
    openButtonName?: string;
  }> = {},
): Promise<Readonly<{ sidebar: HTMLElement; unmount(): void }>> {
  const { unmount } = render(
    <I18nextProvider i18n={appI18n}>
      <SidebarProvider>
        <div className="relative flex min-h-0" data-testid="app-shell-content">
          <div className="min-w-0 flex-1">{opener}</div>
          <SidebarHost />
        </div>
      </SidebarProvider>
    </I18nextProvider>,
  );

  fireEvent.click(screen.getByRole("button", { name: openButtonName }));

  return {
    sidebar: await screen.findByRole("complementary", { name: destinationName }),
    unmount,
  };
}

type SidebarWidthExpectation = readonly [windowWidth: number, sidebarWidth: number];

async function expectOpenSidebarWidths(
  opener: ReactNode,
  expectations: readonly SidebarWidthExpectation[],
): Promise<void> {
  for (const [windowWidth, expectedWidth] of expectations) {
    const restoreWindowWidth = mockWindowWidth(windowWidth);
    try {
      const { sidebar, unmount } = await renderOpenSidebar(opener);
      try {
        expect(sidebarWidthStyle(sidebar)).toBe(expectedWidth);
      } finally {
        unmount();
      }
    } finally {
      restoreWindowWidth();
    }
  }
}

function mockWindowWidth(width: number): () => void {
  const descriptor = Object.getOwnPropertyDescriptor(window, "innerWidth");
  Object.defineProperty(window, "innerWidth", { configurable: true, value: width });
  return () => {
    if (descriptor === undefined) {
      Reflect.deleteProperty(window, "innerWidth");
      return;
    }
    Object.defineProperty(window, "innerWidth", descriptor);
  };
}

function mockSidebarLayout(shellWidth: () => number = () => 1200): void {
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function getBoundingClientRect(
    this: HTMLElement,
  ) {
    if (this instanceof HTMLElement && this.dataset.testid === "app-shell-content") {
      return domRect({ height: 720, width: shellWidth() });
    }
    return domRect({ height: 720, width: 560 });
  });
}

function domRect({ height, width }: Readonly<{ height: number; width: number }>): DOMRect {
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
