import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { appI18n } from "@/i18n";
import { TestAppProviders, createTestServices } from "@/test-support/app-services";
import { createBrowserNativeBridge } from "@/test-support/native-bridge";
import * as ui from "@/ui";
import { workflowDefinition } from "./workflowEditorGraphMutationFixtures";
import {
  initializeWorkflowEditorDraft,
  workflowEditorDraftReducer,
  type WorkflowEditorDraftAction,
  type WorkflowEditorDraftState,
} from "./workflowEditorDraft";
import { planGraphDeletion, type PendingGraphMutation } from "./workflowEditorGraphMutationPlanning";
import { workflowDeletionConfirmationCounts } from "./workflowDeleteConfirmationPolicy";
import type { WorkflowGraphSelection } from "./workflowGraphSelection";
import { useWorkflowGraphDeleteConfirmation } from "./useWorkflowGraphDeleteConfirmation";

const state = initializeWorkflowEditorDraft(workflowDefinition);
const nodeDelete = pendingDelete({ kind: "node", nodeID: "node-agent" });
const edgeDelete = pendingDelete({ kind: "edge", edgeID: "edge-start" });

afterEach(vi.restoreAllMocks);

describe("useWorkflowGraphDeleteConfirmation", () => {
  it("owns one body-portaled confirmation across native, replacement, and workflow lifecycles", async () => {
    const base = createBrowserNativeBridge();
    const openWindow = vi.fn(async () => undefined);
    const bridge = {
      ...base,
      capabilities: { ...base.capabilities, dialogWindows: true },
      dialogs: { openWindow },
    };
    const view = renderView({ mutation: nodeDelete }, bridge);

    open();
    expect(within(screen.getByTestId("sidebar-host")).queryByRole("dialog")).toBeNull();
    expect(openWindow).not.toHaveBeenCalled();
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: appI18n.t("app.cancel") }),
    );
    expect(view.dispatch).not.toHaveBeenCalled();

    open();
    view.rerender(view.tree({ mutation: edgeDelete }));
    open();
    confirm();
    expect(view.dispatch).toHaveBeenCalledExactlyOnceWith({ edgeID: "edge-start", type: "deleteEdge" });
    expect(view.closeInspector).not.toHaveBeenCalled();
    view.rerender(view.tree({ mutation: nodeDelete }));
    open();
    confirm();
    expect(view.closeInspector).toHaveBeenCalledExactlyOnceWith({ kind: "node", nodeID: "node-agent" });

    open();
    view.rerender(view.tree({ mutation: nodeDelete, workflowID: "workflow-2" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    await act(async () => Promise.resolve());
    view.rerender(view.tree({ mutation: nodeDelete }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it.each([
    [nodeDelete, workflowEditorDraftReducer(state, { edgeID: "edge-start", type: "deleteEdge" })],
    [
      edgeDelete,
      workflowEditorDraftReducer(state, {
        edgeID: "edge-start",
        promptTemplate: "Prompt",
        type: "editEdgePrompt",
      }),
    ],
  ])("replans the current draft and rejects changed confirmation facts", (mutation, draftState) => {
    const showStatus = vi.spyOn(ui, "showStatusToast");
    const view = renderView({ mutation });
    open();
    view.rerender(view.tree({ draftState, mutation }));
    confirm();
    expect(view.dispatch).not.toHaveBeenCalled();
    expect(showStatus).toHaveBeenCalledWith(
      expect.objectContaining({ id: "workflow-delete-confirmation-stale" }),
    );
  });
});

type ViewProps = Readonly<{
  draftState?: WorkflowEditorDraftState;
  mutation?: PendingGraphMutation;
  workflowID?: string;
}>;

function renderView(props: ViewProps, bridge = createBrowserNativeBridge()) {
  const dispatch = vi.fn<(action: WorkflowEditorDraftAction) => void>();
  const closeInspector = vi.fn<(selection: WorkflowGraphSelection) => void>();
  const services = createTestServices([], bridge);
  const tree = (next: ViewProps) => (
    <TestAppProviders services={services}>
      <View {...next} closeInspector={closeInspector} dispatch={dispatch} />
    </TestAppProviders>
  );
  return { ...render(tree(props)), closeInspector, dispatch, tree };
}

function View({
  closeInspector,
  dispatch,
  draftState = state,
  mutation,
  workflowID = "workflow-1",
}: ViewProps &
  Readonly<{
    closeInspector: (selection: WorkflowGraphSelection) => void;
    dispatch: (action: WorkflowEditorDraftAction) => void;
  }>) {
  const confirmation = useWorkflowGraphDeleteConfirmation({
    closeDeletedNodeInspector: closeInspector,
    dispatch,
    draftState,
    workflowID,
  });
  return (
    <div data-testid="sidebar-host">
      <button
        data-testid="open"
        onClick={() => {
          if (mutation !== undefined) confirmation.open(mutation);
        }}
      />
      {confirmation.dialog}
    </div>
  );
}

function pendingDelete(selection: WorkflowGraphSelection): PendingGraphMutation {
  const plan = planGraphDeletion(state.draft, selection);
  if (plan.kind !== "ready") throw new Error("Expected ready deletion fixture.");
  return {
    action: { kind: "delete", selection },
    counts: workflowDeletionConfirmationCounts(state.draft, plan.summary),
    summary: plan.summary,
  };
}

function open(): void {
  fireEvent.click(screen.getByTestId("open"));
}

function confirm(): void {
  const buttons = within(screen.getByRole("dialog")).getAllByRole("button");
  const action = buttons[buttons.length - 1];
  if (action === undefined) throw new Error("Expected confirmation action.");
  fireEvent.click(action);
}
