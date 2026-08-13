import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";

import { workflowLabelMaxIDs, type ProjectLabelCatalog, type TaskLabelAssignment } from "@/api";
import { queryKeys, useAppServices } from "@/app-facade";
import { useStableCallback } from "@/ui";
import { patchExistingTaskLabelAssignment, patchExistingTaskLabelProjections } from "./taskLabelCache";

export type TaskLabelAssignmentFailure = Readonly<{
  labelID: string;
  desiredSelected: boolean;
  error: unknown;
}>;

export type TaskLabelAssignmentData = Readonly<{
  selectedLabelIDs: readonly string[];
  pendingLabelIDs: readonly string[];
  failures: readonly TaskLabelAssignmentFailure[];
  isPending: boolean;
  error: Error | null;
  retryLoad(): void;
  setSelected(labelID: string, selected: boolean): void;
  retry(labelID: string): void;
}>;

type AssignmentIntent = Readonly<{
  labelID: string;
  desiredSelected: boolean;
}>;

type LocalAssignmentState = Readonly<{
  pending: ReadonlyMap<string, boolean>;
  failures: ReadonlyMap<string, TaskLabelAssignmentFailure>;
  inFlight: AssignmentIntent | null;
}>;

type AssignmentBasis = Readonly<{
  base: readonly string[];
  available: ReadonlySet<string>;
}>;

