import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { emptyWorkflowDerivedWiring, type WorkflowDefinition } from "../../api";
import { initializeI18n } from "../../i18n/setup";
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
});

function renderInspector(
  controller: WorkflowEditorDraftController,
  selection: Parameters<typeof WorkflowDraftInspectorContent>[0]["selection"],
): void {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <WorkflowDraftInspectorContent controller={controller} selection={selection} />
    </QueryClientProvider>,
  );
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
