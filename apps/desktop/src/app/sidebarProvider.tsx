import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";

import {
  SidebarContext,
  type SidebarCanceledResult,
  type SidebarCancelReason,
  type SidebarController,
  type SidebarDestination,
  type SidebarDestinationSnapshot,
  type SidebarInvalidationResult,
  type SidebarInvalidationTarget,
  type SidebarPhase,
  type SidebarResult,
  type SidebarStateCapture,
} from "@/app-facade";
import {
  sidebarDestinationMatches,
  sidebarDestinationProjectID,
  sameSidebarDestination,
  deactivateSidebarDestination,
} from "./sidebarDestinationAdapter";
import { SidebarHostContext, type SidebarHostState } from "./sidebarHostContext";
import {
  createSidebarHistory,
  type SidebarHistory,
  type SidebarHistorySnapshot,
} from "./sidebarStack";
import {
  sidebarSizePreference,
  sidebarWidthProfile,
  sidebarWidthProfileEquals,
  type SidebarWidthProfile,
} from "@/app-facade";
import { initialSidebarWidthForViewport, type ResolvedSidebarWidth } from "@/app-facade";

const sidebarExitAnimationMs = 140;
type SidebarWidthEntry = Readonly<{ profile: SidebarWidthProfile; widthPx: number }>;
type SidebarWidths = readonly SidebarWidthEntry[];
const defaultSidebarWidthProfile: SidebarWidthProfile = { kind: "custom", sizing: null };
type History = SidebarHistory<SidebarDestination, SidebarDestinationSnapshot>;
type HistorySnapshot = SidebarHistorySnapshot<SidebarDestination, SidebarDestinationSnapshot>;
type PendingSidebar = Readonly<{
  history: History;
  resolve: (result: SidebarResult) => void;
}>;
type VisibleSidebar = Readonly<{
  destination: SidebarDestination;
  snapshot: SidebarDestinationSnapshot | null;
  key: string;
}>;
type CompletedSidebarResult = Exclude<SidebarResult, SidebarCanceledResult>;

