import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { useRouter } from "@tanstack/react-router";
import { flushSync } from "react-dom";

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
  type SidebarTaskDeletionOutcome,
  initialSidebarWidthForViewport,
  sidebarSizePreference,
  sidebarWidthProfile,
  sidebarWidthProfileEquals,
  type ResolvedSidebarWidth,
  type SidebarWidthProfile,
} from "@/app-facade";
import {
  sidebarDestinationMatches,
  sameSidebarDestination,
  deactivateSidebarDestination,
} from "./sidebarDestinationAdapter";
import { SidebarHostContext, type SidebarHostState, type SidebarScopedActions } from "./sidebarHostContext";
import { createSidebarHistory, type SidebarHistory, type SidebarHistorySnapshot } from "./sidebarStack";
import {
  classifySidebarRouteTransition,
  sidebarRouteLocationFromMatches,
  type SidebarRouteTransition,
} from "./sidebarRouteTransition";

const sidebarExitAnimationMs = 140;
type SidebarWidthEntry = Readonly<{ profile: SidebarWidthProfile; widthPx: number }>;
type SidebarWidths = readonly SidebarWidthEntry[];
const defaultSidebarWidthProfile: SidebarWidthProfile = { kind: "custom", sizing: null };
type History = SidebarHistory<SidebarDestination, SidebarDestinationSnapshot>;
type HistorySnapshot = SidebarHistorySnapshot<SidebarDestination, SidebarDestinationSnapshot>;
type PendingSidebar = Readonly<{ history: History; resolve: (result: SidebarResult) => void }>;
type SidebarLifecycle =
  | Readonly<{ kind: "closed" }>
  | Readonly<{ kind: "open"; history: History }>
  | Readonly<{ kind: "closing"; visible: VisibleSidebar | null }>;
type VisibleSidebar = Readonly<{
  destination: SidebarDestination;
  snapshot: SidebarDestinationSnapshot | null;
  key: string;
}>;
type SidebarActivation = Readonly<{ history: History; key: string }>;
type TaskDeletionOperation = Readonly<{
  activation: SidebarActivation | null;
  taskID: string;
  state: "pending" | "deferred" | "completed";
}>;