export function useManagedTaskLabelAssignment({
  availableLabelIDs,
  projectID,
  scheduleCatalogRefresh,
  scheduleTaskAssignmentRefresh,
  taskID,
}: Readonly<{
  availableLabelIDs: readonly string[];
  projectID: string;
  scheduleCatalogRefresh(): void;
  scheduleTaskAssignmentRefresh(taskID: string): void;
  taskID: string;
}>): TaskLabelAssignmentData {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  const assignmentKey = queryKeys.taskLabels(taskID);
  const catalogKey = queryKeys.projectLabels(projectID);
  const assignment = useQuery({
    queryKey: assignmentKey,
    queryFn: async () => {
      const loaded = await api.getTaskLabels(taskID);
      assertTaskAssignment(loaded, taskID);
      return loaded;
    },
    retry: false,
  });
  const [local, setLocal] = useState<LocalAssignmentState>(emptyLocalState);
  const mounted = useRef(false);
  const launched = useRef<AssignmentIntent | null>(null);

  const isMountedOwnerLive = useStableCallback((): boolean => mounted.current);
  const canMutateTask = useStableCallback(
    (): boolean =>
      isMountedOwnerLive() && queryClient.getQueryData<TaskLabelAssignment>(assignmentKey) !== undefined,
  );
  const clearIfMounted = useStableCallback((): void => {
    if (mounted.current) {
      setLocal(clearLocalAssignment);
    }
  });
  const readAssignment = useStableCallback((): TaskLabelAssignment | undefined => {
    const current = queryClient.getQueryData<TaskLabelAssignment>(assignmentKey);
    if (current !== undefined) {
      assertTaskAssignment(current, taskID);
    }
    return current;
  });
  const readCatalog = useStableCallback((): ProjectLabelCatalog | undefined => {
    const catalog = queryClient.getQueryData<ProjectLabelCatalog>(catalogKey);
    if (catalog !== undefined && catalog.projectID !== projectID) {
      throw new Error(
        `Project label catalog for ${catalog.projectID} cannot serve Task ${taskID} in Project ${projectID}.`,
      );
    }
    return catalog;
  });

  const runIntent = useStableCallback(async (intent: AssignmentIntent): Promise<void> => {
    try {
      const response = await api.updateTaskLabels(
        taskID,
        intent.desiredSelected ? [intent.labelID] : [],
        intent.desiredSelected ? [] : [intent.labelID],
      );
      assertTaskAssignment(response, taskID);
      if (!canMutateTask()) {
        clearIfMounted();
        return;
      }
      await queryClient.cancelQueries(
        { queryKey: assignmentKey, exact: true },
        { revert: false, silent: true },
      );
      if (!canMutateTask()) {
        clearIfMounted();
        return;
      }
      const catalog = readCatalog();
      if (catalog === undefined) {
        setLocal((state) => settleAssignmentSuccess(state, intent, null, null));
        void queryClient.invalidateQueries({
          queryKey: queryKeys.projectTaskListsRoot(projectID),
          refetchType: "active",
        });
        scheduleCatalogRefresh();
        scheduleTaskAssignmentRefresh(taskID);
        return;
      }
      const available = new Set(catalog.labels.map((label) => label.id));
      const installed = response.labelIDs.filter((labelID) => available.has(labelID));
      patchExistingTaskLabelAssignment(queryClient, { taskID, labelIDs: installed });
      patchExistingTaskLabelProjections(queryClient, taskID, installed);
      setLocal((state) => settleAssignmentSuccess(state, intent, installed, available));
      void queryClient.invalidateQueries({
        queryKey: queryKeys.projectTaskListsRoot(projectID),
        refetchType: "active",
      });
      scheduleTaskAssignmentRefresh(taskID);
    } catch (error: unknown) {
      if (!canMutateTask()) {
        clearIfMounted();
        return;
      }
      const base = readAssignment()?.labelIDs ?? [];
      const catalog = readCatalog();
      setLocal((state) =>
        settleAssignmentFailure(
          state,
          intent,
          error,
          catalog === undefined
            ? null
            : {
                base,
                available: new Set(catalog.labels.map((label) => label.id)),
              },
        ),
      );
    }
  });

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    const intent = local.inFlight;
    if (intent === null || launched.current === intent) {
      return;
    }
    launched.current = intent;
    if (!canMutateTask()) {
      clearIfMounted();
      return;
    }
    void runIntent(intent);
  }, [canMutateTask, clearIfMounted, local.inFlight, runIntent]);

  useEffect(() => {
    if (!canMutateTask()) {
      clearIfMounted();
      return;
    }
    const base = readAssignment();
    const catalog = readCatalog();
    if (base === undefined || catalog === undefined) {
      return;
    }
    const available = new Set(catalog.labels.map((label) => label.id));
    patchExistingTaskLabelProjections(
      queryClient,
      taskID,
      base.labelIDs.filter((labelID) => available.has(labelID)),
    );
    setLocal((state) => prepareNext(state, base.labelIDs, available));
  }, [
    assignment.data,
    availableLabelIDs,
    clearIfMounted,
    canMutateTask,
    queryClient,
    readAssignment,
    readCatalog,
    taskID,
  ]);

  const setSelected = useStableCallback((labelID: string, selected: boolean): void => {
    if (!canMutateTask()) {
      clearIfMounted();
      return;
    }
    const base = readAssignment();
    const catalog = readCatalog();
    if (base === undefined || (catalog === undefined && !selected)) {
      return;
    }
    if (catalog === undefined) {
      setLocal((state) =>
        setDesiredLabel(state, labelID, selected, {
          base: base.labelIDs,
          available: new Set([...state.pending.keys(), ...state.failures.keys(), labelID]),
        }),
      );
      return;
    }
    if (!catalog.labels.some((label) => label.id === labelID)) {
      return;
    }
    const available = new Set(catalog.labels.map((label) => label.id));
    setLocal((state) => setDesiredLabel(state, labelID, selected, { base: base.labelIDs, available }));
  });

  const retry = useStableCallback((labelID: string): void => {
    if (!canMutateTask()) {
      clearIfMounted();
      return;
    }
    const base = readAssignment();
    const catalog = readCatalog();
    if (base === undefined || catalog === undefined) {
      return;
    }
    const available = new Set(catalog.labels.map((label) => label.id));
    setLocal((state) => retryFailedLabel(state, labelID, base.labelIDs, available));
  });

  const retryLoad = useStableCallback((): void => {
    if (!canMutateTask()) {
      clearIfMounted();
      return;
    }
    void assignment.refetch();
  });

  const selectedLabelIDs = useMemo(
    () => visibleLabelIDs(assignment.data?.labelIDs ?? [], local.pending, new Set(availableLabelIDs)),
    [assignment.data?.labelIDs, availableLabelIDs, local.pending],
  );

  return {
    selectedLabelIDs,
    pendingLabelIDs: [...local.pending.keys()].filter((labelID) => availableLabelIDs.includes(labelID)),
    failures: [...local.failures.values()].sort((left, right) => left.labelID.localeCompare(right.labelID)),
    isPending: assignment.isPending,
    error: assignment.isError ? assignment.error : null,
    retryLoad,
    setSelected,
    retry,
  };
}

function clearLocalAssignment(state: LocalAssignmentState): LocalAssignmentState {
  return state.inFlight === null && state.pending.size === 0 && state.failures.size === 0
    ? state
    : emptyLocalState();
}

