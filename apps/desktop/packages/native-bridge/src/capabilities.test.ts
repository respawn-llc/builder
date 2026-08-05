import { afterEach, describe, expect, it, vi } from "vitest";

import { createBrowserCapabilities } from "./capabilities";

describe("browser host platform detection", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it.each([
    ["MacIntel", "macos"],
    ["Win32", "windows"],
    ["Linux x86_64", "linux"],
    ["Linux armv8l", "linux"],
    ["unknown", "unknown"],
  ] as const)("maps the browser token %s to %s", (platform, expected) => {
    vi.stubGlobal("navigator", { platform });
    expect(createBrowserCapabilities("browser").hostPlatform).toBe(expected);
  });
});
