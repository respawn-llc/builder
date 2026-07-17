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
    const {
      restoreWindowWidth: restoreUncappedWindowWidth,
      sidebar: uncappedSidebar,
      unmount: unmountUncapped,
    } = await renderOpenSidebarAtWindowWidth(1600);
    try {
      expect(sidebarWidthStyle(uncappedSidebar)).toBe(550);
    } finally {
      unmountUncapped();
      restoreUncappedWindowWidth();
    }

    const {
      restoreWindowWidth: restoreCappedWindowWidth,
      sidebar: cappedSidebar,
      unmount: unmountCapped,
    } = await renderOpenSidebarAtWindowWidth(3000);
    try {
      expect(sidebarWidthStyle(cappedSidebar)).toBe(550);
    } finally {
      unmountCapped();
      restoreCappedWindowWidth();
    }
  });

  it("clamps destination desired width by the global max and content minimum", async () => {
    const {
      restoreWindowWidth: restoreWideWindowWidth,
      sidebar: wideSidebar,
      unmount: unmountWide,
    } = await renderOpenSidebarAtWindowWidth(1600, <OpenSizedCustomSidebar />);
    try {
      expect(sidebarWidthStyle(wideSidebar)).toBe(900);
    } finally {
      unmountWide();
      restoreWideWindowWidth();
    }

    const {
      restoreWindowWidth: restoreNarrowWindowWidth,
      sidebar: narrowSidebar,
      unmount: unmountNarrow,
    } = await renderOpenSidebarAtWindowWidth(700, <OpenSizedCustomSidebar />);
    try {
      expect(sidebarWidthStyle(narrowSidebar)).toBe(595);
    } finally {
      unmountNarrow();
      restoreNarrowWindowWidth();
    }
  });

  it("uses shift layout as the default destination mode", async () => {
    render(
      <I18nextProvider i18n={appI18n}>
        <SidebarProvider>
          <div className="relative flex min-h-0" data-testid="app-shell-content">
            <div className="min-w-0 flex-1">
              <OpenCustomSidebar />
            </div>
            <SidebarHost />
          </div>
        </SidebarProvider>
      </I18nextProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Open sidebar" }));

    const sidebar = await screen.findByRole("complementary", { name: "Settings" });
    expect(sidebar).toHaveAttribute("data-mode", "shift");
    expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Settings" })).toBeInTheDocument();
    expect(screen.getByTestId("default-shift-content")).toBeInTheDocument();
  });

  it("keeps overlay destinations out of the flex layout", async () => {
    render(
      <I18nextProvider i18n={appI18n}>
        <SidebarProvider>
          <div className="relative flex min-h-0" data-testid="app-shell-content">
            <div className="min-w-0 flex-1">
              <OpenOverlaySidebar />
            </div>
            <SidebarHost />
          </div>
        </SidebarProvider>
      </I18nextProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Open overlay sidebar" }));

    const sidebar = await screen.findByRole("complementary", { name: "Create workflow" });
    expect(sidebar).toHaveAttribute("data-mode", "overlay");
    expect(screen.getByTestId("overlay-source-content")).toBeInTheDocument();
  });

  it("resizes from the leading edge without requiring pointer-capture APIs", async () => {
    mockSidebarLayout();
    render(
      <I18nextProvider i18n={appI18n}>
        <SidebarProvider>
          <div className="relative flex min-h-0" data-testid="app-shell-content">
            <div className="min-w-0 flex-1">
              <OpenCustomSidebar />
            </div>
            <SidebarHost />
          </div>
        </SidebarProvider>
      </I18nextProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Open sidebar" }));

    const sidebar = await screen.findByRole("complementary", { name: "Settings" });
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
    render(
      <I18nextProvider i18n={appI18n}>
        <SidebarProvider>
          <div className="relative flex min-h-0" data-testid="app-shell-content">
            <div className="min-w-0 flex-1">
              <OpenCustomSidebar />
            </div>
            <SidebarHost />
          </div>
        </SidebarProvider>
      </I18nextProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Open sidebar" }));
    const sidebar = await screen.findByRole("complementary", { name: "Settings" });
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

async function renderOpenSidebarAtWindowWidth(
  width: number,
  opener: ReactNode = <OpenCustomSidebar />,
): Promise<
  Readonly<{
    restoreWindowWidth(): void;
    sidebar: HTMLElement;
    unmount(): void;
  }>
> {
  const restoreWindowWidth = mockWindowWidth(width);
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

  fireEvent.click(screen.getByRole("button", { name: "Open sidebar" }));

  return {
    restoreWindowWidth,
    sidebar: await screen.findByRole("complementary", { name: "Settings" }),
    unmount,
  };
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
