import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import type { WorkflowInspectorInitialFocus, WorkflowInspectorSelection } from "../../app/sidebarContext";
import { useAppServices } from "../../app/useAppServices";
import { useStatusController } from "../../app/useStatusController";
import { WorkflowGraphCanvas } from "./WorkflowGraphCanvas";
import type { WorkflowGraphLayout } from "./workflowGraphLayout";
import {
  workflowDeleteNeedsConfirmation,
  workflowDeletionConfirmationCounts,
} from "./workflowDeleteConfirmationPolicy";
import {
  workflowEditorGraphMutationWarnings,
} from "./workflowEditorGraphMutations";
import {
  cascadeRowCount,
  copyWorkflowNodeText,
  deleteWarningTranslationKey,
  dispatchGraphDeletion,
  graphEditWarningTranslationKey,
  nextGraphDeleteRequestID,
  planGraphDeletion,
  planGraphExtraction,
  type PendingGraphMutation,
} from "./workflowEditorGraphMutationPlanning";
import type { WorkflowEditorDraftAction, WorkflowEditorDraftState } from "./workflowEditorDraft";
import { newWorkflowTopologyID } from "./workflowTopologyID";
import type { WorkflowGraphSelection } from "./workflowGraphSelection";
import type { WorkflowNodeKindSelectionModality } from "./WorkflowNodeKindPicker";

type PendingConnectedCreation = Readonly<{
  conflictVersion: number | null;
  edgeID: string;
  expectedGraphVersion: number;
  modality: WorkflowNodeKindSelectionModality;
}>;

type PendingGraphSelectionRequest = Readonly<{
  conflictVersion: number | null;
  edgeID: string;
  expectedGraphVersion: number;
  requestID: string;
}>;

export type WorkflowEditorCanvasProps = Readonly<{
  graph: WorkflowGraphLayout;
  surface: "route" | "sidebar";
  draftState: WorkflowEditorDraftState | null;
  dispatch: (action: WorkflowEditorDraftAction) => void;
  deleteRequestIndexRef: { current: number };
  inspect: (selection: WorkflowInspectorSelection, initialFocus?: WorkflowInspectorInitialFocus) => void;
  closeDeletedNodeInspector: (selection: WorkflowGraphSelection) => void;
  onPendingGraphMutationChange: (mutation: PendingGraphMutation | null) => void;
  openDeleteConfirmation: (mutation: PendingGraphMutation) => Promise<void>;
  workflowID: string;
}>;

