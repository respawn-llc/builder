import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { beforeAll, describe, expect, it, vi } from "vitest";

import { emptyWorkflowDerivedWiring, type WorkflowSelectorApplicabilityReason } from "@/api";
import { appI18n, initializeI18n } from "@/i18n";
import { groupableWorkflowDefinition } from "./workflowEditorGraphMutationFixtures";
import { WorkflowDraftInspectorContent } from "./WorkflowDraftInspector";
import type { WorkflowEditorDraftController } from "./workflowEditorDraftBridgeCore";
import { initializeWorkflowEditorDraft } from "./workflowEditorDraft";

beforeAll(async () => {
  await initializeI18n();
});

function renderEdgeInspector(
  available: boolean,
  reason: WorkflowSelectorApplicabilityReason = available ? "eligible" : "topology",
  assigneeSelection: "configured" | "previous_node" = "configured",
  thinkingSelection: "configured" | "previous_node" = "configured",
) {
  const edgeID = "edge-source-agent";
  const state = initializeWorkflowEditorDraft({
    ...groupableWorkflowDefinition,
    edges: groupableWorkflowDefinition.edges.map((edge) =>
      edge.id === edgeID ? { ...edge, assigneeSelection, thinkingSelection } : edge,
    ),
  });
  const dispatch = vi.fn();
  const controller: WorkflowEditorDraftController = {
    dispatch,
    dirty: { dirty: false, graphDirty: false, metadataDirty: false },
    draft: state.draft,
    derivedWiring: {
      ...emptyWorkflowDerivedWiring,
      edges: [
        {
          edgeID,
          inputBindings: [],
          requiredProviderFields: [],
          requiredProvisionFields: [],
          assigneeSelectionApplicability: {
            available,
            parameterVisible: available,
            reason,
          },
          thinkingSelectionApplicability: {
            available,
            parameterVisible: available,
            reason,
          },
        },
      ],
    },
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
    workflowID: groupableWorkflowDefinition.workflow.id,
  };
  render(
    <I18nextProvider i18n={appI18n}>
      <WorkflowDraftInspectorContent
        controller={controller}
        selection={{ kind: "edge", edgeID }}
      />
    </I18nextProvider>,
  );
  return { dispatch, edgeID };
}

describe("WorkflowDraftInspector edge selectors", () => {
  it("exposes independent assignee and thinking controls on an eligible Agent transition", async () => {
    const user = userEvent.setup();
    const { dispatch, edgeID } = renderEdgeInspector(true);
    const selectionRegion = screen.getByRole("region", {
      name: appI18n.t("workflowEditor.edgeAssigneeSelection"),
    });
    const assignee = within(selectionRegion).getByRole("button", {
      name: appI18n.t("workflowEditor.edgeAssigneeSelection"),
    });
    const thinking = screen.getByRole("checkbox", {
      name: appI18n.t("workflowEditor.previousNodeThinking"),
    });
    const sectionNames = screen.getAllByRole("region").map((region) => region.getAttribute("aria-labelledby"));
    expect(sectionNames.indexOf(selectionRegion.getAttribute("aria-labelledby"))).toBeLessThan(
      sectionNames.indexOf(screen.getByRole("region", { name: appI18n.t("workflowEditor.prompt") }).getAttribute("aria-labelledby")),
    );
    await user.click(assignee);
    await user.click(
      screen.getByRole("menuitemradio", {
        name: appI18n.t("workflowEditor.previousNodeAssignee"),
      }),
    );
    await user.click(thinking);
    expect(dispatch).toHaveBeenNthCalledWith(1, {
      edgeID,
      selection: "previous_node",
      type: "setEdgeAssigneeSelection",
    });
    expect(dispatch).toHaveBeenNthCalledWith(2, {
      edgeID,
      selection: "previous_node",
      type: "setEdgeThinkingSelection",
    });
  });

  it("disables both controls when server applicability says topology", () => {
    renderEdgeInspector(false);
    const selectionRegion = screen.getByRole("region", {
      name: appI18n.t("workflowEditor.edgeAssigneeSelection"),
    });
    expect(
      within(selectionRegion).getByRole("button", {
        name: appI18n.t("workflowEditor.edgeAssigneeSelection"),
      }),
    ).toBeDisabled();
    expect(
      screen.getByRole("checkbox", { name: appI18n.t("workflowEditor.previousNodeThinking") }),
    ).toBeDisabled();
  });

  it("keeps existing previous-node selectors clearable when applicability is unavailable", async () => {
    const user = userEvent.setup();
    const { dispatch, edgeID } = renderEdgeInspector(false, "topology", "previous_node", "previous_node");
    const selectionRegion = screen.getByRole("region", {
      name: appI18n.t("workflowEditor.edgeAssigneeSelection"),
    });
    const assignee = within(selectionRegion).getByRole("button", {
      name: appI18n.t("workflowEditor.edgeAssigneeSelection"),
    });
    const thinking = screen.getByRole("checkbox", {
      name: appI18n.t("workflowEditor.previousNodeThinking"),
    });
    expect(assignee).not.toBeDisabled();
    expect(thinking).not.toBeDisabled();

    await user.click(assignee);
    await user.click(screen.getByRole("menuitemradio", { name: appI18n.t("workflowEditor.edgeAssigneeConfigured") }));
    await user.click(thinking);

    expect(dispatch).toHaveBeenNthCalledWith(1, {
      edgeID,
      selection: "configured",
      type: "setEdgeAssigneeSelection",
    });
    expect(dispatch).toHaveBeenNthCalledWith(2, {
      edgeID,
      selection: "configured",
      type: "setEdgeThinkingSelection",
    });
  });

  it("localizes configuration unavailability from the server reason code", async () => {
    const user = userEvent.setup();
    renderEdgeInspector(false, "no_callable_roles");
    const selectionRegion = screen.getByRole("region", {
      name: appI18n.t("workflowEditor.edgeAssigneeSelection"),
    });
    await user.hover(
      within(selectionRegion).getByRole("button", {
        name: appI18n.t("workflowEditor.edgeAssigneeSelection"),
      }),
    );
    const unavailableMessages = await screen.findAllByText(
      appI18n.t("workflowEditor.contextSourceUnavailable"),
    );
    expect(unavailableMessages[0]).toBeVisible();
  });
});
