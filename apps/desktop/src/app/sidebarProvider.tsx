import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import {
  SidebarContext,
  runViewTransition,
  type SidebarCanceledResult,
  type SidebarCancelReason,
  type SidebarController,
  type SidebarDestination,
  type SidebarEntryToken,
  type SidebarPhase,
  sidebarRouteMatchesExpectation,
  type SidebarRouteChangeExpectation,
  type SidebarRouteLocation,
  type SidebarResult,
  type SidebarStateCapture,
} from "@/app-facade";
import {
  sidebarSizePreference,
  sidebarWidthProfile,
  sidebarWidthProfileEquals,
  type SidebarWidthProfile,
} from "@/app-facade";
import { initialSidebarWidthForViewport, type ResolvedSidebarWidth } from "@/app-facade";
import {
  findTaskDetailIndex,
  sidebarStackReducer,
  type SidebarStackAction,
  type SidebarStackState,
} from "./sidebarStack";

const sidebarExitAnimationMs = 140;
type SidebarWidthEntry = Readonly<{
  profile: SidebarWidthProfile;
  widthPx: number;
}>;
type SidebarWidths = readonly SidebarWidthEntry[];
const defaultSidebarWidthProfile: SidebarWidthProfile = { kind: "custom", sizing: null };

type PendingSidebar = Readonly<{
  lifecycleID: string;
  resolve: (result: SidebarResult) => void;
}>;
type PendingSidebarRoutePreservation = Readonly<{
  expectation: SidebarRouteChangeExpectation;
  token: SidebarEntryToken;
}>;