export function WorkflowEditorCanvas({
  graph,
  surface,
  draftState,
  dispatch,
  deleteRequestIndexRef,
  inspect,
  closeDeletedNodeInspector,
  onPendingGraphMutationChange,
  openDeleteConfirmation,
  workflowID,
}: WorkflowEditorCanvasProps) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const { push: pushStatus } = useStatusController();
  const [pendingConnectedCreation, setPendingConnectedCreation] = useState<PendingConnectedCreation | null>(null);
  const [graphSelectionRequest, setGraphSelectionRequest] = useState<PendingGraphSelectionRequest | null>(null);
  const clearPendingConnectedCreation = (): void => {
    setGraphSelectionRequest(null);
    setPendingConnectedCreation(null);
  };

  useEffect(() => {
    if (pendingConnectedCreation === null || draftState === null) {
      return;
    }
    const conflictVersion = draftState.conflict?.workflow.version ?? null;
    if (conflictVersion !== pendingConnectedCreation.conflictVersion) {
      queueMicrotask(clearPendingConnectedCreation);
      return;
    }
    if (draftState.graphVersion < pendingConnectedCreation.expectedGraphVersion) {
      return;
    }
    if (!connectedCreationSucceeded(draftState, pendingConnectedCreation)) {
      queueMicrotask(clearPendingConnectedCreation);
      return;
    }
    inspect(
      { edgeID: pendingConnectedCreation.edgeID, kind: "edge" },
      pendingConnectedCreation.modality === "keyboard" ? "firstEditableControl" : undefined,
    );
    queueMicrotask(() => {
      setGraphSelectionRequest({
        conflictVersion: pendingConnectedCreation.conflictVersion,
        edgeID: pendingConnectedCreation.edgeID,
        expectedGraphVersion: pendingConnectedCreation.expectedGraphVersion,
        requestID: `connected-node:${pendingConnectedCreation.edgeID}`,
      });
      setPendingConnectedCreation(null);
    });
  }, [draftState, inspect, pendingConnectedCreation]);

  const validGraphSelectionRequest = graphSelectionRequestForDraft(graphSelectionRequest, draftState);

  const handleDeleteSelection = (selection: WorkflowGraphSelection): void => {
    if (draftState === null) {
      return;
    }
    const plannedDelete = planGraphDeletion(draftState.draft, selection);
    onPendingGraphMutationChange(null);
    if (plannedDelete.kind === "blocked") {
      pushStatus({
        body: t(deleteWarningTranslationKey(plannedDelete.warning)),
        id:
          plannedDelete.warning === workflowEditorGraphMutationWarnings.startNodeDelete
            ? "workflow-initial-node-delete-blocked"
            : "workflow-delete-blocked",
        title: t("workflowEditor.deleteBlockedTitle"),
        tone:
          plannedDelete.warning === workflowEditorGraphMutationWarnings.startNodeDelete
            ? "warning"
            : "danger",
      });
      return;
    }
    const counts = workflowDeletionConfirmationCounts(draftState.draft, plannedDelete.summary);
    if (workflowDeleteNeedsConfirmation(counts)) {
      const deleteRequest = {
        action: { kind: "delete", selection },
        counts,
        requestID: nextGraphDeleteRequestID(workflowID, deleteRequestIndexRef),
        summary: plannedDelete.summary,
      } satisfies PendingGraphMutation;
      onPendingGraphMutationChange(deleteRequest);
      void openDeleteConfirmation(deleteRequest);
      return;
    }
    dispatchGraphDeletion(selection, dispatch);
    closeDeletedNodeInspector(selection);
  };

  const handleExtractNodeFromGroup = (nodeID: string): void => {
    if (draftState === null) {
      return;
    }
    onPendingGraphMutationChange(null);
    const input = {
      nodeID,
      rehomedIncomingTransitionGroupID: newWorkflowTopologyID("transitionGroup"),
    };
    const plannedExtraction = planGraphExtraction(draftState.draft, input);
    if (plannedExtraction.kind === "blocked") {
      pushStatus({
        body: t(graphEditWarningTranslationKey(plannedExtraction.warning)),
        id: "workflow-extract-node-from-group-blocked",
        title: t("workflowEditor.graphEditBlockedTitle"),
        tone: "warning",
      });
      return;
    }
    if (cascadeRowCount(plannedExtraction.summary) > 0) {
      const counts = workflowDeletionConfirmationCounts(draftState.draft, plannedExtraction.summary);
      const extractionRequest = {
        action: { graphVersion: draftState.graphVersion, input, kind: "extract" },
        counts,
        requestID: nextGraphDeleteRequestID(workflowID, deleteRequestIndexRef),
        summary: plannedExtraction.summary,
      } satisfies PendingGraphMutation;
      onPendingGraphMutationChange(extractionRequest);
      void openDeleteConfirmation(extractionRequest);
      return;
    }
    dispatch({ input, type: "extractNodeFromGroup" });
  };

  return (
    <WorkflowGraphCanvas
      graph={graph}
      keyboardScope={surface === "route" ? "global" : "focused"}
      toolbarPositionStrategy={surface === "route" ? "fixed" : "absolute"}
      onAddNode={(kind) => {
        dispatch({ input: { id: newWorkflowTopologyID("node"), kind }, type: "addNode" });
      }}
      onAddConnectedNode={(sourceNodeID, kind, modality) => {
        if (draftState === null) {
          return;
        }
        const edgeID = newWorkflowTopologyID("edge");
        setGraphSelectionRequest(null);
        setPendingConnectedCreation({
          conflictVersion: draftState.conflict?.workflow.version ?? null,
          edgeID,
          expectedGraphVersion: draftState.graphVersion + 1,
          modality,
        });
        dispatch({
          input: {
            edgeID,
            kind,
            nodeID: newWorkflowTopologyID("node"),
            sourceNodeID,
            transitionGroupID: newWorkflowTopologyID("transitionGroup"),
          },
          type: "addConnectedNode",
        });
      }}
      onAddNodeToGroup={(nodeID, groupID) => {
        dispatch({
          input: {
            groupID,
            inferredTopologyIDs: {
              addedBranchJoinEdgeID: newWorkflowTopologyID("edge"),
              addedBranchJoinTransitionGroupID: newWorkflowTopologyID("transitionGroup"),
              existingBranchJoinEdgeID: newWorkflowTopologyID("edge"),
              existingBranchJoinTransitionGroupID: newWorkflowTopologyID("transitionGroup"),
              fanoutEdgeID: newWorkflowTopologyID("edge"),
            },
            nodeID,
          },
          type: "addNodeToGroup",
        });
      }}
      onConnectNodes={(sourceNodeID, targetNodeID) => {
        dispatch({
          input: {
            edgeID: newWorkflowTopologyID("edge"),
            sourceNodeID,
            targetNodeID,
            transitionGroupID: newWorkflowTopologyID("transitionGroup"),
          },
          type: "connectNodes",
        });
      }}
      onReconnectEdge={(input) => {
        dispatch({ input, type: "reconnectEdge" });
      }}
      onCreateNodeGroup={(nodeID) => {
        dispatch({
          input: {
            groupID: newWorkflowTopologyID("nodeGroup"),
            joinNodeID: newWorkflowTopologyID("node"),
            nodeID,
          },
          type: "createNodeGroupFromNode",
        });
      }}
      onCopyText={async (value) => copyWorkflowNodeText(value, nativeBridge)}
      onDeleteSelection={handleDeleteSelection}
      onEdgeInspect={(edgeID) => {
        inspect({ kind: "edge", edgeID });
      }}
      onGroupInspect={(groupID) => {
        inspect({ kind: "group", groupID });
      }}
      onExtractNodeFromGroup={handleExtractNodeFromGroup}
      onRemoveNodeFromGroup={(nodeID) => {
        dispatch({ nodeID, type: "removeNodeFromGroup" });
      }}
      onNodeInspect={(nodeID) => {
        inspect({ kind: "node", nodeID });
      }}
      onWorkflowInspect={() => {
        inspect({ kind: "workflow" });
      }}
      graphSelectionRequest={
        validGraphSelectionRequest === null
          ? null
          : {
              edgeID: validGraphSelectionRequest.edgeID,
              requestID: validGraphSelectionRequest.requestID,
            }
      }
      onGraphSelectionConsumed={(requestID) => {
        setGraphSelectionRequest((current) => (current?.requestID === requestID ? null : current));
      }}
    />
  );
}

function connectedCreationSucceeded(
  draftState: WorkflowEditorDraftState,
  creation: PendingConnectedCreation,
): boolean {
  const mutation = draftState.lastTopologyMutation;
  return (
    draftState.graphVersion === creation.expectedGraphVersion &&
    mutation !== null &&
    mutation.warnings.length === 0 &&
    mutation.nextSelection.kind === "edge" &&
    mutation.nextSelection.edgeID === creation.edgeID &&
    draftState.draft.edges.some((edge) => edge.id === creation.edgeID)
  );
}

function graphSelectionRequestForDraft(
  request: PendingGraphSelectionRequest | null,
  draftState: WorkflowEditorDraftState | null,
): PendingGraphSelectionRequest | null {
  if (
    request === null ||
    draftState === null ||
    draftState.conflict?.workflow.version !== request.conflictVersion ||
    draftState.graphVersion !== request.expectedGraphVersion ||
    !draftState.draft.edges.some((edge) => edge.id === request.edgeID)
  ) {
    return null;
  }
  return request;
}
