import { fireEvent, render, screen, within } from "@testing-library/react";
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
Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
  configurable: true,
  value: vi.fn(),
  writable: true,
});

const edge: DraftWorkflowEdge = {
  id: "edge-1",
  workflowID: "workflow-1",
  transitionGroupID: "group-1",
  key: "start",
  targetNodeID: "node-1",
  assigneeSelection: "configured",
  thinkingSelection: "configured",
  requiresApproval: false,
  contextMode: "new_session",
  contextSource: { kind: "none", nodeKey: "" },
  promptTemplate: "",
  parameters: [
    { rowID: "parameter-first", key: "first", description: "First parameter", purpose: "ordinary" },
    { rowID: "parameter-second", key: "second", description: "Second parameter", purpose: "ordinary" },
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
    const activators = screen.getAllByLabelText("Reorder parameter");
    expect(activators).toHaveLength(2);
    screen.getAllByTestId("workflow-parameter").forEach((node, index) => {
      const top = index * 40;
      Object.defineProperty(node, "getBoundingClientRect", {
        configurable: true,
        value: () => ({
          bottom: top + 40,
          height: 40,
          left: 0,
          right: 320,
          top,
          width: 320,
          x: 0,
          y: top,
          toJSON: () => ({}),
        }),
      });
    });
    const firstActivator = activators[0];
    if (firstActivator === undefined) {
      throw new Error("reorder activator is missing");
    }
    fireEvent.keyDown(firstActivator, { code: "Space", key: " " });
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 0);
    });
    fireEvent.keyDown(firstActivator, { code: "ArrowDown", key: "ArrowDown" });
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 0);
    });
    fireEvent.keyDown(firstActivator, { code: "Space", key: " " });
    expect(dispatchMock).toHaveBeenCalledWith({
      activeRowID: "parameter-first",
      edgeID: "edge-1",
      overRowID: "parameter-second",
      type: "reorderEdgeParameter",
    });

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

  it("keeps protected rows editable and reorderable without exposing delete", () => {
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
    const protectedEdge: DraftWorkflowEdge = {
      ...edge,
      assigneeSelection: "previous_node",
      parameters: [
        { rowID: "protected", key: "agent_role", description: "", purpose: "target_assignee" },
        { rowID: "ordinary", key: "ordinary", description: "ordinary", purpose: "ordinary" },
      ],
    };

    render(
      <I18nextProvider i18n={appI18n}>
        <EditableEdgeParameters controller={controller} edge={protectedEdge} />
      </I18nextProvider>,
    );

    const rows = screen.getAllByTestId("workflow-parameter");
    const protectedRow = rows[0];
    const ordinaryRow = rows[1];
    if (protectedRow === undefined || ordinaryRow === undefined) {
      throw new Error("protected parameter rows are missing");
    }
    expect(within(protectedRow).queryByRole("button", { name: "Delete parameter" })).toBeNull();
    expect(within(ordinaryRow).getByRole("button", { name: "Delete parameter" })).toBeTruthy();
    expect(within(protectedRow).getAllByRole("textbox")).toHaveLength(2);
  });

  it("hides dormant protected rows while retaining ordinary parameters", () => {
    const state = initializeWorkflowEditorDraft({
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
    });
    const controller: WorkflowEditorDraftController = {
      dispatch: vi.fn(),
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
      workflowID: state.draft.workflow.id,
    };

    render(
      <I18nextProvider i18n={appI18n}>
        <EditableEdgeParameters
          controller={controller}
          edge={{
            ...edge,
            parameters: [
              { rowID: "dormant", key: "agent_role", description: "", purpose: "target_assignee" },
              { rowID: "ordinary", key: "ordinary", description: "ordinary", purpose: "ordinary" },
            ],
          }}
        />
      </I18nextProvider>,
    );

    expect(screen.getAllByTestId("workflow-parameter")).toHaveLength(1);
    expect(screen.getAllByDisplayValue("ordinary")).toHaveLength(2);
    expect(screen.queryByDisplayValue("agent_role")).toBeNull();
  });
});
