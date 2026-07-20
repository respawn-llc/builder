import { afterEach, expect, it } from "vitest";

import { BrowserStorageError, readBrowserStorage } from "./browserStorage";

const originalLocalStorage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");

afterEach(() => {
  if (originalLocalStorage === undefined) {
    Reflect.deleteProperty(globalThis, "localStorage");
    return;
  }
  Object.defineProperty(globalThis, "localStorage", originalLocalStorage);
});

it("surfaces unavailable browser storage as a typed read failure", () => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    get() {
      throw new DOMException("blocked", "SecurityError");
    },
  });

  const result = readBrowserStorage("local", "desktop.preference");

  expect(result.ok).toBe(false);
  if (result.ok) {
    throw new Error("expected browser storage read to fail");
  }
  expect(result.error).toBeInstanceOf(BrowserStorageError);
  expect(result.error).toMatchObject({
    area: "local",
    key: "desktop.preference",
    operation: "read",
  });
});