export function SidebarProvider({ children }: Readonly<{ children: ReactNode }>) {
  const [currentHistory, setCurrentHistory] = useState<History | null>(null);
  const [outgoing, setOutgoing] = useState<VisibleSidebar | null>(null);
  const [phase, setPhase] = useState<SidebarPhase>("open");
  const [activeWidthProfile, setActiveWidthProfile] =
    useState<SidebarWidthProfile>(defaultSidebarWidthProfile);
  const [sidebarWidths, setSidebarWidths] = useState<SidebarWidths>(() => [
    { profile: defaultSidebarWidthProfile, widthPx: defaultSidebarWidth() },
  ]);
  const pendingRef = useRef<PendingSidebar | null>(null);
  const currentHistoryRef = useRef<History | null>(null);
  const mutationAdmissionRef = useRef<Readonly<{ history: History; key: string }> | null>(null);
  const captureRef = useRef<SidebarStateCapture | null>(null);
  const closeTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const closingHistoryRef = useRef<History | null>(null);
  const [mutationAdmitted, setMutationAdmitted] = useState(false);

  currentHistoryRef.current = currentHistory;

  const subscribe = useCallback(
    (listener: () => void) => currentHistory?.subscribe(listener) ?? (() => {}),
    [currentHistory],
  );
  const getSnapshot = useCallback(
    () => currentHistory?.snapshot() ?? null,
    [currentHistory],
  );
  const historySnapshot = useSyncExternalStore<HistorySnapshot | null>(
    subscribe,
    getSnapshot,
    () => null,
  );

  const clearCloseTimeout = useCallback(() => {
    if (closeTimeoutRef.current !== null) {
      clearTimeout(closeTimeoutRef.current);
      closeTimeoutRef.current = null;
    }
  }, []);

  const setWidthFor = useCallback((destination: SidebarDestination | null) => {
    if (destination !== null) {
      const profile = sidebarWidthProfile(destination);
      setActiveWidthProfile(profile);
      setSidebarWidths((current) =>
        sidebarWidthForProfile(current, profile) === undefined
          ? [...current, { profile, widthPx: defaultSidebarWidth(destination) }]
          : current,
      );
    }
  }, []);

  const isCurrent = useCallback((history: History, key: string): boolean => {
    return (
      currentHistoryRef.current === history &&
      pendingRef.current?.history === history &&
      history.snapshot()?.key === key
    );
  }, []);

  const clearMutationAdmission = useCallback((history?: History) => {
    const admission = mutationAdmissionRef.current;
    if (admission === null || (history !== undefined && admission.history !== history)) return;
    mutationAdmissionRef.current = null;
    setMutationAdmitted(false);
  }, []);

  const settleAndClose = useCallback(
    (history: History, result: SidebarResult, visible: VisibleSidebar | null = null) => {
      if (pendingRef.current?.history !== history) return;
      const pending = pendingRef.current;
      const current = visible ?? readVisible(history.snapshot());
      pendingRef.current = null;
      captureRef.current = null;
      clearMutationAdmission(history);
      history.destroy();
      closingHistoryRef.current = history;
      setCurrentHistory(null);
      setOutgoing(current);
      setPhase("closing");
      pending.resolve(result);
      clearCloseTimeout();
      closeTimeoutRef.current = setTimeout(() => {
        if (closingHistoryRef.current !== history) return;
        closeTimeoutRef.current = null;
        closingHistoryRef.current = null;
        setOutgoing(null);
        setPhase("open");
      }, sidebarExitAnimationMs);
    },
    [clearCloseTimeout, clearMutationAdmission],
  );

  const closeSidebar = useCallback(
    (reason: SidebarCancelReason = "closed") => {
      const history = currentHistoryRef.current;
      if (history !== null) settleAndClose(history, { status: "canceled", reason });
    },
    [settleAndClose],
  );

  const openSidebar = useCallback(
    (destination: SidebarDestination): Promise<SidebarResult> => {
      clearCloseTimeout();
      closingHistoryRef.current = null;
      const previous = pendingRef.current;
      if (previous !== undefined && previous !== null) {
        pendingRef.current = null;
        captureRef.current = null;
        clearMutationAdmission();
        previous.history.destroy();
        previous.resolve({ status: "canceled", reason: "replaced" });
      }
      const history = createSidebarHistory<SidebarDestination, SidebarDestinationSnapshot>(
        destination,
        null,
      );
      const promise = new Promise<SidebarResult>((resolve) => {
        pendingRef.current = { history, resolve };
      });
      setOutgoing(null);
      setCurrentHistory(history);
      setPhase("open");
      setWidthFor(destination);
      return promise;
    },
    [clearCloseTimeout, clearMutationAdmission, setWidthFor],
  );

  const captureCurrent = useCallback((): SidebarDestinationSnapshot | null | false => {
    const capture = captureRef.current;
    if (capture === null) return null;
    const snapshot = capture();
    if (snapshot === null) return false;
    captureRef.current = null;
    return snapshot;
  }, []);

  const pushSidebar = useCallback(
    (destination: SidebarDestination) => {
      const history = currentHistoryRef.current;
      const snapshot = history?.snapshot() ?? null;
      if (history === null || history === undefined || snapshot === null) return;
      const retainedState = captureCurrent();
      if (retainedState === false) return;
      if (
        history.push({
          sourceKey: snapshot.key,
          destination,
          retainedState,
          deactivateDestination: deactivateSidebarDestination,
          sameDestination: (candidate) => sameSidebarDestination(candidate, destination),
        })
      ) {
        clearMutationAdmission(history);
        setPhase("open");
        setWidthFor(history.snapshot()?.destination ?? destination);
      }
    },
    [captureCurrent, clearMutationAdmission, setWidthFor],
  );

  const backSidebar = useCallback(() => {
    const history = currentHistoryRef.current;
    if (history !== null && mutationAdmissionRef.current?.history === history) return;
    const snapshot = history?.snapshot() ?? null;
    if (history === null || history === undefined || snapshot === null || !snapshot.canGoBack) return;
    const retainedState = captureCurrent();
    if (retainedState === false) return;
    if (history.back({ sourceKey: snapshot.key, retainedState })) {
      clearMutationAdmission(history);
      setPhase("open");
      setWidthFor(history.snapshot()?.destination ?? null);
    }
  }, [captureCurrent, clearMutationAdmission, setWidthFor]);

  const replaceSidebar = useCallback(
    (destination: SidebarDestination) => {
      const history = currentHistoryRef.current;
      const snapshot = history?.snapshot() ?? null;
      if (history === null || history === undefined || snapshot === null) {
        throw new Error("Sidebar replacement requires an active destination.");
      }
      captureRef.current = null;
      if (history.replace({ sourceKey: snapshot.key, destination, retainedState: null })) {
        clearMutationAdmission(history);
        setPhase("open");
        setWidthFor(destination);
      }
    },
    [clearMutationAdmission, setWidthFor],
  );

  const resolveSidebar = useCallback(
    (result: CompletedSidebarResult) => {
      const history = currentHistoryRef.current;
      if (history !== null) settleAndClose(history, result);
      else pendingRef.current?.resolve(result);
    },
    [settleAndClose],
  );

  const invalidateSidebar = useCallback(
    (target: SidebarInvalidationTarget): SidebarInvalidationResult => {
      const history = currentHistoryRef.current;
      if (history === null) return { kind: "absent" };
      const visible = readVisible(history.snapshot());
      const result = history.remove((destination) => sidebarDestinationMatches(destination, target));
      if (result.removedCount === 0) return { kind: "absent" };
      if (result.empty) {
        settleAndClose(history, { status: "canceled", reason: "closed" }, visible);
        return { kind: "closed" };
      }
      if (result.currentRemoved) {
        clearMutationAdmission(history);
        setWidthFor(history.snapshot()?.destination ?? null);
      }
      return { kind: "discarded" };
    },
    [clearMutationAdmission, setWidthFor, settleAndClose],
  );

  const resizeSidebar = useCallback(
    (width: ResolvedSidebarWidth) => {
      setSidebarWidths((current) =>
        current.map((entry) =>
          sidebarWidthProfileEquals(entry.profile, activeWidthProfile)
            ? { ...entry, widthPx: Math.max(0, Math.round(width.px)) }
            : entry,
        ),
      );
    },
    [activeWidthProfile],
  );

  const hostState = useMemo<SidebarHostState>(() => {
    const history = currentHistory;
    const key = historySnapshot?.key ?? null;
    const current = historySnapshot;
    const currentAction = (operation: () => void) => {
      if (history !== null && key !== null && isCurrent(history, key)) operation();
    };
    return {
      mutationAdmitted,
      direction: current?.direction ?? null,
      key,
      snapshot: current?.retainedState ?? null,
      actions: {
        admitMutation: () => {
          if (
            history === null ||
            key === null ||
            !isCurrent(history, key) ||
            mutationAdmissionRef.current !== null
          ) {
            return null;
          }
          const admission = { history, key };
          mutationAdmissionRef.current = admission;
          setMutationAdmitted(true);
          return () => {
            if (
              mutationAdmissionRef.current === admission &&
              isCurrent(admission.history, admission.key)
            ) {
              mutationAdmissionRef.current = null;
              setMutationAdmitted(false);
            }
          };
        },
        capture: (capture) => {
          if (history === null || key === null || !isCurrent(history, key)) return () => {};
          captureRef.current = capture;
          return () => {
            if (captureRef.current === capture) captureRef.current = null;
          };
        },
        close: (reason) => currentAction(() => closeSidebar(reason)),
        invalidate: () =>
          currentAction(() => {
            if (current !== null) invalidateSidebar({ kind: "task", taskID: taskIDOf(current.destination) });
          }),
        replace: (destination) => currentAction(() => replaceSidebar(destination)),
        resolve: (result) => currentAction(() => resolveSidebar(result)),
      },
    };
  }, [
    closeSidebar,
    currentHistory,
    historySnapshot,
    invalidateSidebar,
    isCurrent,
    mutationAdmitted,
    replaceSidebar,
    resolveSidebar,
  ]);

  const visible = currentHistory === null ? outgoing : readVisible(historySnapshot);
  const activeDestination = visible?.destination ?? null;
  const sidebarWidthPx =
    sidebarWidthForProfile(sidebarWidths, activeWidthProfile) ?? defaultSidebarWidth(activeDestination);
  const controller = useMemo<SidebarController>(
    () => ({
      activeDestination,
      backSidebar,
      canGoBack: historySnapshot?.canGoBack ?? false,
      closeSidebar,
      invalidateSidebar,
      openSidebar,
      pushSidebar,
      replaceSidebar,
      phase,
      resolveSidebar,
      resizeSidebar,
      sidebarWidthPx,
    }),
    [
      activeDestination,
      backSidebar,
      closeSidebar,
      historySnapshot?.canGoBack,
      invalidateSidebar,
      openSidebar,
      phase,
      pushSidebar,
      replaceSidebar,
      resolveSidebar,
      resizeSidebar,
      sidebarWidthPx,
    ],
  );

  useEffect(
    () => () => {
      clearCloseTimeout();
      pendingRef.current?.history.destroy();
      pendingRef.current = null;
    },
    [clearCloseTimeout],
  );

  return (
    <SidebarContext.Provider value={controller}>
      <SidebarHostContext.Provider value={hostState}>{children}</SidebarHostContext.Provider>
    </SidebarContext.Provider>
  );
}

function readVisible(snapshot: HistorySnapshot | null): VisibleSidebar | null {
  return snapshot === null
    ? null
    : { destination: snapshot.destination, snapshot: snapshot.retainedState, key: snapshot.key };
}

function taskIDOf(destination: SidebarDestination): string {
  if (destination.kind !== "taskDetail") {
    throw new Error("Only Task Detail destinations can be invalidated through the current action.");
  }
  return destination.taskID;
}

function defaultSidebarWidth(destination: SidebarDestination | null = null): number {
  const preference = sidebarSizePreference(destination);
  return initialSidebarWidthForViewport(
    typeof window === "undefined" ? 0 : window.innerWidth,
    preference,
  );
}

function sidebarWidthForProfile(
  widths: SidebarWidths,
  profile: SidebarWidthProfile,
): number | undefined {
  return widths.find((entry) => sidebarWidthProfileEquals(entry.profile, profile))?.widthPx;
}
