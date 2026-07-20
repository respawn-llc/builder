import { installTestStorage } from "@/test-support/storage";
import type { AppStorageNamespace } from "@/app-facade";
import {
  createLabelFilterState,
  reduceLabelFilterState,
  readPersistedLabelFilterState,
  writePersistedLabelFilterState,
} from "./index";

const namespace = {
  kind: "native-persistence-root",
  identity: "/Users/nek/.kent",
} as const satisfies AppStorageNamespace;
const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const urgentID = "942495c2-5958-4959-8445-94046ad74fbd";

describe("label filter persistence", () => {
  beforeEach(() => {
    installTestStorage("localStorage");
  });

  it("restores the Project filter after relaunch", () => {
    const selected = reduceLabelFilterState(createLabelFilterState(), {
      type: "named.toggle",
      labelID: priorityID,
    });
    const all = reduceLabelFilterState(selected, { type: "named.mode", mode: "all" });

    expect(writePersistedLabelFilterState(namespace, "project-1", all)).toEqual({
      ok: true,
      value: undefined,
    });
    expect(readPersistedLabelFilterState(namespace, "project-1", [priorityID])).toEqual({
      ok: true,
      state: all,
    });
  });

  it("prunes missing catalog IDs from persisted state", () => {
    const priority = reduceLabelFilterState(createLabelFilterState(), {
      type: "named.toggle",
      labelID: priorityID,
    });
    const both = reduceLabelFilterState(priority, {
      type: "named.toggle",
      labelID: urgentID,
    });
    expect(writePersistedLabelFilterState(namespace, "project-1", both).ok).toBe(true);

    expect(readPersistedLabelFilterState(namespace, "project-1", [priorityID])).toEqual({
      ok: true,
      state: {
        filter: { kind: "named", mode: "any", labelIDs: [priorityID] },
        namedMode: "any",
      },
    });
    expect(readPersistedLabelFilterState(namespace, "project-1", [priorityID, urgentID])).toEqual({
      ok: true,
      state: {
        filter: { kind: "named", mode: "any", labelIDs: [priorityID] },
        namedMode: "any",
      },
    });
  });

  it("ignores malformed persisted data", () => {
    const storage = installTestStorage("localStorage");
    expect(writePersistedLabelFilterState(namespace, "project-1", createLabelFilterState()).ok).toBe(true);
    const key = storage.key(0);
    if (key === null) {
      throw new Error("label filter persistence did not create a storage key");
    }
    storage.setItem(key, JSON.stringify({ version: 1, kind: "named", mode: "any" }));

    expect(readPersistedLabelFilterState(namespace, "project-1", [priorityID])).toEqual({
      ok: true,
      state: createLabelFilterState(),
    });
  });

  it("surfaces unavailable local storage while retaining an unrestricted fallback", () => {
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      get() {
        throw new DOMException("blocked", "SecurityError");
      },
    });

    const result = readPersistedLabelFilterState(namespace, "project-1", [priorityID]);

    expect(result.ok).toBe(false);
    if (result.ok) {
      throw new Error("expected label filter persistence to surface unavailable storage");
    }
    expect(result.state).toEqual(createLabelFilterState());
    expect(result.error).toMatchObject({
      area: "local",
      operation: "read",
    });
  });

  it("keys IDs and mode by installation namespace plus Project", () => {
    const storage = installTestStorage("localStorage");
    const selected = reduceLabelFilterState(createLabelFilterState(), {
      type: "named.toggle",
      labelID: priorityID,
    });
    expect(writePersistedLabelFilterState(namespace, "project-1", selected).ok).toBe(true);

    expect(readPersistedLabelFilterState(namespace, "project-2", [priorityID]).state).toEqual(
      createLabelFilterState(),
    );
    expect(
      readPersistedLabelFilterState(
        {
          kind: "browser-endpoint",
          identity: "ws://127.0.0.1:53082/rpc",
        },
        "project-1",
        [priorityID],
      ).state,
    ).toEqual(createLabelFilterState());

    const key = storage.key(0);
    if (key === null) {
      throw new Error("label filter persistence did not create a storage key");
    }
    expect(JSON.parse(storage.getItem(key) ?? "null")).toEqual({
      version: 1,
      kind: "named",
      mode: "any",
      labelIDs: [priorityID],
    });
  });
});
