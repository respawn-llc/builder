import {
  createHistory,
  createMemoryHistory,
  type HistoryLocation,
} from "@tanstack/react-router";

export const sidebarHistoryCapacity = 50;

export type SidebarHistorySnapshot<T, R> = Readonly<{
  destination: T;
  retainedState: R | null;
  key: string;
  canGoBack: boolean;
  direction: "push" | "back" | null;
}>;

export type SidebarHistory<T, R> = Readonly<{
  snapshot(): SidebarHistorySnapshot<T, R> | null;
  subscribe(listener: () => void): () => void;
  push(request: Readonly<{
    sourceKey: string;
    destination: T;
    retainedState: R | null;
    deactivateDestination?: ((destination: T) => T) | undefined;
    sameDestination: (destination: T) => boolean;
  }>): boolean;
  replace(request: Readonly<{
    sourceKey: string;
    destination: T;
    retainedState: R | null;
  }>): boolean;
  back(request: Readonly<{ sourceKey: string; retainedState: R | null }>): boolean;
  remove(predicate: (destination: T) => boolean): Readonly<{
    removedCount: number;
    currentRemoved: boolean;
    empty: boolean;
  }>;
  destroy(): void;
}>;

interface Entry<T, R> {
  destination: T;
  retainedState: R | null;
  location: HistoryLocation;
}

export function createSidebarHistory<T, R>(
  root: T,
  rootRetainedState: R | null = null,
): SidebarHistory<T, R> {
  const seed = createMemoryHistory({ initialEntries: ["/"] });
  let entries: Entry<T, R>[] = [{ destination: root, retainedState: rootRetainedState, location: seed.location }];
  let currentIndex = 0;
  let destroyed = false;
  let direction: SidebarHistorySnapshot<T, R>["direction"] = null;
  let cachedSnapshot: SidebarHistorySnapshot<T, R> | null = null;
  let pending: Readonly<{ destination: T; retainedState: R | null }> | null = null;
  const listeners = new Set<() => void>();

  const location = (path: string, state: HistoryLocation["state"]): HistoryLocation => ({
    href: path,
    pathname: path,
    search: "",
    hash: "",
    state,
  });
  const currentLocation = (): HistoryLocation => {
    const entry = entries[currentIndex];
    if (entry === undefined) throw new Error("Sidebar history has no current location.");
    return entry.location;
  };
  const currentKey = (): string => {
    const key = currentLocation().state.__TSR_key;
    if (key === undefined) throw new Error("Sidebar history location has no TanStack key.");
    return key;
  };
  const notify = (nextDirection: SidebarHistorySnapshot<T, R>["direction"]): void => {
    direction = nextDirection;
    const entry = entries[currentIndex];
    cachedSnapshot =
      entry === undefined
        ? null
        : {
            destination: entry.destination,
            retainedState: entry.retainedState,
            key: currentKey(),
            canGoBack: currentIndex > 0,
            direction,
          };
    listeners.forEach((listener) => { listener(); });
  };
  const rebase = (): void => {
    entries = entries.map((entry, index) => ({
      ...entry,
      location: location(entry.location.href, { ...entry.location.state, __TSR_index: index }),
    }));
    if (entries.length !== 0) history.notify({ type: "REPLACE" });
  };
  const history = createHistory({
    getLocation: currentLocation,
    getLength: () => entries.length,
    pushState: (path, state) => {
      if (pending === null) throw new Error("Sidebar history push has no typed destination.");
      entries = entries.slice(0, currentIndex + 1);
      entries.push({ ...pending, location: location(path, state) });
      currentIndex = entries.length - 1;
      pending = null;
    },
    replaceState: (path, state) => {
      if (pending === null) throw new Error("Sidebar history replace has no typed destination.");
      entries[currentIndex] = { ...pending, location: location(path, state) };
      pending = null;
    },
    go: (offset) => {
      currentIndex = Math.min(Math.max(currentIndex + offset, 0), entries.length - 1);
    },
    back: () => {
      currentIndex = Math.max(currentIndex - 1, 0);
    },
    forward: () => {
      currentIndex = Math.min(currentIndex + 1, entries.length - 1);
    },
    createHref: (path) => path,
    notifyOnIndexChange: false,
  });
  seed.destroy();
  notify(null);

  const refreshCurrent = (
    entry: Readonly<{ destination: T; retainedState: R | null }>,
  ): void => {
    pending = entry;
    history.replace("/", undefined);
  };

  const push = (request: Parameters<SidebarHistory<T, R>["push"]>[0]): boolean => {
    if (destroyed || request.sourceKey !== currentKey()) return false;
    if (request.sameDestination(entries[currentIndex]!.destination)) return false;
    entries[currentIndex] = {
      ...entries[currentIndex]!,
      destination:
        request.deactivateDestination?.(entries[currentIndex]!.destination) ??
        entries[currentIndex]!.destination,
      retainedState: request.retainedState,
    };
    const earlierIndex = entries
      .slice(0, currentIndex)
      .findIndex((entry) => request.sameDestination(entry.destination));
    if (earlierIndex !== -1) {
      entries = entries.slice(0, earlierIndex + 1);
      currentIndex = earlierIndex;
      rebase();
      refreshCurrent(entries[currentIndex]!);
      notify("push");
      return true;
    }
    if (entries.length >= sidebarHistoryCapacity) {
      entries.splice(1, 1);
      currentIndex -= 1;
      rebase();
    }
    pending = { destination: request.destination, retainedState: null };
    history.push("/", undefined);
    notify("push");
    return true;
  };

  const replace = (request: Parameters<SidebarHistory<T, R>["replace"]>[0]): boolean => {
    if (destroyed || request.sourceKey !== currentKey()) return false;
    refreshCurrent(request);
    notify("push");
    return true;
  };

  const back = (request: Parameters<SidebarHistory<T, R>["back"]>[0]): boolean => {
    if (destroyed || request.sourceKey !== currentKey() || currentIndex === 0) return false;
    entries[currentIndex] = { ...entries[currentIndex]!, retainedState: request.retainedState };
    entries = entries.slice(0, currentIndex);
    currentIndex -= 1;
    rebase();
    refreshCurrent(entries[currentIndex]!);
    notify("back");
    return true;
  };

  const remove = (predicate: (destination: T) => boolean) => {
    if (destroyed) return { removedCount: 0, currentRemoved: false, empty: false };
    const matches = entries.map((entry) => predicate(entry.destination));
    const removedCount = matches.filter(Boolean).length;
    if (removedCount === 0) return { removedCount: 0, currentRemoved: false, empty: false };
    const oldIndex = currentIndex;
    const currentRemoved = matches[currentIndex] === true;
    entries = entries.filter((_entry, index) => matches[index] !== true);
    if (entries.length === 0) {
      destroyed = true;
      notify(null);
      return { removedCount, currentRemoved, empty: true };
    }
    currentIndex = oldIndex - matches.slice(0, oldIndex).filter(Boolean).length;
    currentIndex = Math.min(currentIndex, entries.length - 1);
    rebase();
    if (currentRemoved) refreshCurrent(entries[currentIndex]!);
    notify(currentRemoved ? "back" : null);
    return { removedCount, currentRemoved, empty: false };
  };

  return {
    snapshot: () => {
      return cachedSnapshot;
    },
    subscribe: (listener) => {
      if (destroyed) return () => {};
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    push,
    replace,
    back,
    remove,
    destroy: () => {
      destroyed = true;
      pending = null;
      listeners.clear();
      history.destroy();
    },
  };
}
