import {
  useCallback,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
  type Dispatch,
  type ReactNode,
  type SetStateAction,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { errorMessage } from "../../api/errors";
import { useSidebar } from "../../app/sidebarContext";
import type {
  SidebarDestination,
  WorkflowInspectorInitialFocus,
  WorkflowInspectorSelection,
} from "../../app/sidebarContext";
import { useStatusController } from "../../app/useStatusController";
import { useWindowChromeTitle } from "../../app/windowChromeTitle";
import { ErrorState, LoadingState } from "../../ui";
import { cx } from "../../ui/classes";
import { useWorkflowEditorData, type WorkflowEditorData } from "./useWorkflowEditorData";
import {
  initializeWorkflowEditorDraft,
  workflowEditorDirtyState,
  type WorkflowEditorDraftState,
} from "./workflowEditorDraft";
import {
  useRegisterWorkflowEditorDraftController,
  type WorkflowEditorDraftController,
} from "./workflowEditorDraftBridgeCore";
import { graphEditWarningTranslationKey, type PendingGraphMutation } from "./workflowEditorGraphMutationPlanning";
import {
  emptyWorkflowDefinition,
  emptyWorkflowValidation,
} from "./workflowEditorLayoutSnapshot";
import { workflowEditorDraftStateReducer } from "./workflowEditorQueries";
import { workflowEditorViewState } from "./workflowEditorViewState";
import { useWorkflowEditorGraphState } from "./useWorkflowEditorGraphState";
import { useWorkflowEditorSave, type WorkflowEditorSave } from "./useWorkflowEditorSave";
import { useWorkflowGraphDeleteConfirmation } from "./useWorkflowGraphDeleteConfirmation";
import { WorkflowEditorCanvas } from "./WorkflowEditorCanvas";
import { WorkflowEditorEmbeddedInspector } from "./WorkflowEditorEmbeddedInspector";
import { WorkflowEditorLegendIsland } from "./WorkflowEditorLegendIsland";
import { WorkflowEditorStatusIsland } from "./WorkflowEditorStatusIsland";
import type { WorkflowGraphSelection } from "./workflowGraphSelection";
import type { WorkflowGraphLayout } from "./workflowGraphLayout";
import type { WorkflowEditorDraftAction } from "./workflowEditorDraft";

export type WorkflowEditorRouteProps = Readonly<{
  projectID: string;
  surface?: "route" | "sidebar" | undefined;
  workflowID: string;
}>;

type WorkflowEditorEmbeddedInspectorSelection = Readonly<{
  initialFocus?: WorkflowInspectorInitialFocus | undefined;
  selection: WorkflowInspectorSelection;
  workflowID: string;
}>;

type WorkflowEditorReadyViewProps = Readonly<{
  activeEmbeddedInspectorInitialFocus?: WorkflowInspectorInitialFocus | undefined;
  activeEmbeddedInspectorSelection: WorkflowInspectorSelection | null;
  closeDeletedNodeInspector: (selection: WorkflowGraphSelection) => void;
  controller: WorkflowEditorDraftController;
  deleteConfirmationFallback: ReactNode;
  deleteRequestIndexRef: { current: number };
  dispatch: (action: WorkflowEditorDraftAction) => void;
  draftState: WorkflowEditorDraftState | null;
  graph: WorkflowGraphLayout;
  inspect: (selection: WorkflowInspectorSelection, initialFocus?: WorkflowInspectorInitialFocus) => void;
  onClearEmbeddedInspector: () => void;
  onPendingGraphMutationChange: (mutation: PendingGraphMutation | null) => void;
  openDeleteConfirmation: (mutation: PendingGraphMutation) => Promise<void>;
  save: WorkflowEditorSave;
  surface: "route" | "sidebar";
  workflowID: string;
}>;

export function WorkflowEditorRoute({ projectID, surface = "route", workflowID }: WorkflowEditorRouteProps) {
  const { t } = useTranslation();
  const { activeDestination, closeSidebar, openSidebar } = useSidebar();
  const { push: pushStatus } = useStatusController();
  const data = useWorkflowEditorData(projectID, workflowID);
  const workflow = data.workflowQuery.data?.workflow;
  const [draftState, dispatch] = useReducer(workflowEditorDraftStateReducer, null);
  const dirty = useMemo(
    () =>
      draftState === null
        ? { dirty: false, graphDirty: false, metadataDirty: false }
        : workflowEditorDirtyState(draftState),
    [draftState],
  );
  const save = useWorkflowEditorSave({ data, dispatch, draftState, projectID, workflowID });
  const [embeddedInspectorSelection, setEmbeddedInspectorSelection] =
    useState<WorkflowEditorEmbeddedInspectorSelection | null>(null);
  const pendingGraphMutationRef = useRef<PendingGraphMutation | null>(null);
  const onPendingGraphMutationChange = useCallback((mutation: PendingGraphMutation | null) => {
    pendingGraphMutationRef.current = mutation;
  }, []);
  const deleteRequestIndexRef = useRef(0);
  const graphState = useWorkflowEditorGraphState({ data, dirty, draftState, workflowID });
  const { draftDerivedWiring, draftValidation, executionValidation, layoutQuery } = graphState;
  useWindowChromeTitle(
    workflow === undefined ? t("workflowEditor.title") : workflow.name,
    surface === "route",
  );

  const inspectWorkflowGraphItem = useWorkflowGraphInspector({
    openSidebar,
    setEmbeddedInspectorSelection,
    surface,
    workflowID,
  });

  const closeDeletedNodeInspector = useCallback(
    (selection: WorkflowGraphSelection) => {
      if (selection.kind !== "node") {
        return;
      }
      if (
        surface === "sidebar" &&
        embeddedSelectionMatchesNode(embeddedInspectorSelection, workflowID, selection.nodeID)
      ) {
        setEmbeddedInspectorSelection(null);
      }
      if (
        surface === "route" &&
        overlayDestinationMatchesNode(activeDestination, workflowID, selection.nodeID)
      ) {
        closeSidebar("closed");
      }
    },
    [activeDestination, closeSidebar, embeddedInspectorSelection, surface, workflowID],
  );

  useWorkflowEditorDraftSync({ data, dirty: dirty.dirty, dispatch, draftState });

  const fallbackDraftState = draftState ?? initializeWorkflowEditorDraft(emptyWorkflowDefinition(workflowID));
  const controller = useMemo<WorkflowEditorDraftController>(
    () => ({
      dispatch,
      dirty,
      draft: fallbackDraftState.draft,
      derivedWiring: draftDerivedWiring,
      draftValidation,
      executionValidation:
        dirty.graphDirty && draftValidation === null ? emptyWorkflowValidation : executionValidation,
      save() {
        void save.saveWorkflowDraft();
      },
      saveBlockers: save.saveBlockers,
      saveError: save.saveError,
      // Drop the captured save validation once the draft moves past the version
      // it was computed against: its errors may reference rows a later edit
      // changed or removed, and the next save attempt re-validates fresh.
      saveValidation:
        save.saveValidation !== null && save.saveValidation.version === fallbackDraftState.version
          ? save.saveValidation.results
          : null,
      saving: save.saving,
      state: fallbackDraftState,
      workflowID,
    }),
    [
      dirty,
      draftDerivedWiring,
      draftValidation,
      executionValidation,
      fallbackDraftState,
      save,
      workflowID,
    ],
  );
  useRegisterWorkflowEditorDraftController(controller);
  useEffect(() => {
    const warning = draftState?.lastTopologyMutation?.warnings[0];
    if (warning === undefined) {
      return;
    }
    pushStatus({
      body: t(graphEditWarningTranslationKey(warning)),
      id: `workflow-graph-edit-warning-${draftState?.version.toString() ?? "unknown"}`,
      title: t("workflowEditor.graphEditBlockedTitle"),
      tone: "warning",
    });
  }, [draftState?.lastTopologyMutation, draftState?.version, pushStatus, t]);

  const deleteConfirmation = useWorkflowGraphDeleteConfirmation({
    closeDeletedNodeInspector,
    dispatch,
    draftState,
    onPendingGraphMutationChange,
    pendingRef: pendingGraphMutationRef,
  });

  const viewState = workflowEditorViewState(data, layoutQuery, graphState.projectedGraph);
  const activeEmbeddedInspector = embeddedInspectorForWorkflow(
    embeddedInspectorSelection,
    surface,
    workflowID,
  );

  if (viewState.kind !== "ready") {
    return (
      <WorkflowEditorNonReadyState
        onRetryLinks={() => {
          void data.linksQuery.refetch();
        }}
        onRetryLoad={() => {
          void data.workflowQuery.refetch();
          void data.validationQuery.refetch();
          void layoutQuery.refetch();
        }}
        viewState={viewState}
      />
    );
  }

  return (
    <WorkflowEditorReadyView
      activeEmbeddedInspectorInitialFocus={activeEmbeddedInspector?.initialFocus}
      activeEmbeddedInspectorSelection={activeEmbeddedInspector?.selection ?? null}
      closeDeletedNodeInspector={closeDeletedNodeInspector}
      controller={controller}
      deleteConfirmationFallback={deleteConfirmation.fallback}
      deleteRequestIndexRef={deleteRequestIndexRef}
      dispatch={dispatch}
      draftState={draftState}
      graph={viewState.graph}
      inspect={inspectWorkflowGraphItem}
      onClearEmbeddedInspector={() => {
        setEmbeddedInspectorSelection(null);
      }}
      onPendingGraphMutationChange={onPendingGraphMutationChange}
      openDeleteConfirmation={deleteConfirmation.open}
      save={save}
      surface={surface}
      workflowID={workflowID}
    />
  );
}

function useWorkflowGraphInspector({
  openSidebar,
  setEmbeddedInspectorSelection,
  surface,
  workflowID,
}: Readonly<{
  openSidebar: ReturnType<typeof useSidebar>["openSidebar"];
  setEmbeddedInspectorSelection: Dispatch<SetStateAction<WorkflowEditorEmbeddedInspectorSelection | null>>;
  surface: "route" | "sidebar";
  workflowID: string;
}>): (selection: WorkflowInspectorSelection, initialFocus?: WorkflowInspectorInitialFocus) => void {
  return useCallback(
    (selection, initialFocus) => {
      if (surface === "sidebar") {
        setEmbeddedInspectorSelection({ initialFocus, selection, workflowID });
        return;
      }
      void openSidebar({ initialFocus, kind: "workflowInspect", mode: "overlay", selection, workflowID });
    },
    [openSidebar, setEmbeddedInspectorSelection, surface, workflowID],
  );
}

function embeddedInspectorForWorkflow(
  embedded: WorkflowEditorEmbeddedInspectorSelection | null,
  surface: "route" | "sidebar",
  workflowID: string,
): WorkflowEditorEmbeddedInspectorSelection | null {
  return surface === "sidebar" && embedded?.workflowID === workflowID ? embedded : null;
}

function WorkflowEditorReadyView(props: WorkflowEditorReadyViewProps) {
  const {
    activeEmbeddedInspectorInitialFocus,
    activeEmbeddedInspectorSelection,
    closeDeletedNodeInspector,
    controller,
    deleteConfirmationFallback,
    deleteRequestIndexRef,
    dispatch,
    draftState,
    graph,
    inspect,
    onClearEmbeddedInspector,
    onPendingGraphMutationChange,
    openDeleteConfirmation,
    save,
    surface,
    workflowID,
  } = props;
  const positionStrategy = surface === "route" ? "fixed" : "absolute";
  const editorRoute = (
    <section
      className={cx(
        "app-region-no-drag min-h-0 overflow-hidden",
        surface === "route"
          ? "fixed inset-0 z-0 h-screen w-screen"
          : "relative h-full w-full rounded-[var(--radius-l)] border border-[var(--color-outline)] bg-[var(--color-island-1)]",
      )}
      data-testid="workflow-editor-route"
    >
      <WorkflowEditorCanvas
        closeDeletedNodeInspector={closeDeletedNodeInspector}
        deleteRequestIndexRef={deleteRequestIndexRef}
        dispatch={dispatch}
        draftState={draftState}
        graph={graph}
        inspect={inspect}
        onPendingGraphMutationChange={onPendingGraphMutationChange}
        openDeleteConfirmation={openDeleteConfirmation}
        surface={surface}
        workflowID={workflowID}
      />
      <WorkflowEditorEmbeddedInspector
        initialFocus={activeEmbeddedInspectorInitialFocus}
        onClose={onClearEmbeddedInspector}
        selection={activeEmbeddedInspectorSelection}
        workflowID={workflowID}
      />
      <WorkflowEditorStatusIsland
        confirmationPreview={save.saveConfirmationPreview}
        controller={controller}
        onCancelConfirmation={() => {
          save.setSaveConfirmationPreview(null);
        }}
        onConfirmSave={() => {
          if (save.saveConfirmationPreview === null) {
            return;
          }
          void save.saveWorkflowDraft(save.saveConfirmationPreview);
        }}
        onDiscard={() => {
          dispatch({ source: controller.state.source, type: "reset" });
        }}
        positionStrategy={positionStrategy}
      />
      {deleteConfirmationFallback}
      <WorkflowEditorLegendIsland positionStrategy={positionStrategy} />
    </section>
  );

  return surface === "route" ? createPortal(editorRoute, document.body) : editorRoute;
}

function embeddedSelectionMatchesNode(
  embedded: WorkflowEditorEmbeddedInspectorSelection | null,
  workflowID: string,
  nodeID: string,
): boolean {
  return (
    embedded?.workflowID === workflowID &&
    embedded.selection.kind === "node" &&
    embedded.selection.nodeID === nodeID
  );
}

function overlayDestinationMatchesNode(
  destination: SidebarDestination | null,
  workflowID: string,
  nodeID: string,
): boolean {
  return (
    destination?.kind === "workflowInspect" &&
    destination.workflowID === workflowID &&
    destination.selection.kind === "node" &&
    destination.selection.nodeID === nodeID
  );
}

function useWorkflowEditorDraftSync(
  params: Readonly<{
    data: WorkflowEditorData;
    dirty: boolean;
    dispatch: (action: Parameters<typeof workflowEditorDraftStateReducer>[1]) => void;
    draftState: WorkflowEditorDraftState | null;
  }>,
): void {
  const { data, dirty, dispatch, draftState } = params;
  useEffect(() => {
    const source = data.workflowQuery.data;
    if (source === undefined) {
      return;
    }
    if (draftState === null) {
      dispatch({ source, type: "reset" });
      return;
    }
    if (source.workflow.version === draftState.source.workflow.version) {
      return;
    }
    if (dirty) {
      if (
        draftState.conflict?.workflow.version !== source.workflow.version &&
        draftState.acknowledgedConflictVersion !== source.workflow.version
      ) {
        dispatch({ source, type: "conflict" });
      }
      return;
    }
    dispatch({ source, type: "reset" });
  }, [data.workflowQuery.data, dirty, dispatch, draftState]);
}

function WorkflowEditorNonReadyState({
  onRetryLinks,
  onRetryLoad,
  viewState,
}: Readonly<{
  onRetryLinks: () => void;
  onRetryLoad: () => void;
  viewState: Exclude<ReturnType<typeof workflowEditorViewState>, { kind: "ready" }>;
}>) {
  const { t } = useTranslation();
  if (viewState.kind === "loading") {
    return (
      <LoadingState
        appearanceDelayMs={0}
        chromePadding
        contentWidth="full"
        title={t("workflowEditor.loadingTitle")}
      />
    );
  }
  if (viewState.kind === "linkError") {
    return (
      <ErrorState
        body={errorMessage(viewState.error)}
        chromePadding
        contentWidth="full"
        onRetry={onRetryLinks}
        retryLabel={t("app.retry")}
        title={t("workflowEditor.linkLoadFailed")}
      />
    );
  }
  if (viewState.kind === "unlinked") {
    return (
      <ErrorState
        body={t("workflowEditor.unlinkedBody")}
        chromePadding
        contentWidth="full"
        reveal={false}
        title={t("workflowEditor.unlinkedTitle")}
      />
    );
  }
  return (
    <ErrorState
      body={errorMessage(viewState.error)}
      chromePadding
      contentWidth="full"
      onRetry={onRetryLoad}
      retryLabel={t("app.retry")}
      title={t("workflowEditor.loadFailed")}
    />
  );
}
