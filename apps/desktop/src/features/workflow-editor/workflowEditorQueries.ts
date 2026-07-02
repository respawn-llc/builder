import { useQuery, type UseQueryResult } from "@tanstack/react-query";

import type { WorkflowDefinition, WorkflowGraphSavePreview, WorkflowValidation } from "../../api";
import { queryKeys } from "../../app/queryKeys";
import { useAppServices } from "../../app/useAppServices";
import {
  initializeWorkflowEditorDraft,
  workflowEditorDraftGraph,
  workflowEditorDraftMetadata,
  workflowEditorDraftReducer,
  type WorkflowEditorDraftState,
} from "./workflowEditorDraft";
import { layoutWorkflowGraph, type WorkflowGraphLayout } from "./workflowGraphLayout";

export type WorkflowSaveConfirmationPreviewEntry = Readonly<{
  key: string;
  preview: WorkflowGraphSavePreview;
}>;

export function workflowSaveConfirmationPreviewKey(state: WorkflowEditorDraftState): string {
  return [state.source.workflow.id, state.source.workflow.version.toString(), state.version.toString()].join(
    ":",
  );
}

export function workflowEditorDraftStateReducer(
  state: WorkflowEditorDraftState | null,
  action: Parameters<typeof workflowEditorDraftReducer>[1],
): WorkflowEditorDraftState | null {
  if (state === null) {
    return action.type === "reset" ? initializeWorkflowEditorDraft(action.source) : state;
  }
  return workflowEditorDraftReducer(state, action);
}

export function useWorkflowDraftValidationQuery(
  workflowID: string,
  draftState: WorkflowEditorDraftState | null,
  graphDirty: boolean,
) {
  const { api } = useAppServices();
  // Metadata-only edits (display name/description) do not bump graphVersion, so
  // the server's draft validation of the workflow name would keep reusing the
  // stale staleTime:Infinity result. Vary the key by the draft metadata too.
  const metadataSignature =
    draftState === null ? "" : JSON.stringify(workflowEditorDraftMetadata(draftState));
  return useQuery({
    queryKey: queryKeys.workflowDraftValidation(
      workflowID,
      draftState?.source.workflow.version ?? 0,
      draftState?.graphVersion ?? 0,
      metadataSignature,
    ),
    queryFn: async () => {
      if (draftState === null) {
        throw new Error("Workflow draft validation requested before draft is initialized.");
      }
      return api.validateWorkflowGraphDraft({
        graph: workflowEditorDraftGraph(draftState),
        metadata: workflowEditorDraftMetadata(draftState),
        modes: ["draft", "execution"],
        workflowID,
      });
    },
    enabled: draftState !== null && !graphDirty,
    staleTime: Infinity,
  });
}

export function useWorkflowDraftDerivedWiringQuery(
  workflowID: string,
  draftState: WorkflowEditorDraftState | null,
  graphDirty: boolean,
) {
  const { api } = useAppServices();
  // Keyed on the draft graph content, not graphVersion: wiring-relevant edits
  // (node input fields, edge parameters) intentionally leave graphVersion
  // unchanged, so a version key would serve stale wiring for exactly those
  // edits. Only stringify while the query is enabled (graph dirty) to avoid the
  // cost on every clean-state render. Derive-wiring is cheap (no validation),
  // so refetching on graph content changes is acceptable.
  const graphSignature =
    draftState !== null && graphDirty ? JSON.stringify(workflowEditorDraftGraph(draftState)) : "";
  return useQuery({
    queryKey: queryKeys.workflowDraftDerivedWiring(
      workflowID,
      draftState?.source.workflow.version ?? 0,
      graphSignature,
    ),
    queryFn: async () => {
      if (draftState === null) {
        throw new Error("Workflow draft derived wiring requested before draft is initialized.");
      }
      return api.deriveWorkflowGraphWiring({
        graph: workflowEditorDraftGraph(draftState),
        workflowID,
      });
    },
    enabled: draftState !== null && graphDirty,
    staleTime: Infinity,
  });
}

export function useWorkflowScriptPathValidationQuery(workflowID: string, nodeID: string, scriptPath: string) {
  const { api } = useAppServices();
  return useQuery({
    queryKey: queryKeys.workflowScriptPathValidation(workflowID, nodeID, scriptPath),
    queryFn: async () =>
      api.validateWorkflowScriptPath({
        nodeID,
        scriptPath,
        workflowID,
      }),
    enabled: workflowID.length > 0 && nodeID.length > 0,
    refetchInterval: scriptPathValidationRefreshMs,
    staleTime: scriptPathValidationStaleMs,
  });
}

const scriptPathValidationStaleMs = 5_000;
const scriptPathValidationRefreshMs = 5_000;

export function useWorkflowGraphLayoutQuery(
  workflowID: string,
  definition: WorkflowDefinition | undefined,
  draftVersion: number,
  validation: WorkflowValidation | null,
): UseQueryResult<WorkflowGraphLayout> {
  return useQuery({
    queryKey: queryKeys.workflowGraphLayout(
      workflowID,
      (definition?.workflow.version ?? 0) * 100_000 + draftVersion,
      validation?.valid ?? false,
      validation?.errors ?? [],
    ),
    queryFn: async () => {
      if (definition === undefined || validation === null) {
        throw new Error("Workflow graph layout requested before workflow definition and validation loaded.");
      }
      return layoutWorkflowGraph(definition, validation);
    },
    enabled: definition !== undefined && validation !== null,
    placeholderData: (previous) => previous,
  });
}
