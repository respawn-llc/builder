import { createBrowserNativeBridge, type NativeBridge } from "@app/native-bridge";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { emptyWorkflowDerivedWiring, type WorkflowDefinition } from "../../api";
import { AppServicesProvider } from "../../app/servicesContext";
import { initializeI18n } from "../../i18n/setup";
import { createTestServices } from "../../testSupport/appServices";
import type { WorkflowEditorDraftController } from "./workflowEditorDraftBridgeCore";
import { initializeWorkflowEditorDraft, workflowEditorDirtyState } from "./workflowEditorDraft";
import { workflowDefinition } from "../../test-support/workflow-editor/workflowEditorGraphMutationFixtures";
import { WorkflowDraftInspectorContent } from "./WorkflowDraftInspector";

void initializeI18n();

describe("WorkflowDraftInspectorContent", () => {
  it("shows completion mode only for editable agent nodes", () => {
    const controller = workflowDraftController(withAgentCompletionMode(workflowDefinition, "tool"));

    renderInspector(controller, { kind: "node", nodeID: "node-agent" });

    expect(screen.getByLabelText("Completion mode")).toHaveTextContent("Tool call");
  });

  it("does not show completion mode for non-agent nodes", () => {
    const controller = workflowDraftController(workflowDefinition);

    renderInspector(controller, { kind: "node", nodeID: "node-done" });

    expect(screen.queryByText("Completion mode")).not.toBeInTheDocument();
  });
  it("keeps context source options openable when unavailable", async () => {
    const user = userEvent.setup();
    const controller = workflowDraftController(workflowDefinition);

    renderInspector(controller, { edgeID: "edge-start", kind: "edge" });

    await user.click(screen.getByRole("button", { name: "Context source" }));
    const fallbackOption = await screen.findByRole("menuitemradio", {
      name: "Previous run of this target, or new session",
    });

    expect(fallbackOption).toHaveAttribute("aria-disabled", "true");
    await user.hover(fallbackOption);
    expect(await screen.findByRole("tooltip")).toHaveTextContent("N/A for current configuration");
  });

  it("edits script node path without showing agent controls", () => {
    const controller = workflowDraftController(withScriptNode(workflowDefinition));

    renderInspector(controller, { kind: "node", nodeID: "node-script" });

    expect(screen.queryByText("Completion mode")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Script path"), { target: { value: "scripts/run" } });
    expect(controller.dispatch).toHaveBeenCalledWith({
      nodeID: "node-script",
      patch: { scriptPath: "scripts/run" },
      type: "editScriptNode",
    });
    expect(screen.queryByRole("button", { name: "Select script" })).not.toBeInTheDocument();
  });

  it("picks script paths through the native bridge when available", async () => {
    const bridge = nativeBridgeWithFileSelection("/tmp/worktree/scripts/run");
    const controller = workflowDraftController(withScriptNode(workflowDefinition));

    renderInspector(controller, { kind: "node", nodeID: "node-script" }, bridge);

    fireEvent.click(screen.getByRole("button", { name: "Select script" }));

    await waitFor(() => {
      expect(controller.dispatch).toHaveBeenCalledWith({
        nodeID: "node-script",
        patch: { scriptPath: "/tmp/worktree/scripts/run" },
        type: "editScriptNode",
      });
    });
  });
});

function renderInspector(
  controller: WorkflowEditorDraftController,
  selection: Parameters<typeof WorkflowDraftInspectorContent>[0]["selection"],
  nativeBridge: NativeBridge = createBrowserNativeBridge(),
): void {
  const services = createTestServices([], nativeBridge);
  render(
    <QueryClientProvider client={new QueryClient()}>
      <AppServicesProvider services={services}>
        <WorkflowDraftInspectorContent controller={controller} selection={selection} />
      </AppServicesProvider>
    </QueryClientProvider>,
  );
}

function nativeBridgeWithFileSelection(path: string): NativeBridge {
  const base = createBrowserNativeBridge();
  return {
    ...base,
    capabilities: {
      ...base.capabilities,
      files: { ...base.capabilities.files, select: true },
    },
    files: {
      ...base.files,
      async selectFile() {
        return { path };
      },
    },
  };
}

function workflowDraftController(source: WorkflowDefinition): WorkflowEditorDraftController {
  const state = initializeWorkflowEditorDraft(source);
  return {
    dispatch: vi.fn(),
    dirty: workflowEditorDirtyState(state),
    draft: state.draft,
    derivedWiring: emptyWorkflowDerivedWiring,
    draftValidation: { errors: [], valid: true },
    executionValidation: { errors: [], valid: true },
    save: vi.fn(),
    saveBlockers: [],
    saveError: "",
    saveValidation: null,
    saving: false,
    state,
    workflowID: source.workflow.id,
  };
}

function withAgentCompletionMode(source: WorkflowDefinition, completionMode: string): WorkflowDefinition {
  return {
    ...source,
    nodes: source.nodes.map((node) => (node.id === "node-agent" ? { ...node, completionMode } : node)),
  };
}

function withScriptNode(source: WorkflowDefinition): WorkflowDefinition {
  return {
    ...source,
    nodes: [
      ...source.nodes,
      {
        completionMode: "",
        groupID: "",
        groupKey: "",
        id: "node-script",
        inputFields: [],
        joinInputProviders: [],
        key: "script",
        kind: "script",
        name: "Script",
        outputFields: [],
        promptTemplate: "",
        scriptPath: null,
        subagentRole: "",
        workflowID: source.workflow.id,
      },
    ],
  };
}
