import { act, fireEvent, render, renderHook, screen, within } from "@testing-library/react";
import { StrictMode, useEffect, useLayoutEffect, useState, type ReactNode } from "react";

import {
  useSidebarRoots,
  useSidebarShell,
  type SidebarDestination,
  type SidebarDestinationPolicy,
  type SidebarPageNavigator,
} from "@/app-facade";
import { SidebarProvider } from "./sidebarProvider";
import { useSidebarCurrentPage } from "./sidebarPageContext";
import { SidebarHost } from "./sidebar";

const policy: SidebarDestinationPolicy = {
  equals: (left, right) =>
    left.kind === "custom" && right.kind === "custom" && left.title === right.title,
  retainedState: (_destination, state) => state,
};

function destination(title: string): SidebarDestination {
  return { kind: "custom", title, content: null };
}

function wrapper({ children }: Readonly<{ children: ReactNode }>) {
  return <SidebarProvider policy={policy}>{children}</SidebarProvider>;
}

function strictWrapper({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <StrictMode>
      <SidebarProvider policy={policy}>{children}</SidebarProvider>
    </StrictMode>
  );
}

function useHarness() {
  return {
    page: useSidebarCurrentPage(),
    roots: useSidebarRoots(),
    shell: useSidebarShell(),
  };
}

function requireNavigator(value: ReturnType<typeof useHarness>): SidebarPageNavigator {
  const navigator = value.page?.navigator;
  if (navigator === undefined) {
    throw new Error("Expected a mounted sidebar page.");
  }
  return navigator;
}

function ShellHarness() {
  const roots = useSidebarRoots();
  const page = useSidebarCurrentPage();
  const [available, setAvailable] = useState(true);

  useLayoutEffect(() => {
    const root = roots.open({
      kind: "custom",
      title: "A",
      content: <div data-testid="page-a" />,
    });
    return root.release;
  }, [roots]);

  useEffect(() => {
    if (page === null) {
      return;
    }
    const detachCapture = page.navigator.registerCapture(() => ({ page: "A" }));
    const detachAvailability = page.navigator.registerAvailability({
      back: available,
      close: available,
    });
    return () => {
      detachAvailability();
      detachCapture();
    };
  }, [available, page]);

  return (
    <>
      <button
        data-testid="push-page"
        onClick={() => {
          page?.navigator.push({
            kind: "custom",
            title: "B",
            content: <div data-testid="page-b" />,
          });
        }}
        type="button"
      />
      <button
        data-testid="toggle-availability"
        onClick={() => {
          setAvailable((current) => !current);
        }}
        type="button"
      />
      <SidebarHost />
    </>
  );
}

