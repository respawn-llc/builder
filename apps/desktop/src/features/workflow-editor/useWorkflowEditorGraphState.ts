import { useMemo, useState } from "react";
import type { UseQueryResult } from "@tanstack/react-query";

import {
  emptyWorkflowDerivedWiring,
  type WorkflowDefinition,
  type WorkflowDerivedWiring,
  type WorkflowValidation,
} from "@/api";
import {
  workflowDefinitionFromDraft,
  type WorkflowEditorDirtyState,
  type WorkflowEditorDraftState,
} from "./workflowEditorDraft";
import { workflowGraphLayoutWithDraftProjection, type WorkflowGraphLayout } from "./workflowGraphLayout";
import {
  emptyWorkflowValidation,
  mergeWorkflowValidations,
  workflowLayoutSnapshotAfterRender,
  type WorkflowLayoutSnapshot,
} from "./workflowEditorLayoutSnapshot";
import {
  useWorkflowDraftDerivedWiringQuery,
  useWorkflowDraftValidationQuery,
  useWorkflowGraphLayoutQuery,
} from "./workflowEditorQueries";
import type { WorkflowEditorData } from "./useWorkflowEditorData";

export type WorkflowEditorGraphState = Readonly<{
  draftDefinition: WorkflowDefinition | undefined;
  draftDerivedWiring: WorkflowDerivedWiring;
  draftValidation: WorkflowValidation | null;
  executionValidation: WorkflowValidation | null;
  layoutQuery: UseQueryResult<WorkflowGraphLayout>;
  projectedGraph: WorkflowGraphLayout | undefined;
}>;

export function useWorkflowEditorGraphState(
  params: Readonly<{
    data: WorkflowEditorData;
    dirty: WorkflowEditorDirtyState;
    draftState: WorkflowEditorDraftState | null;
    workflowID: string;
  }>,
): WorkflowEditorGraphState {
  const { data, dirty, draftState, workflowID } = params;
  const [layoutSnapshot, setLayoutSnapshot] = useState<WorkflowLayoutSnapshot>({
    graphVersion: 0,
    layout: undefined,
    validation: null,
  });
  const draftDefinition = useMemo(
    () => (draftState === null ? data.workflowQuery.data : workflowDefinitionFromDraft(draftState.draft)),
    [data.workflowQuery.data, draftState],
  );
  const draftValidationQuery = useWorkflowDraftValidationQuery(workflowID, draftState, dirty.graphDirty);
  const draftDerivedWiringQuery = useWorkflowDraftDerivedWiringQuery(
    workflowID,
    draftState,
    dirty.graphDirty,
  );
  const cachedDraftValidation = draftValidationQuery.data?.draft ?? null;
  const cachedExecutionValidation = resolveCachedExecutionValidation(
    draftValidationQuery.data?.execution,
    data.validationQuery.data,
  );
  // Memoized so the merged object keeps a stable identity across renders: it
  // feeds workflowLayoutSnapshotAfterRender, which compares validation by
  // reference, and a fresh object every render would loop render-time setState.
  const cleanLayoutValidation = useMemo(
    () => mergeWorkflowValidations(cachedDraftValidation, cachedExecutionValidation),
    [cachedDraftValidation, cachedExecutionValidation],
  );
  const cleanGraphVersion = draftState?.graphVersion ?? 0;
  const topologyDirty = isTopologyDirty(dirty.graphDirty, draftState, layoutSnapshot.graphVersion);
  const cleanScoped = resolveCleanScopedValidations({
    cachedDraftValidation,
    cachedExecutionValidation,
    cleanLayoutValidation,
    graphDirty: dirty.graphDirty,
  });
  const { draftValidation, executionValidation } = cleanScoped;
  // While the graph is dirty the full draft validation (which also carries
  // derived wiring) is disabled, so a dedicated cheap derive-wiring query keeps
  // inspector wiring suggestions current; when clean, the validation result's
  // wiring is authoritative.
  const draftDerivedWiring = resolveDraftDerivedWiring({
    draftDefinition,
    derivedWiringQueryData: draftDerivedWiringQuery.data,
    graphDirty: dirty.graphDirty,
    validationQueryWiring: draftValidationQuery.data?.derivedWiring,
  });
  const layoutValidation = resolveLayoutValidation({
    cleanLayoutValidation,
    graphDirty: dirty.graphDirty,
    snapshotValidation: layoutSnapshot.validation,
    topologyDirty,
  });
  const graphValidation = resolveGraphValidation(dirty.graphDirty, cleanLayoutValidation);
  const layoutQuery = useWorkflowGraphLayoutQuery(
    workflowID,
    draftDefinition,
    cleanGraphVersion,
    layoutValidation,
  );
  const nextLayoutSnapshot = workflowLayoutSnapshotAfterRender(layoutSnapshot, {
    cleanGraphVersion,
    cleanValidation: cleanScoped.cleanValidationForSnapshot,
    layout: layoutQuery.data,
  });
  if (nextLayoutSnapshot !== layoutSnapshot) {
    setLayoutSnapshot(nextLayoutSnapshot);
  }
  const projectedGraph = useMemo(
    () =>
      resolveProjectedGraph({
        draftDefinition,
        graphDirty: dirty.graphDirty,
        graphValidation,
        layout: layoutQuery.data,
        snapshotLayout: layoutSnapshot.layout,
      }),
    [dirty.graphDirty, draftDefinition, graphValidation, layoutQuery.data, layoutSnapshot.layout],
  );

  return {
    draftDefinition,
    draftDerivedWiring,
    draftValidation,
    executionValidation,
    layoutQuery,
    projectedGraph,
  };
}

