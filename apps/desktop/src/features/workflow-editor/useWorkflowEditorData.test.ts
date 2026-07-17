import { describe, expect, it } from "vitest";

import type { WorkflowProjectEvent } from "@/api";
import {
  shouldNotifyWorkflowEditorRefresh,
  shouldRefreshWorkflowDefinition,
  shouldRefreshWorkflowEditor,
  shouldRefreshWorkflowLink,
} from "./useWorkflowEditorData";

describe("shouldRefreshWorkflowEditor", () => {
  it("refreshes workflow definition events for the open workflow only", () => {
    expect(
      shouldRefreshWorkflowEditor(
        workflowEvent({ action: "node_updated", resource: "workflow", workflowID: "workflow-1" }),
        "project-1",
        "workflow-1",
      ),
    ).toBe(true);
    expect(
      shouldRefreshWorkflowEditor(
        workflowEvent({ action: "updated", resource: "workflow", workflowID: "workflow-1" }),
        "project-1",
        "workflow-1",
      ),
    ).toBe(true);
    expect(
      shouldRefreshWorkflowEditor(
        workflowEvent({ action: "graph_saved", resource: "workflow", workflowID: "workflow-1" }),
        "project-1",
        "workflow-1",
      ),
    ).toBe(true);
    expect(
      shouldRefreshWorkflowEditor(
        workflowEvent({ action: "deleted", resource: "workflow", workflowID: "workflow-1" }),
        "",
        "workflow-1",
      ),
    ).toBe(true);
    expect(
      shouldRefreshWorkflowEditor(
        workflowEvent({ action: "node_updated", resource: "workflow", workflowID: "workflow-2" }),
        "project-1",
        "workflow-1",
      ),
    ).toBe(false);
    expect(
      shouldRefreshWorkflowEditor(
        workflowEvent({ action: "created", resource: "task", workflowID: "workflow-1" }),
        "project-1",
        "workflow-1",
      ),
    ).toBe(false);
  });

  it("refreshes active project workflow-link events that may affect the open workflow", () => {
    expect(
      shouldRefreshWorkflowEditor(
        workflowEvent({
          action: "unlinked",
          changedIDs: ["link-1"],
          projectID: "project-1",
          resource: "workflow_link",
          workflowID: "workflow-1",
        }),
        "project-1",
        "workflow-1",
      ),
    ).toBe(true);
    expect(
      shouldRefreshWorkflowEditor(
        workflowEvent({
          action: "unlinked",
          changedIDs: ["link-2"],
          projectID: "project-1",
          resource: "workflow_link",
          workflowID: "workflow-2",
        }),
        "project-1",
        "workflow-1",
      ),
    ).toBe(false);
    expect(
      shouldRefreshWorkflowEditor(
        workflowEvent({
          action: "unlinked",
          projectID: "project-2",
          resource: "workflow_link",
          workflowID: "workflow-1",
        }),
        "project-1",
        "workflow-1",
      ),
    ).toBe(false);
  });

  it("separates workflow definition events from project workflow-link events", () => {
    const definitionEvent = workflowEvent({
      action: "graph_saved",
      resource: "workflow",
      workflowID: "workflow-1",
    });
    const linkEvent = workflowEvent({
      action: "default_changed",
      changedIDs: ["link-1"],
      projectID: "project-1",
      resource: "workflow_link",
      workflowID: "workflow-1",
    });

    expect(shouldRefreshWorkflowDefinition(definitionEvent, "workflow-1")).toBe(true);
    expect(shouldRefreshWorkflowDefinition(linkEvent, "workflow-1")).toBe(false);
    expect(shouldRefreshWorkflowLink(definitionEvent, "project-1", "workflow-1")).toBe(false);
    expect(shouldRefreshWorkflowLink(linkEvent, "project-1", "workflow-1")).toBe(true);
  });

  it("does not show workflow-updated feedback for workflow deletion refreshes", () => {
    expect(
      shouldNotifyWorkflowEditorRefresh(
        workflowEvent({ action: "graph_saved", resource: "workflow", workflowID: "workflow-1" }),
        "project-1",
        "workflow-1",
      ),
    ).toBe(true);
    expect(
      shouldNotifyWorkflowEditorRefresh(
        workflowEvent({ action: "deleted", resource: "workflow", workflowID: "workflow-1" }),
        "project-1",
        "workflow-1",
      ),
    ).toBe(false);
    expect(
      shouldNotifyWorkflowEditorRefresh(
        workflowEvent({
          action: "unlinked",
          projectID: "project-1",
          resource: "workflow_link",
          workflowID: "workflow-1",
        }),
        "project-1",
        "workflow-1",
      ),
    ).toBe(false);
    expect(
      shouldNotifyWorkflowEditorRefresh(
        workflowEvent({
          action: "default_changed",
          projectID: "project-1",
          resource: "workflow_link",
          workflowID: "workflow-1",
        }),
        "project-1",
        "workflow-1",
      ),
    ).toBe(true);
  });
});

function workflowEvent(overrides: Partial<WorkflowProjectEvent>): WorkflowProjectEvent {
  return {
    action: "updated",
    changedIDs: [],
    occurredAtUnixMs: 1,
    projectID: null,
    resource: "workflow",
    workflowID: null,
    ...overrides,
  };
}