describe("SidebarProvider stack", () => {
  it("pushes, restores snapshots, truncates a matching branch, replaces, and goes Back", async () => {
    const { result } = renderHook(useHarness, { wrapper });
    let lifecycle: Promise<unknown> | undefined;

    act(() => {
      lifecycle = result.current.roots.open(destination("A")).lifecycle;
    });
    const a = requireNavigator(result.current);
    const captured: string[] = [];
    act(() => {
      a.registerCapture(() => {
        captured.push("A");
        return { draft: "a" };
      });
      expect(a.push(destination("B"))).toBe("accepted");
      expect(a.back()).toBe("stale");
    });
    expect(captured).toEqual(["A"]);
    expect(result.current.shell.activeDestination).toEqual(destination("B"));

    const b = requireNavigator(result.current);
    act(() => {
      b.registerCapture(() => ({ draft: "b" }));
      expect(b.push(destination("C"))).toBe("accepted");
    });
    const c = requireNavigator(result.current);
    act(() => {
      expect(c.push(destination("A"))).toBe("accepted");
    });
    expect(result.current.shell.activeDestination).toEqual(destination("A"));
    expect(result.current.page?.retainedState).toEqual({ draft: "a" });

    const restoredA = requireNavigator(result.current);
    act(() => {
      expect(restoredA.replace(destination("D"))).toBe("accepted");
    });
    const d = requireNavigator(result.current);
    act(() => {
      expect(d.back()).toBe("accepted");
    });
    expect(result.current.shell.phase).toBe("closing");
    await expect(lifecycle).resolves.toBe("closed");
  });

  it("rejects append Push without capture while leaving the page capability active", () => {
    const { result } = renderHook(useHarness, { wrapper });

    act(() => {
      result.current.roots.open(destination("A"));
    });
    const navigator = requireNavigator(result.current);
    act(() => {
      expect(navigator.push(destination("B"))).toBe("unavailable");
      expect(navigator.back()).toBe("accepted");
    });
    expect(result.current.shell.activeDestination).toEqual(destination("A"));
    expect(result.current.shell.phase).toBe("closing");
  });

  it("does not capture discarded pages and revokes before an accepted state update", () => {
    const { result } = renderHook(useHarness, { wrapper });

    act(() => {
      result.current.roots.open(destination("A"));
    });
    const a = requireNavigator(result.current);
    const capture = vi.fn(() => ({ draft: "a" }));
    act(() => {
      a.registerCapture(capture);
      expect(a.replace(destination("B"))).toBe("accepted");
      expect(a.close()).toBe("stale");
    });
    expect(capture).not.toHaveBeenCalled();
    expect(result.current.shell.activeDestination).toEqual(destination("B"));

    const b = requireNavigator(result.current);
    act(() => {
      b.registerCapture(capture);
      result.current.roots.open(destination("C"));
      expect(b.back()).toBe("stale");
    });
    expect(capture).not.toHaveBeenCalled();
    expect(result.current.shell.activeDestination).toEqual(destination("C"));
  });

  it("keeps the root and the newest non-root pages within the 50-entry limit", () => {
    const { result } = renderHook(useHarness, { wrapper });

    act(() => {
      result.current.roots.open(destination("0"));
    });
    for (let index = 1; index <= 51; index += 1) {
      const navigator = requireNavigator(result.current);
      act(() => {
        navigator.registerCapture(() => ({ index: index - 1 }));
        expect(navigator.push(destination(index.toString()))).toBe("accepted");
      });
    }

    const visited: string[] = [];
    while (result.current.shell.canGoBack) {
      const current = result.current.shell.activeDestination;
      if (current?.kind === "custom") {
        visited.push(current.title);
      }
      act(() => {
        expect(requireNavigator(result.current).back()).toBe("accepted");
      });
    }
    expect(visited).toHaveLength(49);
    expect(visited).not.toContain("1");
    expect(result.current.shell.activeDestination).toEqual(destination("0"));
  });

  it("keeps a replayed current-page registration usable without reactivating stale pages", () => {
    const { result } = renderHook(useHarness, { wrapper: strictWrapper });

    act(() => {
      result.current.roots.open(destination("A"));
    });
    const a = requireNavigator(result.current);
    act(() => {
      a.registerCapture(() => ({ draft: "a" }));
      expect(a.push(destination("B"))).toBe("accepted");
    });
    act(() => {
      expect(a.back()).toBe("stale");
      expect(requireNavigator(result.current).back()).toBe("accepted");
    });
    expect(result.current.shell.activeDestination).toEqual(destination("A"));
  });

  it("settles only the owning root for replacement, release, close, and provider teardown", async () => {
    const view = renderHook(useHarness, { wrapper });
    let firstRoot: ReturnType<typeof view.result.current.roots.open> | undefined;
    let secondRoot: ReturnType<typeof view.result.current.roots.open> | undefined;
    act(() => {
      firstRoot = view.result.current.roots.open(destination("A"));
    });
    const oldPage = requireNavigator(view.result.current);
    let staleClose: ReturnType<typeof oldPage.close> | undefined;
    act(() => {
      secondRoot = view.result.current.roots.open(destination("B"));
      staleClose = oldPage.close();
      firstRoot?.release();
    });
    await expect(firstRoot?.lifecycle).resolves.toBe("replaced");
    expect(staleClose).toBe("stale");
    expect(view.result.current.shell.activeDestination).toEqual(destination("B"));
    act(() => {
      secondRoot?.release();
    });
    await expect(secondRoot?.lifecycle).resolves.toBe("released");
    expect(view.result.current.shell.activeDestination).toBeNull();

    let teardownRoot: ReturnType<typeof view.result.current.roots.open> | undefined;
    act(() => {
      teardownRoot = view.result.current.roots.open(destination("C"));
    });
    const teardownPage = requireNavigator(view.result.current);
    view.unmount();
    await expect(teardownRoot?.lifecycle).resolves.toBe("released");
    expect(teardownPage.close()).toBe("stale");
    teardownRoot?.release();
  });

  it("replays capture and availability registration without clearing a newer registration", () => {
    const { result } = renderHook(useHarness, { wrapper });
    act(() => {
      result.current.roots.open(destination("A"));
    });
    const a = requireNavigator(result.current);
    const firstCapture = vi.fn(() => ({ draft: "old" }));
    const secondCapture = vi.fn(() => ({ draft: "new" }));
    act(() => {
      const detachFirst = a.registerCapture(firstCapture);
      a.registerCapture(secondCapture);
      detachFirst();
      expect(a.push(destination("B"))).toBe("accepted");
    });
    expect(firstCapture).not.toHaveBeenCalled();
    expect(secondCapture).toHaveBeenCalledOnce();

    const b = requireNavigator(result.current);
    let detachAvailability: (() => void) | undefined;
    act(() => {
      detachAvailability = b.registerAvailability({ back: false, close: false });
    });
    expect(result.current.shell.canGoBack).toBe(true);
    expect(result.current.shell.backAvailable).toBe(false);
    expect(result.current.shell.closeAvailable).toBe(false);
    expect(result.current.shell.back()).toBe("unavailable");
    act(() => {
      detachAvailability?.();
    });
    expect(result.current.shell.backAvailable).toBe(true);
    act(() => {
      expect(b.back()).toBe("accepted");
    });
    expect(result.current.page?.retainedState).toEqual({ draft: "new" });
  });

  it("retains widths per profile and keeps the closing page rendered through the exit phase", () => {
    vi.useFakeTimers();
    const { result } = renderHook(useHarness, { wrapper });
    act(() => {
      result.current.roots.open(destination("A"));
      result.current.shell.resize({ px: 430 });
    });
    const a = requireNavigator(result.current);
    act(() => {
      a.registerCapture(() => ({ draft: "a" }));
      expect(
        a.push({
          kind: "custom",
          title: "Wide",
          content: null,
          sizing: { desiredWidthPx: 700, minWidthPx: 400 },
        }),
      ).toBe("accepted");
    });
    expect(result.current.shell.sidebarWidthPx).toBe(700);
    act(() => {
      result.current.shell.resize({ px: 600 });
      expect(requireNavigator(result.current).back()).toBe("accepted");
    });
    expect(result.current.shell.sidebarWidthPx).toBe(430);
    act(() => {
      expect(requireNavigator(result.current).close()).toBe("accepted");
    });
    expect(result.current.shell.phase).toBe("closing");
    expect(result.current.shell.activeDestination).toEqual(destination("A"));
    act(() => {
      vi.runAllTimers();
    });
    expect(result.current.shell.activeDestination).toBeNull();
    vi.useRealTimers();
  });

  it("renders X before Back, hides root Back, and mounts only the current page", () => {
    render(
      <SidebarProvider policy={policy}>
        <ShellHarness />
      </SidebarProvider>,
    );
    const headerButtons = () =>
      within(screen.getByTestId("app-sidebar-leading-controls")).getAllByRole("button");
    expect(headerButtons()).toHaveLength(1);
    expect(screen.getByTestId("page-a")).toBeInTheDocument();

    fireEvent.click(screen.getByTestId("push-page"));
    expect(headerButtons()).toHaveLength(2);
    expect(screen.queryByTestId("page-a")).toBeNull();
    expect(screen.getByTestId("page-b")).toBeInTheDocument();
    expect(screen.getByTestId("app-sidebar-page").getAttribute("data-direction")).toBe("push");

    fireEvent.click(screen.getByTestId("toggle-availability"));
    expect(headerButtons()[0]?.hasAttribute("disabled")).toBe(true);
    expect(headerButtons()[1]?.hasAttribute("disabled")).toBe(true);
    fireEvent.click(screen.getByTestId("toggle-availability"));
    const backButton = headerButtons()[1];
    if (backButton === undefined) {
      throw new Error("Expected nested sidebar Back.");
    }
    fireEvent.click(backButton);

    expect(headerButtons()).toHaveLength(1);
    expect(screen.getByTestId("page-a")).toBeInTheDocument();
    expect(screen.queryByTestId("page-b")).toBeNull();
    expect(screen.getByTestId("app-sidebar-page").getAttribute("data-direction")).toBe("back");
  });
});
