import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AppServicesProvider } from "../../app/servicesContext";
import type { WorkflowInspectorInitialFocus, WorkflowInspectorSelection } from "../../app/sidebarContext";
import { StatusProvider } from "../../app/statusStore";
import { initializeI18n } from "../../i18n/setup";
import { createTestServices } from "../../testSupport/appServices";
import { workflowDefinition } from "../../test-support/workflow-editor/workflowEditorGraphMutationFixtures";
import {
  initializeWorkflowEditorDraft,
  workflowEditorDraftReducer,
  type WorkflowEditorDraftAction,
  type WorkflowEditorDraftState,
} from "./workflowEditorDraft";
import type { WorkflowEditorCanvasProps } from "./WorkflowEditorCanvas";
import type { WorkflowGraphCanvasProps } from "./WorkflowGraphCanvas";
import type { WorkflowGraphLayout } from "./workflowGraphLayout";

const graphCanvasHarness = vi.hoisted((): { props: WorkflowGraphCanvasProps | null } => ({ props: null }));

vi.mock("./WorkflowGraphCanvas", () => ({
  WorkflowGraphCanvas: (props: WorkflowGraphCanvasProps) => {
    graphCanvasHarness.props = props;
    const requestedEdgeID = props.graphSelectionRequest?.edgeID;
    const selectionReady =
      requestedEdgeID !== undefined && props.graph.edges.some((edge) => edge.data?.entityID === requestedEdgeID);
    return (
      <div data-selection-ready={selectionReady} data-testid="workflow-editor-canvas-boundary">
        <button
          data-testid="connected-node-keyboard-creation"
          onClick={() => {
            props.onAddConnectedNode?.("node-start", "agent", "keyboard");
          }}
          type="button"
        />
        <button
          data-testid="connected-node-pointer-creation"
          onClick={() => {
            props.onAddConnectedNode?.("node-start", "script", "pointer");
          }}
          type="button"
        />
        <button
          data-testid="consume-graph-selection"
          disabled={!selectionReady}
          onClick={() => {
            const request = props.graphSelectionRequest;
            if (request !== null && request !== undefined) {
              props.onGraphSelectionConsumed?.(request.requestID);
            }
          }}
          type="button"
        />
      </div>
    );
  },
}));

import { WorkflowEditorCanvas } from "./WorkflowEditorCanvas";

void initializeI18n();

