import { createElement, Fragment, type ComponentType, type ReactNode } from "react";
import type { SidebarDestination, SidebarDestinationPolicy, SidebarNavigationOutcome, SidebarPageNavigator, SidebarPhase, SidebarRootHandle, SidebarRootOutcome, SidebarTransitionDirection } from "@/app-facade";
const stackLimit = 50;
const exitAnimationMs = 140;
interface Capability { active: boolean; availability: Readonly<{ back: boolean; close: boolean }> | null; capture: Readonly<{ read: () => unknown }> | null; rootBack: (() => void) | undefined }
interface PendingRoot { resolve(outcome: SidebarRootOutcome): void; settled: boolean }
export interface SidebarStackEntry { readonly Boundary: ComponentType<Readonly<{ children: ReactNode }>>; readonly capability: Capability; readonly destination: SidebarDestination; readonly navigator: SidebarPageNavigator; readonly retainedState?: unknown }
export interface SidebarStackView { readonly entries: readonly SidebarStackEntry[]; readonly phase: SidebarPhase; readonly transitionDirection: SidebarTransitionDirection | null }

export const emptySidebarStackView: SidebarStackView = { entries: [], phase: "open", transitionDirection: null };
export function createSidebarStack(policy: SidebarDestinationPolicy, publish: (view: SidebarStackView) => void) {
  let view = emptySidebarStackView;
  let root: PendingRoot | undefined;
  let closeTimeout: ReturnType<typeof setTimeout> | undefined;
  const emit = (entries: readonly SidebarStackEntry[], phase: SidebarPhase, direction: SidebarTransitionDirection | null) => {
    view = { entries, phase, transitionDirection: direction };
    publish(view);
  };
  const current = () => view.entries.at(-1);
  const revokeCurrent = () => {
    const entry = current();
    if (entry !== undefined) {
      entry.capability.active = false;
    }
  };
  const settle = (pending: PendingRoot, outcome: SidebarRootOutcome) => {
    if (pending.settled) {
      return;
    }
    pending.settled = true;
    if (root === pending) {
      root = undefined;
    }
    pending.resolve(outcome);
  };
  const clearCloseTimeout = () => {
    if (closeTimeout !== undefined) {
      clearTimeout(closeTimeout);
      closeTimeout = undefined;
    }
  };
  const notify = (capability: Capability) => {
    if (capability.active && current()?.capability === capability) {
      emit(view.entries, view.phase, view.transitionDirection);
    }
  };
  const createEntry = (
    destination: SidebarDestination,
    retainedState?: unknown,
    rootBack?: () => void,
  ): SidebarStackEntry => {
    const capability: Capability = { active: true, availability: null, capture: null, rootBack };
    const Boundary = ({ children }: Readonly<{ children: ReactNode }>) => createElement(Fragment, null, children);
    const navigator: SidebarPageNavigator = {
      back: () => {
        if (capability.rootBack !== undefined) {
          capability.rootBack();
          return "accepted";
        }
        return back(capability);
      },
      close: () => close(capability),
      push: (next) => push(capability, next),
      replace: (next) => replace(capability, next),
      registerAvailability: (availability) => {
        const registration = { ...availability };
        capability.availability = registration;
        notify(capability);
        return () => {
          if (capability.availability === registration) {
            capability.availability = null;
            notify(capability);
          }
        };
      },
      registerCapture: (read) => {
        const registration = { read };
        capability.capture = registration;
        return () => {
          if (capability.capture === registration) {
            capability.capture = null;
          }
        };
      },
    };
    const entry = { Boundary, capability, destination, navigator };
    return retainedState === undefined
      ? entry
      : { ...entry, retainedState: policy.retainedState(destination, retainedState) };
  };
  const close = (capability: Capability): Exclude<SidebarNavigationOutcome, "unavailable"> => {
    if (!capability.active) {
      return "stale";
    }
    capability.active = false;
    if (root !== undefined) {
      settle(root, "closed");
    }
    clearCloseTimeout();
    emit(view.entries, "closing", null);
    closeTimeout = setTimeout(() => {
      closeTimeout = undefined;
      emit([], "open", null);
    }, exitAnimationMs);
    return "accepted";
  };
  const back = (capability: Capability): Exclude<SidebarNavigationOutcome, "unavailable"> => {
    if (!capability.active) {
      return "stale";
    }
    if (view.entries.length === 1) {
      return close(capability);
    }
    capability.active = false;
    const previous = view.entries.at(-2);
    if (previous === undefined) {
      throw new Error("Sidebar Back requires a previous page.");
    }
    emit(
      [
        ...view.entries.slice(0, -2),
        createEntry(previous.destination, previous.retainedState, previous.capability.rootBack),
      ],
      "open",
      "back",
    );
    return "accepted";
  };
  const replace = (capability: Capability, destination: SidebarDestination): Exclude<SidebarNavigationOutcome, "unavailable"> => {
    if (!capability.active) {
      return "stale";
    }
    capability.active = false;
    emit(
      [...view.entries.slice(0, -1), createEntry(destination)],
      "open",
      "replace",
    );
    return "accepted";
  };
  const push = (
    capability: Capability,
    destination: SidebarDestination,
    rootBack?: () => void,
  ): SidebarNavigationOutcome => {
    if (!capability.active) {
      return "stale";
    }
    const retained = findRetainedEntry(view.entries, destination, policy);
    if (retained !== undefined) {
      capability.active = false;
      emit(
        [
          ...view.entries.slice(0, retained.index),
          createEntry(retained.entry.destination, retained.entry.retainedState, rootBack),
        ],
        "open",
        "back",
      );
      return "accepted";
    }
    if (capability.capture === null) {
      return "unavailable";
    }
    const retainedState = capability.capture.read();
    capability.active = false;
    const currentEntry = current();
    if (currentEntry === undefined) {
      throw new Error("Sidebar Push requires a current page.");
    }
    const appended = [
      ...view.entries.slice(0, -1),
      { ...currentEntry, retainedState },
      createEntry(destination, undefined, rootBack),
    ];
    const rootEntry = appended[0];
    if (rootEntry === undefined) {
      throw new Error("Sidebar Push lost its root page.");
    }
    emit(appended.length <= stackLimit ? appended : [rootEntry, ...appended.slice(-(stackLimit - 1))], "open", "push");
    return "accepted";
  };
  const open = (destination: SidebarDestination, onBack?: () => void): SidebarRootHandle => {
    clearCloseTimeout();
    revokeCurrent();
    if (root !== undefined) {
      settle(root, "replaced");
    }
    let resolveLifecycle: ((outcome: SidebarRootOutcome) => void) | undefined;
    const lifecycle = new Promise<SidebarRootOutcome>((resolve) => { resolveLifecycle = resolve; });
    if (resolveLifecycle === undefined) {
      throw new Error("Sidebar lifecycle promise did not initialize.");
    }
    const ownedRoot: PendingRoot = { resolve: resolveLifecycle, settled: false };
    root = ownedRoot;
    emit([createEntry(destination, undefined, onBack)], "open", "replace");
    return {
      lifecycle,
      push: (next) => {
        if (ownedRoot.settled || root !== ownedRoot) {
          return "stale";
        }
        const entry = current();
        return entry === undefined ? "stale" : push(entry.capability, next, onBack);
      },
      release: () => {
        if (ownedRoot.settled || root !== ownedRoot) {
          return;
        }
        revokeCurrent();
        settle(ownedRoot, "released");
        clearCloseTimeout();
        emit([], "open", null);
      },
    };
  };
  return { dispose: () => {
      clearCloseTimeout();
      revokeCurrent();
      if (root !== undefined) {
        settle(root, "released");
      }
    }, open };
}

function findRetainedEntry(
  entries: readonly SidebarStackEntry[],
  destination: SidebarDestination,
  policy: SidebarDestinationPolicy,
): Readonly<{ entry: SidebarStackEntry; index: number }> | undefined {
  for (const [index, entry] of entries.slice(0, -1).entries()) {
    if (policy.equals(entry.destination, destination)) return { entry, index };
  }
  return undefined;
}
