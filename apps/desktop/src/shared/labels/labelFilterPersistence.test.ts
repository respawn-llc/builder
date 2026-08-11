import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { MemoryStorage } from "@/test-support/browser-storage";
import type { AppStorageNamespace } from "@/app-facade";
import { createLabelFilterState } from "./labelFilterState";
import { readPersistedLabelFilterState, writePersistedLabelFilterState } from "./labelFilterPersistence";

const projectID = "project-1";
const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const urgentID = "942495c2-5958-4959-8445-94046ad74fbd";
const namespace: AppStorageNamespace = {
  kind: "browser-endpoint",
  identity: "http://127.0.0.1:8080",
};
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

describe("label filter persistence", () => {
  it("restores an existing version-one included-only record unchanged", () => {
    localStorage.setItem(
      labelFilterStorageKey(),
      JSON.stringify({
        version: 1,
        kind: "named",
        mode: "all",
        labelIDs: [priorityID],
      }),
    );

    expect(readPersistedLabelFilterState(namespace, projectID, [priorityID])).toEqual({
      ok: true,
      state: {
        filter: {
          kind: "named",
          mode: "all",
          labelIDs: [priorityID],
          excludedLabelIDs: [],
        },
        namedMode: "all",
      },
    });
  });

  it("round-trips an excluded partition through the existing version-one record", () => {
    const storageKey = labelFilterStorageKey();
    const state = {
      filter: {
        kind: "named" as const,
        mode: "any" as const,
        labelIDs: [priorityID],
        excludedLabelIDs: [urgentID],
      },
      namedMode: "any" as const,
    };

    expect(writePersistedLabelFilterState(namespace, projectID, state)).toEqual({
      ok: true,
      value: undefined,
    });
    expect(JSON.parse(localStorage.getItem(storageKey) ?? "")).toEqual({
      version: 1,
      kind: "named",
      mode: "any",
      labelIDs: [priorityID],
      excludedLabelIDs: [urgentID],
    });
    expect(readPersistedLabelFilterState(namespace, projectID, [priorityID, urgentID])).toEqual({
      ok: true,
      state,
    });
  });

  it.each([
    {
      name: "overlapping partitions",
      record: {
        version: 1,
        kind: "named",
        mode: "any",
        labelIDs: [priorityID],
        excludedLabelIDs: [priorityID],
      },
      catalogLabelIDs: [priorityID],
    },
    {
      name: "more than one hundred combined conditions",
      record: {
        version: 1,
        kind: "named",
        mode: "any",
        labelIDs: generatedLabelIDs().slice(0, 50),
        excludedLabelIDs: generatedLabelIDs().slice(50),
      },
      catalogLabelIDs: generatedLabelIDs(),
    },
  ])("falls back before reconciliation for $name", ({ record, catalogLabelIDs }) => {
    localStorage.setItem(labelFilterStorageKey(), JSON.stringify(record));

    expect(readPersistedLabelFilterState(namespace, projectID, catalogLabelIDs)).toEqual({
      ok: true,
      state: createLabelFilterState(),
    });
  });
});

function labelFilterStorageKey(): string {
  const result = writePersistedLabelFilterState(namespace, projectID, createLabelFilterState());
  if (!result.ok) {
    throw result.error;
  }
  const key = localStorage.key(0);
  if (key === null) {
    throw new Error("label filter persistence did not create a storage record");
  }
  localStorage.clear();
  return key;
}

function generatedLabelIDs(): readonly string[] {
  return Array.from(
    { length: 101 },
    (_, index) => `00000000-0000-4000-8000-${index.toString().padStart(12, "0")}`,
  );
}
