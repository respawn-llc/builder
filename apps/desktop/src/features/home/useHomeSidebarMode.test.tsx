import { act, renderHook } from "@testing-library/react";

import { useHomeSidebarMode } from "./useHomeSidebarMode";

it("switches Home sidebars between overlay and shift as available width changes", () => {
  const media = controlledMediaQuery(false);
  const original = Object.getOwnPropertyDescriptor(globalThis, "matchMedia");
  Object.defineProperty(globalThis, "matchMedia", {
    configurable: true,
    value: vi.fn(() => media.query),
  });

  try {
    const { result } = renderHook(useHomeSidebarMode);
    expect(result.current).toBe("overlay");

    act(() => {
      media.setMatches(true);
    });
    expect(result.current).toBe("shift");
  } finally {
    if (original === undefined) {
      Reflect.deleteProperty(globalThis, "matchMedia");
    } else {
      Object.defineProperty(globalThis, "matchMedia", original);
    }
  }
});

function controlledMediaQuery(initialMatches: boolean): Readonly<{
  query: MediaQueryList;
  setMatches: (matches: boolean) => void;
}> {
  const target = new EventTarget();
  let matches = initialMatches;
  const query = {
    addEventListener: target.addEventListener.bind(target),
    addListener: ignoreLegacyMediaQueryListener,
    dispatchEvent: target.dispatchEvent.bind(target),
    get matches() {
      return matches;
    },
    media: "(min-width: 1001px)",
    onchange: null,
    removeEventListener: target.removeEventListener.bind(target),
    removeListener: ignoreLegacyMediaQueryListener,
  } satisfies MediaQueryList;
  return {
    query,
    setMatches(next) {
      matches = next;
      target.dispatchEvent(new Event("change"));
    },
  };
}

function ignoreLegacyMediaQueryListener(): void {
  return;
}