function setDesiredLabel(
  state: LocalAssignmentState,
  labelID: string,
  selected: boolean,
  basis: AssignmentBasis,
): LocalAssignmentState {
  const pending = new Map(state.pending);
  if (!pending.has(labelID) && pending.size >= workflowLabelMaxIDs) {
    throw new Error(
      `Task label assignment pending intents exceeded the ${String(workflowLabelMaxIDs)}-Label Project bound.`,
    );
  }
  pending.set(labelID, selected);
  const failures = new Map(state.failures);
  failures.delete(labelID);
  return prepareNext({ ...state, pending, failures }, basis.base, basis.available);
}

function retryFailedLabel(
  state: LocalAssignmentState,
  labelID: string,
  base: readonly string[],
  available: ReadonlySet<string>,
): LocalAssignmentState {
  const failure = state.failures.get(labelID);
  if (failure === undefined || !available.has(labelID)) {
    return state;
  }
  const pending = new Map(state.pending);
  pending.set(labelID, failure.desiredSelected);
  const failures = new Map(state.failures);
  failures.delete(labelID);
  return prepareNext({ ...state, pending, failures }, base, available);
}

function settleAssignmentSuccess(
  state: LocalAssignmentState,
  intent: AssignmentIntent,
  base: readonly string[] | null,
  available: ReadonlySet<string> | null,
): LocalAssignmentState {
  if (state.inFlight !== intent) {
    return state;
  }
  const pending = new Map(state.pending);
  if (pending.get(intent.labelID) === intent.desiredSelected) {
    pending.delete(intent.labelID);
  }
  const settled = { ...state, pending, inFlight: null };
  return base === null || available === null ? settled : prepareNext(settled, base, available);
}

function settleAssignmentFailure(
  state: LocalAssignmentState,
  intent: AssignmentIntent,
  error: unknown,
  basis: AssignmentBasis | null,
): LocalAssignmentState {
  if (state.inFlight !== intent) {
    return state;
  }
  const pending = new Map(state.pending);
  const failures = new Map(state.failures);
  if (pending.get(intent.labelID) === intent.desiredSelected) {
    pending.delete(intent.labelID);
    failures.set(intent.labelID, {
      labelID: intent.labelID,
      desiredSelected: intent.desiredSelected,
      error,
    });
  }
  const settled = { ...state, pending, failures, inFlight: null };
  return basis === null ? settled : prepareNext(settled, basis.base, basis.available);
}

function prepareNext(
  state: LocalAssignmentState,
  baseLabelIDs: readonly string[],
  availableLabelIDs: ReadonlySet<string>,
): LocalAssignmentState {
  const base = new Set(baseLabelIDs);
  const pending = new Map(
    [...state.pending].filter(
      ([labelID, selected]) =>
        availableLabelIDs.has(labelID) &&
        (labelID === state.inFlight?.labelID || base.has(labelID) !== selected),
    ),
  );
  const failures = new Map([...state.failures].filter(([labelID]) => availableLabelIDs.has(labelID)));
  let inFlight = state.inFlight;
  if (inFlight === null) {
    const next = pending.entries().next();
    if (!next.done) {
      const [labelID, desiredSelected] = next.value;
      inFlight = { labelID, desiredSelected };
    }
  }
  if (
    inFlight === state.inFlight &&
    mapsEqual(pending, state.pending) &&
    mapsEqual(failures, state.failures)
  ) {
    return state;
  }
  return { ...state, pending, failures, inFlight };
}

function emptyLocalState(): LocalAssignmentState {
  return {
    pending: new Map(),
    failures: new Map(),
    inFlight: null,
  };
}

function visibleLabelIDs(
  authoritativeLabelIDs: readonly string[],
  pending: ReadonlyMap<string, boolean>,
  availableLabelIDs: ReadonlySet<string>,
): readonly string[] {
  const visible = new Set(authoritativeLabelIDs.filter((labelID) => availableLabelIDs.has(labelID)));
  for (const [labelID, selected] of pending) {
    if (!availableLabelIDs.has(labelID)) {
      continue;
    }
    if (selected) {
      visible.add(labelID);
    } else {
      visible.delete(labelID);
    }
  }
  return [...visible];
}

function mapsEqual<K, V>(left: ReadonlyMap<K, V>, right: ReadonlyMap<K, V>): boolean {
  if (left.size !== right.size) {
    return false;
  }
  for (const [key, value] of left) {
    if (right.get(key) !== value) {
      return false;
    }
  }
  return true;
}

function assertTaskAssignment(assignment: TaskLabelAssignment, taskID: string): void {
  if (assignment.taskID !== taskID) {
    throw new Error(
      `Task label assignment response for Task ${assignment.taskID} cannot update Task ${taskID}.`,
    );
  }
}
