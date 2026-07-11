import { createBrowserNativeBridge, type NativeBridge } from "@app/native-bridge";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { emptyWorkflowDerivedWiring, type WorkflowDefinition } from "../../api";
import { AppServicesProvider } from "../../app/servicesContext";
import { initializeI18n } from "../../i18n/setup";
import { workflowEditorEnglish } from "../../i18n/workflowEditorEn";
import { createTestServices, type TestAppServices } from "../../testSupport/appServices";
import type { WorkflowEditorDraftController } from "./workflowEditorDraftBridgeCore";
import { initializeWorkflowEditorDraft, workflowEditorDirtyState } from "./workflowEditorDraft";
import { workflowDefinition } from "../../test-support/workflow-editor/workflowEditorGraphMutationFixtures";
import { WorkflowDraftInspectorContent } from "./WorkflowDraftInspector";

void initializeI18n();

describe("WorkflowDraftInspectorContent", () => {
  it("shows completion mode only for editable agent nodes", () => {
    const controller = workflowDraftController(withAgentCompletionMode(workflowDefinition, "tool"));

    mountInspector(controller, { kind: "node", nodeID: "node-agent" });

    expect(screen.getByLabelText("Completion mode")).toHaveTextContent("Tool call");
  });

  it("does not show completion mode for non-agent nodes", () => {
    const controller = workflowDraftController(workflowDefinition);

    mountInspector(controller, { kind: "node", nodeID: "node-done" });

    expect(screen.queryByText("Completion mode")).not.toBeInTheDocument();
  });
  it("keeps context source options openable when unavailable", async () => {
    const user = userEvent.setup();
    const controller = workflowDraftController(workflowDefinition);

    mountInspector(controller, { edgeID: "edge-start", kind: "edge" });

    await user.click(screen.getByRole("button", { name: "Context source" }));
    const fallbackOption = await screen.findByRole("menuitemradio", {
      name: "Previous run of this target, or new session",
    });

    expect(fallbackOption).toHaveAttribute("aria-disabled", "true");
    await user.hover(fallbackOption);
    expect(await screen.findByRole("tooltip")).toHaveTextContent("N/A for current configuration");
  });

  it("focuses the first transition control only when requested", () => {
    const controller = workflowDraftController(workflowDefinition);

    mountInspector(controller, { edgeID: "edge-start", kind: "edge" }, undefined, "firstEditableControl");

    expect(screen.getByLabelText(workflowEditorEnglish.transitionText)).toHaveFocus();
  });

  it("edits script node path without showing agent controls", () => {
    const controller = workflowDraftController(withScriptNode(workflowDefinition));

    mountInspector(controller, { kind: "node", nodeID: "node-script" });

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

    mountInspector(controller, { kind: "node", nodeID: "node-script" }, bridge);

    const selectScriptButton = screen.getByRole("button", { name: "Select script" });
    expect(screen.getByTestId("text-input-trailing-control")).toContainElement(selectScriptButton);

    fireEvent.click(selectScriptButton);

    await waitFor(() => {
      expect(controller.dispatch).toHaveBeenCalledWith({
        nodeID: "node-script",
        patch: { scriptPath: "/tmp/worktree/scripts/run" },
        type: "editScriptNode",
      });
    });
  });

  it("validates the current script path through the server API", async () => {
    const controller = workflowDraftController(withScriptNode(workflowDefinition, "scripts/run"));

    const appServices = mountInspector(controller, { kind: "node", nodeID: "node-script" }, undefined, [
      {
        method: "workflow.scriptPath.validate",
        result: {
          valid: false,
          errors: [
            {
              code: "workflow.validation.script_path_relative_check_skipped",
              message: "relative script_path was not checked because no task worktree root is available",
              workflow_id: "workflow-1",
              node_id: "node-script",
              blocks_context: false,
            },
          ],
        },
      },
    ]);

    expect(
      await screen.findByText(
        "relative script_path was not checked because no task worktree root is available",
      ),
    ).toBeInTheDocument();
    expect(appServices.transport.calls).toContainEqual({
      method: "workflow.scriptPath.validate",
      params: { workflow_id: "workflow-1", node_id: "node-script", script_path: "scripts/run" },
    });
  });

  it("copies a script stdout completion example from the current outgoing contract", async () => {
    const copied: string[] = [];
    const controller = workflowDraftController(withScriptNodeCompletionContract(workflowDefinition));

    mountInspector(controller, { kind: "node", nodeID: "node-script" }, nativeBridgeWithClipboard(copied));

    fireEvent.click(screen.getByRole("button", { name: "Copy stdout example" }));

    await waitFor(() => {
      expect(copied).toHaveLength(1);
    });
    expect(JSON.parse(copied[0] ?? "{}")).toEqual({
      commentary: "Completed the script step.",
      summary: "summary value",
    });
  });
});

function mountInspector(
  controller: WorkflowEditorDraftController,
  selection: Parameters<typeof WorkflowDraftInspectorContent>[0]["selection"],
  nativeBridge: NativeBridge = createBrowserNativeBridge(),
  routesOrInitialFocus:
    | Parameters<typeof createTestServices>[0]
    | Parameters<typeof WorkflowDraftInspectorContent>[0]["initialFocus"] = defaultInspectorRoutes(),
): TestAppServices {
  const routes = Array.isArray(routesOrInitialFocus) ? routesOrInitialFocus : defaultInspectorRoutes();
  const initialFocus = routesOrInitialFocus === "firstEditableControl" ? routesOrInitialFocus : undefined;
  const services = createTestServices(routes, nativeBridge);
  render(
    <QueryClientProvider client={new QueryClient()}>
      <AppServicesProvider services={services}>
        <WorkflowDraftInspectorContent controller={controller} initialFocus={initialFocus} selection={selection} />
      </AppServicesProvider>
    </QueryClientProvider>,
  );
  return services;
}

function defaultInspectorRoutes(): Parameters<typeof createTestServices>[0] {
  return [
    {
      method: "workflow.scriptPath.validate",
      result: { valid: true, errors: [] },
    },
  ];
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

function nativeBridgeWithClipboard(copied: string[]): NativeBridge {
  const base = createBrowserNativeBridge();
  return {
    ...base,
    capabilities: {
      ...base.capabilities,
      clipboard: { ...base.capabilities.clipboard, writeText: true },
    },
    clipboard: {
      ...base.clipboard,
      async writeText(value): Promise<void> {
        copied.push(value);
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

function withScriptNode(source: WorkflowDefinition, scriptPath: string | null = null): WorkflowDefinition {
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
        scriptPath,
        subagentRole: "",
        workflowID: source.workflow.id,
      },
    ],
  };
}

function withScriptNodeCompletionContract(source: WorkflowDefinition): WorkflowDefinition {
  const withScript = withScriptNode(source, "scripts/run");
  return {
    ...withScript,
    edges: [
      ...withScript.edges,
      {
        contextMode: "new_session",
        contextSource: { kind: "immediate_source", nodeKey: "" },
        id: "edge-script-done",
        inputBindings: [],
        key: "done",
        outputRequirements: [],
        parameters: [{ description: "Summary", key: "summary" }],
        promptTemplate: "",
        requiresApproval: false,
        targetNodeID: "node-done",
        transitionGroupID: "group-script-done",
        workflowID: source.workflow.id,
      },
    ],
    transitionGroups: [
      ...withScript.transitionGroups,
      {
        description: "Script completed.",
        id: "group-script-done",
        sourceNodeID: "node-script",
        transitionID: "done",
        name: "Done",
        workflowID: source.workflow.id,
      },
    ],
  };
}