describe("WorkflowEditorCanvas connected creation", () => {
  beforeEach(() => {
    graphCanvasHarness.props = null;
  });

  it("opens the keyboard inspector from the successful draft before layout resolves and selects only after layout", async () => {
    const initial = initializeWorkflowEditorDraft(workflowDefinition);
    const dispatched: WorkflowEditorDraftAction[] = [];
    const inspections: Inspection[] = [];
    const dispatch: WorkflowEditorCanvasProps["dispatch"] = (action) => {
      dispatched.push(action);
    };
    const inspect: WorkflowEditorCanvasProps["inspect"] = (selection, initialFocus) => {
      inspections.push({ initialFocus, selection });
    };
    const { rerender } = renderEditorCanvas({ dispatch, draftState: initial, graph: emptyGraph(), inspect });

    fireEvent.click(screen.getByTestId("connected-node-keyboard-creation"));

    const action = onlyConnectedCreation(dispatched);
    expect(action.input).toMatchObject({
      kind: "agent",
      sourceNodeID: "node-start",
    });
    expect(action.input.edgeID).toMatch(/^workflow-edge-[0-9a-f-]{36}$/u);
    expect(action.input.nodeID).toMatch(/^workflow-node-[0-9a-f-]{36}$/u);
    expect(action.input.transitionGroupID).toMatch(/^workflow-transition-group-[0-9a-f-]{36}$/u);

    const succeeded = workflowEditorDraftReducer(initial, action);
    rerender(editorCanvasTree({ dispatch, draftState: succeeded, graph: emptyGraph(), inspect }));

    await waitFor(() => {
      expect(inspections).toEqual([
        {
          initialFocus: "firstEditableControl",
          selection: { edgeID: action.input.edgeID, kind: "edge" },
        },
      ]);
    });
    expect(screen.getByTestId("workflow-editor-canvas-boundary")).toHaveAttribute("data-selection-ready", "false");
    expect(graphCanvasHarness.props?.graphSelectionRequest).toEqual({
      edgeID: action.input.edgeID,
      requestID: `connected-node:${action.input.edgeID}`,
    });

    rerender(editorCanvasTree({ dispatch, draftState: succeeded, graph: graphWithEdge(action.input.edgeID), inspect }));

    expect(screen.getByTestId("workflow-editor-canvas-boundary")).toHaveAttribute("data-selection-ready", "true");
    fireEvent.click(screen.getByTestId("consume-graph-selection"));
    await waitFor(() => {
      expect(graphCanvasHarness.props?.graphSelectionRequest).toBeNull();
    });
  });

  it("does not request pointer-origin inspector autofocus", async () => {
    const initial = initializeWorkflowEditorDraft(workflowDefinition);
    const dispatched: WorkflowEditorDraftAction[] = [];
    const inspections: Inspection[] = [];
    const dispatch: WorkflowEditorCanvasProps["dispatch"] = (action) => {
      dispatched.push(action);
    };
    const inspect: WorkflowEditorCanvasProps["inspect"] = (selection, initialFocus) => {
      inspections.push({ initialFocus, selection });
    };
    const { rerender } = renderEditorCanvas({ dispatch, draftState: initial, graph: emptyGraph(), inspect });

    fireEvent.click(screen.getByTestId("connected-node-pointer-creation"));
    const action = onlyConnectedCreation(dispatched);
    const succeeded = workflowEditorDraftReducer(initial, action);
    rerender(editorCanvasTree({ dispatch, draftState: succeeded, graph: emptyGraph(), inspect }));

    await waitFor(() => {
      expect(inspections).toEqual([
        {
          initialFocus: undefined,
          selection: { edgeID: action.input.edgeID, kind: "edge" },
        },
      ]);
    });
  });

  it("cancels selection delivery when a reset removes the expected edge before layout arrives", async () => {
    const initial = initializeWorkflowEditorDraft(workflowDefinition);
    const dispatched: WorkflowEditorDraftAction[] = [];
    const dispatch: WorkflowEditorCanvasProps["dispatch"] = (action) => {
      dispatched.push(action);
    };
    const inspect: WorkflowEditorCanvasProps["inspect"] = () => undefined;
    const { rerender } = renderEditorCanvas({ dispatch, draftState: initial, graph: emptyGraph(), inspect });

    fireEvent.click(screen.getByTestId("connected-node-keyboard-creation"));
    const action = onlyConnectedCreation(dispatched);
    const succeeded = workflowEditorDraftReducer(initial, action);
    rerender(editorCanvasTree({ dispatch, draftState: succeeded, graph: emptyGraph(), inspect }));
    await waitFor(() => {
      expect(graphCanvasHarness.props?.graphSelectionRequest?.edgeID).toBe(action.input.edgeID);
    });

    rerender(editorCanvasTree({ dispatch, draftState: initial, graph: graphWithEdge(action.input.edgeID), inspect }));

    await waitFor(() => {
      expect(graphCanvasHarness.props?.graphSelectionRequest).toBeNull();
    });
    expect(screen.getByTestId("workflow-editor-canvas-boundary")).toHaveAttribute("data-selection-ready", "false");
  });
});

function renderEditorCanvas(props: CanvasHarnessProps) {
  return render(editorCanvasTree(props));
}

type CanvasHarnessProps = Readonly<{
  dispatch: WorkflowEditorCanvasProps["dispatch"];
  draftState: WorkflowEditorDraftState;
  graph: WorkflowGraphLayout;
  inspect: WorkflowEditorCanvasProps["inspect"];
}>;

function editorCanvasTree({ dispatch, draftState, graph, inspect }: CanvasHarnessProps) {
  return (
    <AppServicesProvider services={createTestServices([])}>
      <StatusProvider>
        <WorkflowEditorCanvas
          closeDeletedNodeInspector={() => undefined}
          deleteRequestIndexRef={{ current: 0 }}
          dispatch={dispatch}
          draftState={draftState}
          graph={graph}
          inspect={inspect}
          onPendingGraphMutationChange={() => undefined}
          openDeleteConfirmation={async () => undefined}
          surface="route"
          workflowID="workflow-1"
        />
      </StatusProvider>
    </AppServicesProvider>
  );
}

type Inspection = Readonly<{
  initialFocus: WorkflowInspectorInitialFocus | undefined;
  selection: WorkflowInspectorSelection;
}>;

function onlyConnectedCreation(
  actions: readonly WorkflowEditorDraftAction[],
): Extract<WorkflowEditorDraftAction, { type: "addConnectedNode" }> {
  const action = actions[0];
  if (action?.type !== "addConnectedNode") {
    throw new Error("Expected connected-node creation dispatch.");
  }
  return action;
}

function emptyGraph(): WorkflowGraphLayout {
  return { edges: [], nodes: [] };
}

function graphWithEdge(edgeID: string): WorkflowGraphLayout {
  return {
    edges: [
      {
        data: {
          contextMode: "new_session",
          entityID: edgeID,
          entityKind: "edge",
          hasError: false,
          label: "",
          routePoints: [],
          transitionGroupID: "transition-group",
        },
        id: edgeID,
        source: "node-start",
        target: "node-created",
        type: "workflow",
      },
    ],
    nodes: [],
  };
}
