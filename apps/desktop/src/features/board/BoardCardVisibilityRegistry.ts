import { createContext, useCallback, useContext, useSyncExternalStore } from "react";

import { boardCardInstanceKey, type BoardCardInstance, type BoardCardInstanceKey } from "./BoardCardInstance";

interface BoardCardInstanceEntry {
  readonly instance: BoardCardInstance;
  readonly listeners: Set<() => void>;
  element: HTMLElement | null;
  visible: boolean;
}

export class BoardCardVisibilityStore {
  readonly #entries = new Map<BoardCardInstanceKey, BoardCardInstanceEntry>();
  readonly #keyByElement = new WeakMap<Element, BoardCardInstanceKey>();
  #observer: IntersectionObserver | null = null;

  register(instance: BoardCardInstance, element: HTMLElement | null): void {
    const key = boardCardInstanceKey(instance);
    const entry = this.#entry(key, instance);
    if (entry.element !== null) {
      this.#observer?.unobserve(entry.element);
      this.#keyByElement.delete(entry.element);
    }
    entry.element = element;
    if (element === null) {
      this.#setVisible(entry, false);
      this.#deleteUnusedEntry(key, entry);
      return;
    }
    this.#keyByElement.set(element, key);
    if (typeof IntersectionObserver === "undefined") {
      this.#setVisible(entry, true);
      return;
    }
    this.#observer ??= new IntersectionObserver((entries) => {
      for (const observed of entries) {
        const observedKey = this.#keyByElement.get(observed.target);
        if (observedKey === undefined) {
          continue;
        }
        const observedEntry = this.#entries.get(observedKey);
        if (observedEntry !== undefined) {
          this.#setVisible(observedEntry, observed.isIntersecting);
        }
      }
    });
    this.#observer.observe(element);
  }

  subscribe(instance: BoardCardInstance, listener: () => void): () => void {
    const key = boardCardInstanceKey(instance);
    const entry = this.#entry(key, instance);
    entry.listeners.add(listener);
    return () => {
      entry.listeners.delete(listener);
      this.#deleteUnusedEntry(key, entry);
    };
  }

  isVisible(instance: BoardCardInstance): boolean {
    return this.#entries.get(boardCardInstanceKey(instance))?.visible ?? false;
  }

  visibleTaskIDs(): ReadonlySet<string> {
    return new Set(
      Array.from(this.#entries.values())
        .filter((entry) => entry.visible)
        .map((entry) => entry.instance.taskID),
    );
  }

  elementForUniqueTask(taskID: string): HTMLElement | undefined {
    let unique: HTMLElement | undefined;
    for (const entry of this.#entries.values()) {
      if (entry.instance.taskID !== taskID || entry.element === null) {
        continue;
      }
      if (unique !== undefined) {
        return undefined;
      }
      unique = entry.element;
    }
    return unique;
  }

  destroy(): void {
    this.#observer?.disconnect();
    this.#observer = null;
    this.#entries.clear();
  }

  #entry(key: BoardCardInstanceKey, instance: BoardCardInstance): BoardCardInstanceEntry {
    const existing = this.#entries.get(key);
    if (existing !== undefined) {
      return existing;
    }
    const entry: BoardCardInstanceEntry = {
      instance,
      listeners: new Set(),
      element: null,
      visible: false,
    };
    this.#entries.set(key, entry);
    return entry;
  }

  #setVisible(entry: BoardCardInstanceEntry, visible: boolean): void {
    if (entry.visible === visible) {
      return;
    }
    entry.visible = visible;
    for (const listener of entry.listeners) {
      listener();
    }
  }

  #deleteUnusedEntry(key: BoardCardInstanceKey, entry: BoardCardInstanceEntry): void {
    if (entry.element === null && entry.listeners.size === 0) {
      this.#entries.delete(key);
    }
  }
}

export const BoardCardVisibilityContext = createContext<BoardCardVisibilityStore | null>(null);

export function useBoardCardInstanceVisibility(instance: BoardCardInstance): boolean {
  const store = useContext(BoardCardVisibilityContext);
  const columnID = instance.columnID;
  const taskID = instance.taskID;
  const subscribe = useCallback(
    (listener: () => void) => store?.subscribe({ columnID, taskID }, listener) ?? (() => undefined),
    [columnID, store, taskID],
  );
  const getSnapshot = useCallback(
    () => store?.isVisible({ columnID, taskID }) ?? true,
    [columnID, store, taskID],
  );
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}