export function SidebarProvider({ children }: Readonly<{ children: ReactNode }>) {
  const [stackState, setStackState] = useState<SidebarStackState | null>(null);
  const [activeWidthProfile, setActiveWidthProfile] =
    useState<SidebarWidthProfile>(defaultSidebarWidthProfile);
  const [phase, setPhase] = useState<SidebarPhase>("open");
  const [sidebarWidths, setSidebarWidths] = useState<SidebarWidths>(() => [
    { profile: defaultSidebarWidthProfile, widthPx: defaultSidebarWidth() },
  ]);
  const [exitBlockedToken, setExitBlockedToken] = useState<SidebarEntryToken | null>(null);
  const stackStateRef = useRef<SidebarStackState | null>(stackState);
  const pendingRef = useRef<PendingSidebar | null>(null);
  const captureRef = useRef(new Map<string, SidebarStateCapture>());
  const preserveRouteChangeRef = useRef<PendingSidebarRoutePreservation | null>(null);
  const closeTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const nextIDRef = useRef(0);

  const nextID = useCallback((prefix: "activation" | "lifecycle" | "entry"): string => {
    nextIDRef.current += 1;
    return `${prefix}-${nextIDRef.current.toString()}`;
  }, []);

  const clearCloseTimeout = useCallback(() => {
    if (closeTimeoutRef.current !== null) {
      clearTimeout(closeTimeoutRef.current);
      closeTimeoutRef.current = null;
    }
  }, []);

  const dispatchStack = useCallback((action: SidebarStackAction) => {
    setStackState((current) => {
      const next = sidebarStackReducer(current, action);
      stackStateRef.current = next;
      return next;
    });
  }, []);

  const clearCapture = useCallback((token: SidebarEntryToken) => {
    captureRef.current.delete(tokenKey(token));
  }, []);

  const clearAllCaptures = useCallback(() => {
    captureRef.current.clear();
  }, []);

  const isLiveEntryToken = useCallback((token: SidebarEntryToken): boolean => {
    const state = stackStateRef.current;
    return (
      state !== null &&
      pendingRef.current?.lifecycleID === token.lifecycleID &&
      state.lifecycleID === token.lifecycleID &&
      state.entries.some((entry) => entry.entryID === token.entryID)
    );
  }, []);

  const isCurrentToken = useCallback(
    (token: SidebarEntryToken): boolean => {
      if (!isLiveEntryToken(token)) {
        return false;
      }
      return stackStateRef.current?.entries.at(-1)?.entryID === token.entryID;
    },
    [isLiveEntryToken],
  );

  const activeTokenForState = useCallback((state: SidebarStackState | null): SidebarEntryToken | null => {
    const activeEntry = state?.entries.at(-1);
    return activeEntry === undefined || state === null
      ? null
      : { entryID: activeEntry.entryID, lifecycleID: state.lifecycleID };
  }, []);

  const animateClosed = useCallback(
    (lifecycleID: string) => {
      clearCloseTimeout();
      setPhase("closing");
      closeTimeoutRef.current = setTimeout(() => {
        closeTimeoutRef.current = null;
        if (stackStateRef.current?.lifecycleID !== lifecycleID) {
          return;
        }
        dispatchStack({ type: "close", lifecycleID });
        setPhase("open");
      }, sidebarExitAnimationMs);
    },
    [clearCloseTimeout, dispatchStack],
  );

  const closeSidebar = useCallback(
    (reason: SidebarCancelReason = "closed") => {
      const state = stackStateRef.current;
      const pending = pendingRef.current;
      setExitBlockedToken(null);
      pendingRef.current = null;
      preserveRouteChangeRef.current = null;
      clearAllCaptures();
      pending?.resolve({ status: "canceled", reason });
      if (state !== null) {
        animateClosed(state.lifecycleID);
      }
    },
    [animateClosed, clearAllCaptures],
  );

  const preserveSidebarOnNextRouteChange = useCallback(
    (token: SidebarEntryToken, expectation: SidebarRouteChangeExpectation) => {
      if (!isLiveEntryToken(token)) {
        return;
      }
      preserveRouteChangeRef.current = { expectation, token };
    },
    [isLiveEntryToken],
  );

  const clearSidebarRouteChangePreservation = useCallback((token: SidebarEntryToken) => {
    const pending = preserveRouteChangeRef.current;
    if (pending?.token.lifecycleID === token.lifecycleID && pending.token.entryID === token.entryID) {
      preserveRouteChangeRef.current = null;
    }
  }, []);

  const consumeSidebarRouteChangePreservation = useCallback((location: SidebarRouteLocation): boolean => {
    const pending = preserveRouteChangeRef.current;
    preserveRouteChangeRef.current = null;
    const state = stackStateRef.current;
    return (
      pending !== null &&
      state !== null &&
      state.lifecycleID === pending.token.lifecycleID &&
      state.entries.length > 0 &&
      sidebarRouteMatchesExpectation(location, pending.expectation)
    );
  }, []);

  const openSidebar = useCallback(
    async (destination: SidebarDestination): Promise<SidebarResult> => {
      clearCloseTimeout();
      setExitBlockedToken(null);
      const previousPending = pendingRef.current;
      pendingRef.current = null;
      preserveRouteChangeRef.current = null;
      clearAllCaptures();
      previousPending?.resolve({ status: "canceled", reason: "replaced" });

      const lifecycleID = nextID("lifecycle");
      const entryID = nextID("entry");
      const activationID = nextID("activation");
      const nextProfile = sidebarWidthProfile(destination);
      setActiveWidthProfile(nextProfile);
      setSidebarWidths((current) => {
        if (sidebarWidthForProfile(current, nextProfile) !== undefined) {
          return current;
        }
        return [...current, { profile: nextProfile, widthPx: defaultSidebarWidth(destination) }];
      });
      setPhase("open");
      dispatchStack({
        type: "open",
        activationID,
        lifecycleID,
        entryID,
        destination,
      });
      return new Promise<SidebarResult>((resolve) => {
        pendingRef.current = { lifecycleID, resolve };
      });
    },
    [clearAllCaptures, clearCloseTimeout, dispatchStack, nextID],
  );

  const replaceSidebar = useCallback(
    (destination: SidebarDestination): void => {
      const state = stackStateRef.current;
      if (state === null || pendingRef.current === null) {
        throw new Error("Sidebar replacement requires an active destination lifecycle.");
      }
      clearCloseTimeout();
      setExitBlockedToken(null);
      preserveRouteChangeRef.current = null;
      const currentEntry = state.entries.at(-1);
      if (currentEntry === undefined) {
        throw new Error("Sidebar replacement requires an active destination.");
      }
      clearCapture({ entryID: currentEntry.entryID, lifecycleID: state.lifecycleID });
      const nextProfile = sidebarWidthProfile(destination);
      setActiveWidthProfile(nextProfile);
      setSidebarWidths((current) =>
        sidebarWidthForProfile(current, nextProfile) === undefined
          ? [...current, { profile: nextProfile, widthPx: defaultSidebarWidth(destination) }]
          : current,
      );
      setPhase("open");
      dispatchStack({
        type: "replace",
        activationID: nextID("activation"),
        entryID: nextID("entry"),
        destination,
      });
    },
    [clearCapture, clearCloseTimeout, dispatchStack, nextID],
  );

  const captureCurrentState = useCallback((): boolean => {
    const state = stackStateRef.current;
    const pending = pendingRef.current;
    const activeEntry = state?.entries.at(-1);
    if (state === null || pending === null || activeEntry === undefined) {
      return false;
    }
    const token = { entryID: activeEntry.entryID, lifecycleID: state.lifecycleID };
    const capture = captureRef.current.get(tokenKey(token));
    if (capture === undefined) {
      return true;
    }
    const snapshot = capture();
    if (snapshot === null) {
      return false;
    }
    dispatchStack({
      type: "capture",
      lifecycleID: token.lifecycleID,
      entryID: token.entryID,
      snapshot,
    });
    clearCapture(token);
    return true;
  }, [clearCapture, dispatchStack]);

  const pushSidebar = useCallback(
    (destination: SidebarDestination): void => {
      const state = stackStateRef.current;
      const pending = pendingRef.current;
      if (state === null || pending === null || !captureCurrentState()) {
        return;
      }
      const activeEntry = state.entries.at(-1);
      if (activeEntry === undefined) {
        return;
      }
      const existingTaskIndex = findTaskDetailIndex(state.entries, destination);
      clearCloseTimeout();
      setExitBlockedToken(null);
      clearCapture({ entryID: activeEntry.entryID, lifecycleID: state.lifecycleID });
      const nextDestination =
        existingTaskIndex === undefined
          ? destination
          : (state.entries[existingTaskIndex]?.destination ?? destination);
      const activationID = nextID("activation");
      setActiveWidthProfile(sidebarWidthProfile(nextDestination));
      setPhase("open");
      void runViewTransition({
        scope: "sidebar-push",
        update: () => {
          dispatchStack({
            type: "push",
            activationID,
            lifecycleID: state.lifecycleID,
            entryID: nextID("entry"),
            sourceEntryID: activeEntry.entryID,
            destination,
          });
        },
      });
    },
    [captureCurrentState, clearCapture, clearCloseTimeout, dispatchStack, nextID],
  );

  const backSidebar = useCallback((): void => {
    const state = stackStateRef.current;
    const pending = pendingRef.current;
    const activeEntry = state?.entries.at(-1);
    if (
      state === null ||
      pending?.lifecycleID !== state.lifecycleID ||
      state.entries.length <= 1 ||
      activeEntry === undefined
    ) {
      return;
    }
    setExitBlockedToken(null);
    clearCapture({ entryID: activeEntry.entryID, lifecycleID: state.lifecycleID });
    const previousEntry = state.entries.at(-2);
    if (previousEntry === undefined) {
      return;
    }
    clearCloseTimeout();
    setActiveWidthProfile(sidebarWidthProfile(previousEntry.destination));
    const activationID = nextID("activation");
    setPhase("open");
    void runViewTransition({
      scope: "sidebar-back",
      update: () => {
        dispatchStack({
          type: "back",
          activationID,
          lifecycleID: state.lifecycleID,
          entryID: activeEntry.entryID,
        });
      },
    });
  }, [clearCapture, clearCloseTimeout, dispatchStack, nextID]);

  const resolveSidebar = useCallback(
    (result: Exclude<SidebarResult, SidebarCanceledResult>) => {
      const state = stackStateRef.current;
      const pending = pendingRef.current;
      setExitBlockedToken(null);
      pendingRef.current = null;
      preserveRouteChangeRef.current = null;
      clearAllCaptures();
      pending?.resolve(result);
      if (state !== null) {
        animateClosed(state.lifecycleID);
      }
    },
    [animateClosed, clearAllCaptures],
  );

  const resizeSidebar = useCallback(
    (width: ResolvedSidebarWidth) => {
      setSidebarWidths((current) => setSidebarWidthForProfile(current, activeWidthProfile, width.px));
    },
    [activeWidthProfile],
  );

  const registerSidebarStateCapture = useCallback(
    (token: SidebarEntryToken, capture: SidebarStateCapture): (() => void) => {
      if (!isCurrentToken(token)) {
        return () => {
          return;
        };
      }
      const key = tokenKey(token);
      captureRef.current.set(key, capture);
      return () => {
        if (captureRef.current.get(key) === capture) {
          captureRef.current.delete(key);
        }
      };
    },
    [isCurrentToken],
  );

  const removeSidebarEntry = useCallback(
    (token: SidebarEntryToken): void => {
      const state = stackStateRef.current;
      if (state === null || !isLiveEntryToken(token)) {
        clearSidebarRouteChangePreservation(token);
        return;
      }
      clearCapture(token);
      if (state.entries.length === 1) {
        closeSidebar("closed");
        return;
      }
      dispatchStack({
        type: "remove",
        ...(state.entries.at(-1)?.entryID === token.entryID ? { activationID: nextID("activation") } : {}),
        lifecycleID: token.lifecycleID,
        entryID: token.entryID,
      });
    },
    [
      clearCapture,
      clearSidebarRouteChangePreservation,
      closeSidebar,
      dispatchStack,
      isLiveEntryToken,
      nextID,
    ],
  );

  const closeSidebarIfCurrent = useCallback(
    (token: SidebarEntryToken, reason: SidebarCancelReason = "closed"): void => {
      if (isCurrentToken(token)) {
        closeSidebar(reason);
      }
    },
    [closeSidebar, isCurrentToken],
  );

  const setSidebarExitBlocked = useCallback(
    (token: SidebarEntryToken, blocked: boolean): void => {
      if (!isCurrentToken(token)) {
        return;
      }
      setExitBlockedToken((current) => {
        if (blocked) {
          return current !== null && tokenKey(current) === tokenKey(token) ? current : token;
        }
        return current !== null && tokenKey(current) === tokenKey(token) ? null : current;
      });
    },
    [isCurrentToken],
  );

  const replaceSidebarIfCurrent = useCallback(
    (token: SidebarEntryToken, destination: SidebarDestination): void => {
      if (isCurrentToken(token)) {
        replaceSidebar(destination);
      }
    },
    [isCurrentToken, replaceSidebar],
  );

  const resolveSidebarIfCurrent = useCallback(
    (token: SidebarEntryToken, result: Exclude<SidebarResult, SidebarCanceledResult>): void => {
      if (isCurrentToken(token)) {
        resolveSidebar(result);
      }
    },
    [isCurrentToken, resolveSidebar],
  );

  useEffect(() => {
    return () => {
      clearCloseTimeout();
      pendingRef.current?.resolve({ status: "canceled", reason: "closed" });
      pendingRef.current = null;
      clearAllCaptures();
    };
  }, [clearAllCaptures, clearCloseTimeout]);

  const activeDestination = stackState?.entries.at(-1)?.destination ?? null;
  const activeActivationID = phase === "closing" ? null : (stackState?.activationID ?? null);
  const activeSnapshot = stackState?.entries.at(-1)?.snapshot ?? null;
  const activeToken = useMemo(
    () => activeTokenForState(stackState),
    [activeTokenForState, stackState],
  );
  const stackDestinations = useMemo(
    () => stackState?.entries.map((entry) => entry.destination) ?? [],
    [stackState],
  );
  const stackEntryTokens = useMemo(
    () =>
      stackState?.entries.map((entry) => ({ entryID: entry.entryID, lifecycleID: stackState.lifecycleID })) ??
      [],
    [stackState],
  );
  const sidebarWidthPx =
    sidebarWidthForProfile(sidebarWidths, activeWidthProfile) ?? defaultSidebarWidth(activeDestination);

  const value = useMemo<SidebarController>(
    () => ({
      activeDestination,
      activeActivationID,
      activeSnapshot,
      activeToken,
      backSidebar,
      canGoBack: (stackState?.entries.length ?? 0) > 1,
      sidebarExitBlocked:
        activeToken !== null &&
        exitBlockedToken !== null &&
        tokenKey(activeToken) === tokenKey(exitBlockedToken),
      setSidebarExitBlocked,
      closeSidebar,
      closeSidebarIfCurrent,
      openSidebar,
      preserveSidebarOnNextRouteChange,
      clearSidebarRouteChangePreservation,
      consumeSidebarRouteChangePreservation,
      pushSidebar,
      registerSidebarStateCapture,
      removeSidebarEntry,
      replaceSidebar,
      replaceSidebarIfCurrent,
      phase,
      resizeSidebar,
      resolveSidebar,
      resolveSidebarIfCurrent,
      sidebarWidthPx,
      stackDestinations,
      stackEntryTokens,
    }),
    [
      activeDestination,
      activeActivationID,
      activeSnapshot,
      activeToken,
      backSidebar,
      exitBlockedToken,
      closeSidebar,
      closeSidebarIfCurrent,
      openSidebar,
      preserveSidebarOnNextRouteChange,
      clearSidebarRouteChangePreservation,
      consumeSidebarRouteChangePreservation,
      phase,
      pushSidebar,
      registerSidebarStateCapture,
      removeSidebarEntry,
      replaceSidebar,
      replaceSidebarIfCurrent,
      resizeSidebar,
      resolveSidebar,
      resolveSidebarIfCurrent,
      setSidebarExitBlocked,
      sidebarWidthPx,
      stackDestinations,
      stackEntryTokens,
      stackState?.entries.length,
    ],
  );

  return <SidebarContext.Provider value={value}>{children}</SidebarContext.Provider>;
}

function tokenKey(token: SidebarEntryToken): string {
  return `${token.lifecycleID}:${token.entryID}`;
}

function defaultSidebarWidth(destination: SidebarDestination | null = null): number {
  const sizePreference = sidebarSizePreference(destination);
  if (typeof window === "undefined") {
    return initialSidebarWidthForViewport(0, sizePreference);
  }
  return initialSidebarWidthForViewport(window.innerWidth, sizePreference);
}

function sidebarWidthForProfile(widths: SidebarWidths, profile: SidebarWidthProfile): number | undefined {
  return widths.find((entry) => sidebarWidthProfileEquals(entry.profile, profile))?.widthPx;
}

function setSidebarWidthForProfile(
  widths: SidebarWidths,
  profile: SidebarWidthProfile,
  widthPx: number,
): SidebarWidths {
  const resolvedWidthPx = Math.max(0, Math.round(widthPx));
  if (sidebarWidthForProfile(widths, profile) === undefined) {
    return [...widths, { profile, widthPx: resolvedWidthPx }];
  }
  return widths.map((entry) =>
    sidebarWidthProfileEquals(entry.profile, profile) ? { ...entry, widthPx: resolvedWidthPx } : entry,
  );
}
