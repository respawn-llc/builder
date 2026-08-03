import { useCallback, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { errorMessage, type WorkflowGraphSavePreview, type WorkflowGraphValidationResults } from "@/api";
import { queryKeys } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import {
  workflowEditorDirtyState,
  workflowEditorDraftGraph,
  workflowEditorDraftMetadata,
  type WorkflowEditorDraftAction,
  type WorkflowEditorDraftState,
} from "./workflowEditorDraft";
import { confirmationFromImpact } from "./workflowEditorGraphMutationPlanning";
import {
  workflowSaveConfirmationPreviewKey,
  type WorkflowSaveConfirmationPreviewEntry,
} from "./workflowEditorQueries";
import type { WorkflowEditorData } from "./useWorkflowEditorData";

export type WorkflowEditorSave = Readonly<{
  saving: boolean;
  saveError: string;
  saveBlockers: readonly string[];
  saveValidation: { version: number; results: WorkflowGraphValidationResults } | null;
  saveConfirmationPreview: WorkflowGraphSavePreview | null;
  setSaveConfirmationPreview: (preview: WorkflowGraphSavePreview | null) => void;
  saveWorkflowDraft: (confirmedPreview?: WorkflowGraphSavePreview) => Promise<void>;
}>;

export function useWorkflowEditorSave(
  params: Readonly<{
    data: WorkflowEditorData;
    dispatch: (action: WorkflowEditorDraftAction) => void;
    draftState: WorkflowEditorDraftState | null;
    projectID: string;
    workflowID: string;
  }>,
): WorkflowEditorSave {
  const { data, dispatch, draftState, projectID, workflowID } = params;
  const { t } = useTranslation();
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  const [saving, setSaving] = useState(false);
  const savingRef = useRef(false);
  const [saveError, setSaveError] = useState("");
  const [saveBlockers, setSaveBlockers] = useState<readonly string[]>([]);
  // Structured draft/execution validation captured at the last save attempt,
  // tagged with the draft version it was computed against. The background
  // validation query is disabled while the graph is dirty, so without this a
  // blocked save can only show the generic blocker summary with no per-issue
  // detail. The draft version lets the controller drop it once the draft is
  // edited, so surfaced issues never reference stale rows.
  const [saveValidation, setSaveValidation] = useState<{
    version: number;
    results: WorkflowGraphValidationResults;
  } | null>(null);
  const [saveConfirmationPreviewEntry, setSaveConfirmationPreviewEntry] =
    useState<WorkflowSaveConfirmationPreviewEntry | null>(null);

  const saveConfirmationPreviewKey =
    draftState === null ? "" : workflowSaveConfirmationPreviewKey(draftState);
  const saveConfirmationPreview =
    saveConfirmationPreviewEntry?.key === saveConfirmationPreviewKey
      ? saveConfirmationPreviewEntry.preview
      : null;

  const setSaveConfirmationPreview = useCallback(
    (preview: WorkflowGraphSavePreview | null): void => {
      setSaveConfirmationPreviewEntry(preview === null ? null : { key: saveConfirmationPreviewKey, preview });
    },
    [saveConfirmationPreviewKey],
  );

  const applySavedDefinition = useCallback(
    async (definition: WorkflowEditorDraftState["source"]): Promise<void> => {
      dispatch({ source: definition, type: "reset" });
      await Promise.all([
        data.workflowQuery.refetch(),
        data.validationQuery.refetch(),
        queryClient.invalidateQueries({ queryKey: queryKeys.allWorkflows }),
        data.projectContext
          ? queryClient.invalidateQueries({ queryKey: queryKeys.boardWorkflowRoot(projectID, workflowID) })
          : Promise.resolve(),
        data.projectContext
          ? queryClient.invalidateQueries({
              queryKey: queryKeys.boardNodeCardsWorkflowRoot(projectID, workflowID),
            })
          : Promise.resolve(),
      ]);
    },
    [
      data.projectContext,
      data.validationQuery,
      data.workflowQuery,
      dispatch,
      projectID,
      queryClient,
      workflowID,
    ],
  );

  const saveWorkflowDraft = useCallback(
    async (confirmedPreview?: WorkflowGraphSavePreview): Promise<void> => {
      if (draftState === null || savingRef.current) {
        return;
      }
      savingRef.current = true;
      setSaving(true);
      setSaveError("");
      setSaveBlockers([]);
      setSaveValidation(null);
      try {
        const outcome = await runWorkflowSave({ api, confirmedPreview, draftState, t, workflowID });
        await applySaveOutcome(outcome, {
          applySavedDefinition,
          setSaveBlockers,
          setSaveConfirmationPreview,
          setSaveValidation,
        });
      } catch (error) {
        setSaveError(errorMessage(error));
      } finally {
        savingRef.current = false;
        setSaving(false);
      }
    },
    [api, applySavedDefinition, draftState, setSaveConfirmationPreview, t, workflowID],
  );

  return {
    saving,
    saveError,
    saveBlockers,
    saveValidation,
    saveConfirmationPreview,
    setSaveConfirmationPreview,
    saveWorkflowDraft,
  };
}

type WorkflowEditorApi = ReturnType<typeof useAppServices>["api"];
type Translate = ReturnType<typeof useTranslation>["t"];

type WorkflowSaveOutcome =
  | Readonly<{
      kind: "draftInvalid";
      version: number;
      results: WorkflowGraphValidationResults;
      message: string;
    }>
  | Readonly<{ kind: "confirmationRequired"; preview: WorkflowGraphSavePreview }>
  | Readonly<{ kind: "blocked"; blockers: readonly string[] }>
  | Readonly<{
      kind: "savedBlocked";
      preview: WorkflowGraphSavePreview | null;
      blockers: readonly string[];
    }>
  | Readonly<{ kind: "saved"; definition: WorkflowEditorDraftState["source"] }>;

type RunWorkflowSaveParams = Readonly<{
  api: WorkflowEditorApi;
  confirmedPreview: WorkflowGraphSavePreview | undefined;
  draftState: WorkflowEditorDraftState;
  t: Translate;
  workflowID: string;
}>;

async function runWorkflowSave(params: RunWorkflowSaveParams): Promise<WorkflowSaveOutcome> {
  const { api, confirmedPreview, draftState, t, workflowID } = params;
  const latestDirty = workflowEditorDirtyState(draftState);
  const graph = workflowEditorDraftGraph(draftState);
  const metadata = latestDirty.metadataDirty ? workflowEditorDraftMetadata(draftState) : undefined;
  if (latestDirty.graphDirty) {
    const draftInvalid = await validateDraftBeforeSave({ api, draftState, graph, t, workflowID });
    if (draftInvalid !== null) {
      return draftInvalid;
    }
  }
  const preview =
    confirmedPreview ??
    (await api.previewWorkflowGraphSave({
      expectedVersion: draftState.source.workflow.version,
      graph,
      metadata,
      workflowID,
    }));
  const previewOutcome = previewSaveOutcome(preview, confirmedPreview);
  if (previewOutcome !== null) {
    return previewOutcome;
  }
  const saved = await api.saveWorkflowGraph({
    expectedVersion: draftState.source.workflow.version,
    graph,
    metadata,
    workflowID,
    confirmation:
      confirmedPreview === undefined ? undefined : confirmationFromImpact(confirmedPreview.impact),
  });
  if (!saved.saved || saved.definition === null) {
    return {
      kind: "savedBlocked",
      blockers: saved.blockers.map((blocker) => blocker.message),
      preview: saved.confirmationRequired ? saved : null,
    };
  }
  return { kind: "saved", definition: saved.definition };
}

async function validateDraftBeforeSave(
  params: Readonly<{
    api: WorkflowEditorApi;
    draftState: WorkflowEditorDraftState;
    graph: ReturnType<typeof workflowEditorDraftGraph>;
    t: Translate;
    workflowID: string;
  }>,
): Promise<Extract<WorkflowSaveOutcome, { kind: "draftInvalid" }> | null> {
  const { api, draftState, graph, t, workflowID } = params;
  const validation = await api.validateWorkflowGraphDraft({
    graph,
    metadata: workflowEditorDraftMetadata(draftState),
    modes: ["draft", "execution"],
    workflowID,
  });
  if (validation.draft?.valid === true) {
    return null;
  }
  return {
    kind: "draftInvalid",
    message: t("workflowEditor.draftValidationBlocksSave"),
    results: validation,
    version: draftState.version,
  };
}

function previewSaveOutcome(
  preview: WorkflowGraphSavePreview,
  confirmedPreview: WorkflowGraphSavePreview | undefined,
): Extract<WorkflowSaveOutcome, { kind: "confirmationRequired" | "blocked" }> | null {
  const actionableBlockers = preview.blockers.filter((blocker) => blocker.code !== "confirmation_required");
  if (confirmedPreview === undefined && preview.confirmationRequired && actionableBlockers.length === 0) {
    return { kind: "confirmationRequired", preview };
  }
  if (actionableBlockers.length > 0) {
    return { kind: "blocked", blockers: actionableBlockers.map((blocker) => blocker.message) };
  }
  return null;
}

async function applySaveOutcome(
  outcome: WorkflowSaveOutcome,
  apply: Readonly<{
    applySavedDefinition: (definition: WorkflowEditorDraftState["source"]) => Promise<void>;
    setSaveBlockers: (blockers: readonly string[]) => void;
    setSaveConfirmationPreview: (preview: WorkflowGraphSavePreview | null) => void;
    setSaveValidation: (value: { version: number; results: WorkflowGraphValidationResults } | null) => void;
  }>,
): Promise<void> {
  if (outcome.kind === "draftInvalid") {
    apply.setSaveValidation({ results: outcome.results, version: outcome.version });
    apply.setSaveBlockers([outcome.message]);
    return;
  }
  if (outcome.kind === "confirmationRequired") {
    apply.setSaveConfirmationPreview(outcome.preview);
    apply.setSaveBlockers([]);
    return;
  }
  if (outcome.kind === "blocked") {
    apply.setSaveConfirmationPreview(null);
    apply.setSaveBlockers(outcome.blockers);
    return;
  }
  if (outcome.kind === "savedBlocked") {
    apply.setSaveConfirmationPreview(outcome.preview);
    apply.setSaveBlockers(outcome.blockers);
    return;
  }
  apply.setSaveConfirmationPreview(null);
  await apply.applySavedDefinition(outcome.definition);
}
