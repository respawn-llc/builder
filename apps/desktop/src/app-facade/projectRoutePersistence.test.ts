import { afterEach, beforeEach, expect, it } from "vitest";

import { MemoryStorage } from "@/test-support/browser-storage";
import { readLastProjectRoute, writeLastProjectRoute } from "./projectRoutePersistence";

const storageKey = "desktop.lastProjectRoute";
const canonicalWorkflowID = "7e8d24d2-8a98-4dcf-a197-6214db1cb3c0";
const originalLocalStorage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");

beforeEach(() => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: new MemoryStorage(),
  });
});

afterEach(() => {
  if (originalLocalStorage === undefined) {
    Reflect.deleteProperty(globalThis, "localStorage");
    return;
  }
  Object.defineProperty(globalThis, "localStorage", originalLocalStorage);
});

it("restores an omitted workflow selector", () => {
  localStorage.setItem(storageKey, JSON.stringify({ projectId: "project-1" }));

  expect(readLastProjectRoute()).toEqual({ projectId: "project-1" });
});

it("rejects a persisted malformed present workflow selector", () => {
  localStorage.setItem(storageKey, JSON.stringify({ projectId: "project-1", workflowId: "workflow-1" }));

  expect(readLastProjectRoute()).toBeNull();
});

it("writes a canonical workflow selector unchanged", () => {
  writeLastProjectRoute({ projectId: "project-1", workflowId: canonicalWorkflowID });

  expect(readLastProjectRoute()).toEqual({ projectId: "project-1", workflowId: canonicalWorkflowID });
});

it("persists no Project workspace tab", () => {
  writeLastProjectRoute({ projectId: "project-1" });

  expect(JSON.parse(localStorage.getItem(storageKey) ?? "null")).toEqual({
    projectId: "project-1",
  });
});