export function SidebarProvider({ children }: Readonly<{ children: ReactNode }>) {
  const [lifecycle, setLifecycle] = useState<SidebarLifecycle>({ kind: "closed" });
  const currentHistory = lifecycle.kind === "open" ? lifecycle.history : null;
  const outgoing = lifecycle.kind === "closing" ? lifecycle.visible : null;
  const phase: SidebarPhase = lifecycle.kind === "closing" ? "closing" : "open";
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
  const router = useRouter();
  const previousLocationRef = useRef(
    sidebarRouteLocationFromMatches(router.state.location.pathname, router.state.matches),
  );
  const taskDeletionRef = useRef<TaskDeletionOperation | null>(null);
  const [mutationAdmitted, setMutationAdmitted] = useState(false);

  useLayoutEffect(() => {
    currentHistoryRef.current = currentHistory;
  }, [currentHistory]);

  const subscribe = useCallback(
    (listener: () => void) => currentHistory?.subscribe(listener) ?? (() => undefined),
    [currentHistory],
  );
  const getSnapshot = useCallback(() => currentHistory?.snapshot() ?? null, [currentHistory]);
  const historySnapshot = useSyncExternalStore(subscribe, getSnapshot, () => null);

  const clearCloseTimeout = useCallback(() => {
    if (closeTimeoutRef.current !== null) {
      clearTimeout(closeTimeoutRef.current);
      closeTimeoutRef.current = null;
    }
  }, []);

  const setWidthFor = useCallback((destination: SidebarDestination | null) => {
    if (destination === null) return;
    const profile = sidebarWidthProfile(destination);
    setActiveWidthProfile(profile);
    setSidebarWidths((current) =>
      sidebarWidthForProfile(current, profile) === undefined
        ? [...current, { profile, widthPx: defaultSidebarWidth(destination) }]
        : current,
    );
  }, []);

  const isCurrent = useCallback(
    (history: History, key: string) =>
      currentHistoryRef.current === history &&
      pendingRef.current?.history === history &&
      history.snapshot()?.key === key,
    [],
  );

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
      flushSync(() => {
        setLifecycle({ kind: "closing", visible: current });
      });
      pending.resolve(result);
      clearCloseTimeout();
      closeTimeoutRef.current = setTimeout(() => {
        if (closingHistoryRef.current !== history) return;
        closeTimeoutRef.current = null;
        closingHistoryRef.current = null;
        setLifecycle({ kind: "closed" });
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

  const reconcileRouteTransition = useCallback(
    (transition: SidebarRouteTransition) => {
      const deletionOperation = taskDeletionRef.current;
      const deletionActivationIsCurrent =
        deletionOperation !== null &&
        (deletionOperation.activation === null
          ? currentHistoryRef.current === null && pendingRef.current === null
          : isCurrent(deletionOperation.activation.history, deletionOperation.activation.key));
      if (
        transition.kind === "boardTask" &&
        transition.to === undefined &&
        deletionOperation?.taskID === transition.from
      ) {
        if (!deletionActivationIsCurrent) {
          taskDeletionRef.current = null;
        } else if (deletionOperation.state === "completed") {
          taskDeletionRef.current = null;
          return;
        } else if (deletionOperation.state === "pending") {
          taskDeletionRef.current = {
            ...deletionOperation,
            state: "deferred",
          };
          return;
        }
      }
      if (transition.kind !== "none") {
        taskDeletionRef.current = null;
        closeSidebar("route_change");
      }
    },
    [closeSidebar, isCurrent],
  );

  useLayoutEffect(
    () =>
      router.subscribe("onBeforeNavigate", ({ toLocation }) => {
        const nextLocation = sidebarRouteLocationFromMatches(
          toLocation.pathname,
          router.matchRoutes(toLocation.pathname, toLocation.search),
        );
        const transition = classifySidebarRouteTransition(previousLocationRef.current, nextLocation);
        previousLocationRef.current = nextLocation;
        reconcileRouteTransition(transition);
      }),
    [reconcileRouteTransition, router],
  );

  const recordTaskDeletion = useCallback((taskID: string) => {
    const history = currentHistoryRef.current;
    taskDeletionRef.current = {
      activation: history === null ? null : sidebarActivation(history),
      state: "pending",
      taskID,
    };
  }, []);

  const settleTaskDeletion = useCallback(
    (taskID: string, outcome: SidebarTaskDeletionOutcome) => {
      const operation = taskDeletionRef.current;
      if (operation?.taskID !== taskID) return;
      if (outcome === "failed") {
        const shouldCloseCurrentSurvivor =
          operation.state === "deferred" &&
          operation.activation !== null &&
          isCurrent(operation.activation.history, operation.activation.key);
        taskDeletionRef.current = null;
        if (shouldCloseCurrentSurvivor) {
          closeSidebar("route_change");
        }
        return;
      }
      if (operation.state === "deferred") {
        taskDeletionRef.current = null;
      } else {
        taskDeletionRef.current = {
          activation: operation.activation,
          state: "completed",
          taskID,
        };
      }
    },
    [closeSidebar, isCurrent],
  );

  const openSidebar = useCallback(
    async (destination: SidebarDestination): Promise<SidebarResult> => {
      clearCloseTimeout();
      closingHistoryRef.current = null;
      const previous = pendingRef.current;
      if (previous !== null) {
        pendingRef.current = null;
        captureRef.current = null;
        clearMutationAdmission();
        previous.history.destroy();
        previous.resolve({ status: "canceled", reason: "replaced" });
      }
      const history = createSidebarHistory<SidebarDestination, SidebarDestinationSnapshot>(destination);
      const promise = new Promise<SidebarResult>((resolve) => {
        pendingRef.current = { history, resolve };
      });
      setLifecycle({ kind: "open", history });
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
      if (history === null || snapshot === null) return;
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
        setWidthFor(history.snapshot()?.destination ?? destination);
      }
    },
    [captureCurrent, clearMutationAdmission, setWidthFor],
  );

  const backSidebar = useCallback(() => {
    const history = currentHistoryRef.current;
    const snapshot = history?.snapshot() ?? null;
    if (
      history === null ||
      snapshot === null ||
      !snapshot.canGoBack ||
      mutationAdmissionRef.current?.history === history
    ) {
      return;
    }
    const retainedState = captureCurrent();
    const nextState = retainedState === false ? null : retainedState;
    if (history.back({ sourceKey: snapshot.key, retainedState: nextState })) {
      clearMutationAdmission(history);
      setWidthFor(history.snapshot()?.destination ?? null);
    }
  }, [captureCurrent, clearMutationAdmission, setWidthFor]);

  const replaceSidebar = useCallback(
    (destination: SidebarDestination) => {
      const history = currentHistoryRef.current;
      const snapshot = history?.snapshot() ?? null;
      if (history === null || snapshot === null) {
        throw new Error("Sidebar replacement requires an active destination.");
      }
      captureRef.current = null;
      if (history.replace({ sourceKey: snapshot.key, destination, retainedState: null })) {
        clearMutationAdmission(history);
        setWidthFor(destination);
      }
    },
    [clearMutationAdmission, setWidthFor],
  );

  const resolveSidebar = useCallback(
    (result: Exclude<SidebarResult, SidebarCanceledResult>) => {
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
      const deletionOperation = taskDeletionRef.current;
      const deletionActivationIsCurrent =
        deletionOperation?.activation !== undefined &&
        deletionOperation.activation !== null &&
        isCurrent(deletionOperation.activation.history, deletionOperation.activation.key);
      const result = history.remove((destination) => sidebarDestinationMatches(destination, target));
      if (result.removedCount === 0) return { kind: "absent" };
      if (deletionOperation !== null && deletionActivationIsCurrent) {
        taskDeletionRef.current = {
          ...deletionOperation,
          activation: sidebarActivation(history),
        };
      }
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
    [clearMutationAdmission, isCurrent, setWidthFor, settleAndClose],
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

  const scopedHistory = currentHistory;
  const scopedKey = historySnapshot?.key ?? null;
  const currentHostAction = useMemo(
    () => (operation: () => void) => {
      if (scopedHistory !== null && scopedKey !== null && isCurrent(scopedHistory, scopedKey)) {
        operation();
      }
    },
    [isCurrent, scopedHistory, scopedKey],
  );
  const hostAdmitMutation = useMemo(
    () => () => {
      if (
        scopedHistory === null ||
        scopedKey === null ||
        !isCurrent(scopedHistory, scopedKey) ||
        mutationAdmissionRef.current !== null
      ) {
        return null;
      }
      const admission = { history: scopedHistory, key: scopedKey };
      mutationAdmissionRef.current = admission;
      setMutationAdmitted(true);
      return () => {
        if (mutationAdmissionRef.current === admission && isCurrent(admission.history, admission.key)) {
          mutationAdmissionRef.current = null;
          setMutationAdmitted(false);
        }
      };
    },
    [isCurrent, scopedHistory, scopedKey],
  );
  const hostCapture = useMemo(
    () => (stateCapture: SidebarStateCapture) => {
      if (scopedHistory === null || scopedKey === null || !isCurrent(scopedHistory, scopedKey)) {
        return () => {
          return;
        };
      }
      captureRef.current = stateCapture;
      return () => {
        if (captureRef.current === stateCapture) captureRef.current = null;
      };
    },
    [isCurrent, scopedHistory, scopedKey],
  );
  const hostActions = useMemo<SidebarScopedActions>(
    () => ({
      admitMutation: hostAdmitMutation,
      capture: hostCapture,
      close: (reason) => {
        currentHostAction(() => {
          closeSidebar(reason);
        });
      },
      invalidate: () => {
        currentHostAction(() => {
          if (historySnapshot !== null) {
            invalidateSidebar({ kind: "task", taskID: taskIDOf(historySnapshot.destination) });
          }
        });
      },
      replace: (destination) => {
        currentHostAction(() => {
          replaceSidebar(destination);
        });
      },
      resolve: (result) => {
        currentHostAction(() => {
          resolveSidebar(result);
        });
      },
    }),
    [
      closeSidebar,
      currentHostAction,
      historySnapshot,
      hostAdmitMutation,
      hostCapture,
      invalidateSidebar,
      replaceSidebar,
      resolveSidebar,
    ],
  );
  const hostState = useSidebarHostState({
    currentHistory,
    historySnapshot,
    hostActions,
    mutationAdmitted,
    outgoing,
  });

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
      recordTaskDeletion,
      settleTaskDeletion,
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
      recordTaskDeletion,
      settleTaskDeletion,
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

function useSidebarHostState({
  currentHistory,
  historySnapshot,
  hostActions,
  mutationAdmitted,
  outgoing,
}: Readonly<{
  currentHistory: History | null;
  historySnapshot: HistorySnapshot | null;
  hostActions: SidebarHostState["actions"];
  mutationAdmitted: boolean;
  outgoing: VisibleSidebar | null;
}>): SidebarHostState {
  const visibleOutgoing = currentHistory === null ? outgoing : null;
  const direction = sidebarHostDirection(currentHistory, historySnapshot);
  const key = sidebarHostKey(currentHistory, historySnapshot, visibleOutgoing);
  const snapshot = sidebarHostSnapshot(currentHistory, historySnapshot, visibleOutgoing);
  return useMemo(
    () => ({ actions: hostActions, direction, key, mutationAdmitted, snapshot }),
    [direction, hostActions, key, mutationAdmitted, snapshot],
  );
}

function sidebarHostDirection(
  history: History | null,
  snapshot: HistorySnapshot | null,
): "push" | "back" | null {
  return history === null ? null : (snapshot?.direction ?? null);
}

function sidebarHostKey(
  history: History | null,
  snapshot: HistorySnapshot | null,
  outgoing: VisibleSidebar | null,
): string | null {
  return history === null ? (outgoing?.key ?? null) : (snapshot?.key ?? null);
}

function sidebarHostSnapshot(
  history: History | null,
  snapshot: HistorySnapshot | null,
  outgoing: VisibleSidebar | null,
): SidebarDestinationSnapshot | null {
  return history === null ? (outgoing?.snapshot ?? null) : (snapshot?.retainedState ?? null);
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

function sidebarActivation(history: History): SidebarActivation | null {
  const key = history.snapshot()?.key;
  return key === undefined ? null : { history, key };
}

function defaultSidebarWidth(destination: SidebarDestination | null = null): number {
  const preference = sidebarSizePreference(destination);
  return initialSidebarWidthForViewport(typeof window === "undefined" ? 0 : window.innerWidth, preference);
}

function sidebarWidthForProfile(widths: SidebarWidths, profile: SidebarWidthProfile): number | undefined {
  return widths.find((entry) => sidebarWidthProfileEquals(entry.profile, profile))?.widthPx;
}
