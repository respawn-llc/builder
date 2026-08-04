import {
  createSidebarHistory,
  sidebarHistoryCapacity,
  type SidebarHistorySnapshot,
} from "./sidebarStack";

type Destination = Readonly<{ name: string }>;

function destination(name: string): Destination {
  return { name };
}

function same(name: string) {
  return (candidate: Destination) => candidate.name === name;
}

function current<T, R>(history: ReturnType<typeof createSidebarHistory<T, R>>) {
  const snapshot = history.snapshot();
  if (snapshot === null) throw new Error("Expected a current sidebar destination.");
  return snapshot;
}

function walkBack<T, R>(
  history: ReturnType<typeof createSidebarHistory<T, R>>,
): SidebarHistorySnapshot<T, R>[] {
  const result: SidebarHistorySnapshot<T, R>[] = [current(history)];
  while (result[0]?.canGoBack === true) {
    const sourceKey = result[0].key;
    expect(history.back({ sourceKey, retainedState: null })).toBe(true);
    result.unshift(current(history));
  }
  return result;
}

describe("createSidebarHistory", () => {
  it("initializes the root with an opaque TanStack key", () => {
    const history = createSidebarHistory(destination("root"));
    const snapshot = current(history);

    expect(snapshot.destination).toEqual(destination("root"));
    expect(typeof snapshot.key).toBe("string");
    expect(snapshot.canGoBack).toBe(false);
  });

  it("pushes, replaces, and truncates an abandoned branch", () => {
    const history = createSidebarHistory(destination("root"));
    const rootKey = current(history).key;

    expect(
      history.push({
        sourceKey: rootKey,
        destination: destination("one"),
        retainedState: { value: "root" },
        sameDestination: same("one"),
      }),
    ).toBe(true);
    expect(current(history).retainedState).toBeNull();
    const oneKey = current(history).key;
    expect(history.push({
      sourceKey: oneKey,
      destination: destination("two"),
      retainedState: null,
      sameDestination: same("two"),
    })).toBe(true);
    const twoKey = current(history).key;
    expect(history.replace({ sourceKey: twoKey, destination: destination("three"), retainedState: null })).toBe(true);
    expect(current(history).destination).toEqual(destination("three"));
    expect(current(history).key).not.toBe(twoKey);

    const threeKey = current(history).key;
    expect(history.back({ sourceKey: threeKey, retainedState: { value: "one" } })).toBe(true);
    const oneAfterBack = current(history);
    expect(oneAfterBack.destination).toEqual(destination("one"));
    expect(oneAfterBack.key).not.toBe(oneKey);
    expect(oneAfterBack.retainedState).toBeNull();
    expect(history.push({
      sourceKey: oneAfterBack.key,
      destination: destination("three"),
      retainedState: null,
      sameDestination: same("three"),
    })).toBe(true);
    expect(walkBack(history).map((entry) => entry.destination.name)).toEqual(["root", "one", "three"]);
  });

  it("returns to an earlier matching destination without duplicates", () => {
    const history = createSidebarHistory(destination("a"));
    const rootKey = current(history).key;
    let key = rootKey;
    for (const name of ["b", "c"]) {
      expect(history.push({
        sourceKey: key,
        destination: destination(name),
        retainedState: null,
        sameDestination: same(name),
      })).toBe(true);
      key = current(history).key;
    }
    key = current(history).key;
    expect(history.push({
      sourceKey: key,
      destination: destination("a"),
      retainedState: null,
      sameDestination: same("a"),
    })).toBe(true);
    expect(current(history).destination).toEqual(destination("a"));
    expect(current(history).key).not.toBe(rootKey);
    expect(current(history).canGoBack).toBe(false);
  });

  it("retains the captured state on the entry that Back restores", () => {
    const history = createSidebarHistory(destination("root"));
    const rootKey = current(history).key;
    expect(history.push({
      sourceKey: rootKey,
      destination: destination("child"),
      retainedState: { draft: "root" },
      sameDestination: same("child"),
    })).toBe(true);
    const childKey = current(history).key;

    expect(history.back({ sourceKey: childKey, retainedState: { draft: "child" } })).toBe(true);
    expect(current(history).retainedState).toEqual({ draft: "root" });
    expect(current(history).direction).toBe("back");
  });

  it("preserves the root and evicts the oldest non-root destination at capacity", () => {
    const history = createSidebarHistory(destination("0"));
    let key = current(history).key;
    for (let index = 1; index < sidebarHistoryCapacity; index += 1) {
      expect(history.push({
        sourceKey: key,
        destination: destination(index.toString()),
        retainedState: null,
        sameDestination: same(index.toString()),
      })).toBe(true);
      key = current(history).key;
    }
    expect(history.push({
      sourceKey: key,
      destination: destination(sidebarHistoryCapacity.toString()),
      retainedState: null,
      sameDestination: same(sidebarHistoryCapacity.toString()),
    })).toBe(true);

    expect(walkBack(history).map((entry) => entry.destination.name)).toEqual([
      "0",
      ...Array.from({ length: sidebarHistoryCapacity - 2 }, (_, index) => (index + 2).toString()),
      sidebarHistoryCapacity.toString(),
    ]);
  });

  it("removes every matching entry, rebases positions, and activates a survivor with a fresh key", () => {
    const history = createSidebarHistory(destination("root"));
    let key = current(history).key;
    for (const name of ["remove-a", "keep", "remove-b"]) {
      expect(history.push({
        sourceKey: key,
        destination: destination(name),
        retainedState: null,
        sameDestination: same(name),
      })).toBe(true);
      key = current(history).key;
    }
    const oldCurrentKey = current(history).key;
    const removed = history.remove((candidate) => candidate.name !== "keep" && candidate.name !== "root");

    expect(removed).toEqual({ removedCount: 2, currentRemoved: true, empty: false });
    expect(current(history).destination).toEqual(destination("keep"));
    expect(current(history).key).not.toBe(oldCurrentKey);
    expect(current(history).canGoBack).toBe(true);
    expect(walkBack(history).map((entry) => entry.destination.name)).toEqual(["root", "keep"]);
  });

  it("removes an inactive entry without changing the active key", () => {
    const history = createSidebarHistory(destination("root"));
    let key = current(history).key;
    for (const name of ["remove", "keep"]) {
      expect(history.push({
        sourceKey: key,
        destination: destination(name),
        retainedState: null,
        sameDestination: same(name),
      })).toBe(true);
      key = current(history).key;
    }
    const activeKey = current(history).key;

    expect(history.remove(same("remove"))).toEqual({
      removedCount: 1,
      currentRemoved: false,
      empty: false,
    });
    expect(current(history).key).toBe(activeKey);
    expect(current(history).canGoBack).toBe(true);
    expect(history.push({
      sourceKey: activeKey,
      destination: destination("next"),
      retainedState: null,
      sameDestination: same("next"),
    })).toBe(true);
    expect(walkBack(history).map((entry) => entry.destination.name)).toEqual(["root", "keep", "next"]);
  });

  it("reports empty when the final matching entry is removed and becomes inert", () => {
    const history = createSidebarHistory(destination("root"));
    expect(history.remove(same("root"))).toEqual({
      removedCount: 1,
      currentRemoved: true,
      empty: true,
    });
    expect(history.snapshot()).toBeNull();
    expect(history.push({
      sourceKey: "stale",
      destination: destination("new"),
      retainedState: null,
      sameDestination: same("new"),
    })).toBe(false);
  });

  it("notifies once per accepted operation and rejects stale source keys", () => {
    const history = createSidebarHistory(destination("root"));
    let notifications = 0;
    history.subscribe(() => {
      notifications += 1;
    });
    const rootKey = current(history).key;

    expect(history.push({
      sourceKey: "stale",
      destination: destination("ignored"),
      retainedState: null,
      sameDestination: same("ignored"),
    })).toBe(false);
    expect(notifications).toBe(0);

    expect(history.push({
      sourceKey: rootKey,
      destination: destination("accepted"),
      retainedState: null,
      sameDestination: same("accepted"),
    })).toBe(true);
    expect(notifications).toBe(1);
    const key = current(history).key;
    expect(history.back({ sourceKey: "stale", retainedState: null })).toBe(false);
    expect(notifications).toBe(1);
    expect(history.back({ sourceKey: key, retainedState: null })).toBe(true);
    expect(notifications).toBe(2);
  });

  it("destroyed history ignores all later commands", () => {
    const history = createSidebarHistory(destination("root"));
    const before = current(history);
    history.destroy();

    expect(history.snapshot()).toEqual(before);
    expect(history.replace({ sourceKey: before.key, destination: destination("new"), retainedState: null })).toBe(
      false,
    );
    expect(history.back({ sourceKey: before.key, retainedState: null })).toBe(false);
    expect(history.remove(same("root"))).toEqual({
      removedCount: 0,
      currentRemoved: false,
      empty: false,
    });
  });
});
