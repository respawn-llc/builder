import { afterEach, beforeEach, expect, it } from "vitest";

import {
  readLastProjectRoute,
  writeLastProjectContentTab,
  writeLastProjectRoute,
} from "./projectRoutePersistence";

const storageKey = "desktop.lastProjectRoute";
const canonicalWorkflowID = "7e8d24d2-8a98-4dcf-a197-6214db1cb3c0";
const originalLocalStorage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");

beforeEach(() => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: new TestStorage(),
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
  localStorage.setItem(storageKey, JSON.stringify({ kind: "workflow_board", projectId: "project-1" }));

  expect(readLastProjectRoute()).toEqual({ kind: "workflow_board", projectId: "project-1" });
});

it("rejects a legacy Home Project route without a content tab", () => {
  localStorage.setItem(storageKey, JSON.stringify({ kind: "home_project", projectId: "project-1" }));

  expect(readLastProjectRoute()).toBeNull();
});

it("rejects a persisted malformed present workflow selector", () => {
  localStorage.setItem(
    storageKey,
    JSON.stringify({ kind: "workflow_board", projectId: "project-1", workflowId: "workflow-1" }),
  );

  expect(readLastProjectRoute()).toBeNull();
});

it("writes a canonical workflow selector unchanged", () => {
  writeLastProjectRoute({
    kind: "workflow_board",
    projectId: "project-1",
    workflowId: canonicalWorkflowID,
  });

  expect(readLastProjectRoute()).toEqual({
    kind: "workflow_board",
    projectId: "project-1",
    workflowId: canonicalWorkflowID,
  });
});

it("writes a Home Project destination unchanged", () => {
  writeLastProjectRoute({ contentTab: "sessions", kind: "home_project", projectId: "project-1" });

  expect(readLastProjectRoute()).toEqual({
    contentTab: "sessions",
    kind: "home_project",
    projectId: "project-1",
  });
});

it("updates the persisted Home Project content tab", () => {
  writeLastProjectRoute({ contentTab: "tasks", kind: "home_project", projectId: "project-1" });

  writeLastProjectContentTab("project-1", "sessions");

  expect(readLastProjectRoute()).toEqual({
    contentTab: "sessions",
    kind: "home_project",
    projectId: "project-1",
  });
});

class TestStorage implements Storage {
  #entries = new Map<string, string>();

  get length(): number {
    return this.#entries.size;
  }

  clear(): void {
    this.#entries.clear();
  }

  getItem(key: string): string | null {
    return this.#entries.get(key) ?? null;
  }

  key(index: number): string | null {
    return [...this.#entries.keys()][index] ?? null;
  }

  removeItem(key: string): void {
    this.#entries.delete(key);
  }

  setItem(key: string, value: string): void {
    this.#entries.set(key, value);
  }
}