function resolveCachedExecutionValidation(
  draftExecution: WorkflowValidation | null | undefined,
  fallbackExecution: WorkflowValidation | undefined,
): WorkflowValidation | null {
  return draftExecution ?? fallbackExecution ?? null;
}

function resolveCleanScopedValidations(
  params: Readonly<{
    cachedDraftValidation: WorkflowValidation | null;
    cachedExecutionValidation: WorkflowValidation | null;
    cleanLayoutValidation: WorkflowValidation | null;
    graphDirty: boolean;
  }>,
): Readonly<{
  draftValidation: WorkflowValidation | null;
  executionValidation: WorkflowValidation | null;
  cleanValidationForSnapshot: WorkflowValidation | null;
}> {
  const { cachedDraftValidation, cachedExecutionValidation, cleanLayoutValidation, graphDirty } = params;
  if (graphDirty) {
    return {
      cleanValidationForSnapshot: null,
      draftValidation: null,
      executionValidation: emptyWorkflowValidation,
    };
  }
  return {
    cleanValidationForSnapshot: cleanLayoutValidation,
    draftValidation: cachedDraftValidation,
    executionValidation: cachedExecutionValidation,
  };
}

function resolveGraphValidation(
  graphDirty: boolean,
  cleanLayoutValidation: WorkflowValidation | null,
): WorkflowValidation {
  if (graphDirty) {
    return emptyWorkflowValidation;
  }
  return cleanLayoutValidation ?? emptyWorkflowValidation;
}

function resolveProjectedGraph(
  params: Readonly<{
    draftDefinition: WorkflowDefinition | undefined;
    graphDirty: boolean;
    graphValidation: WorkflowValidation;
    layout: WorkflowGraphLayout | undefined;
    snapshotLayout: WorkflowGraphLayout | undefined;
  }>,
): WorkflowGraphLayout | undefined {
  const { draftDefinition, graphDirty, graphValidation, layout, snapshotLayout } = params;
  const resolvedLayout = layout ?? (graphDirty ? snapshotLayout : undefined);
  if (resolvedLayout === undefined || draftDefinition === undefined) {
    return resolvedLayout;
  }
  return workflowGraphLayoutWithDraftProjection(resolvedLayout, draftDefinition, graphValidation);
}

function isTopologyDirty(
  graphDirty: boolean,
  draftState: WorkflowEditorDraftState | null,
  snapshotGraphVersion: number,
): boolean {
  return graphDirty && draftState !== null && draftState.graphVersion !== snapshotGraphVersion;
}

function resolveDraftDerivedWiring(
  params: Readonly<{
    draftDefinition: WorkflowDefinition | undefined;
    derivedWiringQueryData: WorkflowDerivedWiring | undefined;
    graphDirty: boolean;
    validationQueryWiring: WorkflowDerivedWiring | undefined;
  }>,
): WorkflowDerivedWiring {
  const { draftDefinition, derivedWiringQueryData, graphDirty, validationQueryWiring } = params;
  const active = graphDirty ? (derivedWiringQueryData ?? validationQueryWiring) : validationQueryWiring;
  return active ?? draftDefinition?.derivedWiring ?? emptyWorkflowDerivedWiring;
}

function resolveLayoutValidation(
  params: Readonly<{
    cleanLayoutValidation: WorkflowValidation | null;
    graphDirty: boolean;
    snapshotValidation: WorkflowValidation | null;
    topologyDirty: boolean;
  }>,
): WorkflowValidation | null {
  const { cleanLayoutValidation, graphDirty, snapshotValidation, topologyDirty } = params;
  if (!graphDirty) {
    return cleanLayoutValidation;
  }
  if (topologyDirty) {
    return emptyWorkflowValidation;
  }
  return snapshotValidation ?? emptyWorkflowValidation;
}
