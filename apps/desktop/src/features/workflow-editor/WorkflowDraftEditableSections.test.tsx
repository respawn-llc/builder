import { fireEvent, render, screen } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeAll, describe, expect, it, vi } from "vitest";

import {
  defaultWorkflowExecutionTargetPolicy,
  emptyWorkflowDerivedWiring,
  type WorkflowDefinition,
} from "@/api";
import { appI18n, initializeI18n } from "@/i18n";
import { EditableEdgeParameters } from "./WorkflowDraftEditableSections";
import type { WorkflowEditorDraftController } from "./workflowEditorDraftBridgeCore";
import { initializeWorkflowEditorDraft } from "./workflowEditorDraft";
import type { DraftWorkflowEdge } from "./workflowEditorDraftTypes";

Object.defineProperty(window, "matchMedia", {
  configurable: true,
  value: vi.fn((query: string): MediaQueryList => ({
    addEventListener: vi.fn(),
    addListener: vi.fn(),
    dispatchEvent: vi.fn(),
    matches: false,
    media: query,
    onchange: null,
    removeEventListener: vi.fn(),
    removeListener: vi.fn(),
  })),
});

const edge: DraftWorkflowEdge = {
  id: "edge-1",
  workflowID: "workflow-1",
  transitionGroupID: "group-1",
  key: "start",
  targetNodeID: "node-1",
  requiresApproval: false,
  contextMode: "new_session",
  contextSource: { kind: "none", nodeKey: "" },
  promptTemplate: "",
  parameters: [
    { rowID: "parameter-first", key: "first", description: "First parameter" },
    { rowID: "parameter-second", key: "second", description: "Second parameter" },
  ],
  inputBindings: [],
  outputRequirements: [],
};

beforeAll(async () => {
  await initializeI18n();
});

describe("EditableEdgeParameters", () => {
  it("keeps editable controls and the full-row reorder activator available", async () => {
    const dispatchMock = vi.fn();
    const source: WorkflowDefinition = {
      derivedWiring: emptyWorkflowDerivedWiring,
      edges: [],
      nodeGroups: [],
      nodes: [],
      transitionGroups: [],
      workflow: {
        description: "",
        executionTargetPolicy: defaultWorkflowExecutionTargetPolicy,
        id: "11111111-1111-4111-8111-111111111111",
        name: "Workflow",
        version: 1,
      },
    };
    const state = initializeWorkflowEditorDraft(source);
    const controller: WorkflowEditorDraftController = {
      dispatch(action) {
        dispatchMock(action);
      },
      dirty: { dirty: false, graphDirty: false, metadataDirty: false },
      draft: state.draft,
      derivedWiring: emptyWorkflowDerivedWiring,
      draftValidation: null,
      executionValidation: null,
      save() {
        return;
      },
      saveBlockers: [],
      saveError: "",
      saveValidation: null,
      saving: false,
      state,
      workflowID: source.workflow.id,
    };

    render(
      <I18nextProvider i18n={appI18n}>
        <EditableEdgeParameters controller={controller} edge={edge} />
      </I18nextProvider>,
    );

    expect(screen.getAllByTestId("workflow-parameter")).toHaveLength(2);
    expect(screen.getAllByLabelText("Reorder parameter")).toHaveLength(2);

    fireEvent.change(screen.getByDisplayValue("first"), { target: { value: "updated" } });
    expect(dispatchMock).toHaveBeenCalledWith({
      edgeID: "edge-1",
      parameterRowID: "parameter-first",
      patch: { key: "updated" },
      type: "updateEdgeParameter",
    });

    const deleteButton = screen.getAllByRole("button", { name: "Delete parameter" })[0];
    if (deleteButton === undefined) {
      throw new Error("delete parameter button is missing");
    }
    fireEvent.click(deleteButton);
    expect(dispatchMock).toHaveBeenCalledWith({
      edgeID: "edge-1",
      parameterRowID: "parameter-first",
      type: "deleteEdgeParameter",
    });
  });
});
