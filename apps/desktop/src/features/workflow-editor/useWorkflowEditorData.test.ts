import { describe, expect, it } from "vitest";

import {
  shouldNotifyWorkflowEditorRefresh,
  shouldRefreshWorkflowDefinition,
  shouldRefreshWorkflowEditor,
  shouldRefreshWorkflowLink,
} from "./useWorkflowEditorData";

describe("shouldRefreshWorkflowEditor", () => {
  it("refreshes workflow definition events for the open workflow only", () => {
    expectWorkflowRefresh({ action: "node_updated", resource: "workflow", workflow_id: "workflow-1" }, true);
    expectWorkflowRefresh({ action: "updated", resource: "workflow", workflow_id: "workflow-1" }, true);
    expectWorkflowRefresh({ action: "graph_saved", resource: "workflow", workflow_id: "workflow-1" }, true);
    expectWorkflowRefresh({ action: "deleted", resource: "workflow", workflow_id: "workflow-1" }, true, "");
    expectWorkflowRefresh({ action: "node_updated", resource: "workflow", workflow_id: "workflow-2" }, false);
    expectWorkflowRefresh({ action: "created", resource: "task", workflow_id: "workflow-1" }, false);
  });

  it("refreshes active project workflow-link events that may affect the open workflow", () => {
    expectWorkflowRefresh(
      {
        action: "unlinked",
        changed_ids: ["link-1"],
        project_id: "project-1",
        resource: "workflow_link",
        workflow_id: "workflow-1",
      },
      true,
    );
    expectWorkflowRefresh(
      {
        action: "unlinked",
        changed_ids: ["link-2"],
        project_id: "project-1",
        resource: "workflow_link",
        workflow_id: "workflow-2",
      },
      false,
    );
    expectWorkflowRefresh(
      {
        action: "unlinked",
        project_id: "project-2",
        resource: "workflow_link",
        workflow_id: "workflow-1",
      },
      false,
    );
  });

  it("separates workflow definition events from project workflow-link events", () => {
    const workflowEvent = eventParams({
      action: "graph_saved",
      resource: "workflow",
      workflow_id: "workflow-1",
    });
    const linkEvent = eventParams({
      action: "default_changed",
      changed_ids: ["link-1"],
      project_id: "project-1",
      resource: "workflow_link",
      workflow_id: "workflow-1",
    });

    expect(shouldRefreshWorkflowDefinition(workflowEvent, "workflow-1")).toBe(true);
    expect(shouldRefreshWorkflowDefinition(linkEvent, "workflow-1")).toBe(false);
    expect(shouldRefreshWorkflowLink(workflowEvent, "project-1", "workflow-1")).toBe(false);
    expect(shouldRefreshWorkflowLink(linkEvent, "project-1", "workflow-1")).toBe(true);
  });

  it("does not show workflow-updated feedback for workflow deletion refreshes", () => {
    expectWorkflowRefreshNotification(
      { action: "graph_saved", resource: "workflow", workflow_id: "workflow-1" },
      true,
    );
    expectWorkflowRefreshNotification(
      { action: "deleted", resource: "workflow", workflow_id: "workflow-1" },
      false,
    );
    expectWorkflowRefreshNotification(
      {
        action: "unlinked",
        project_id: "project-1",
        resource: "workflow_link",
        workflow_id: "workflow-1",
      },
      false,
    );
    expectWorkflowRefreshNotification(
      {
        action: "default_changed",
        project_id: "project-1",
        resource: "workflow_link",
        workflow_id: "workflow-1",
      },
      true,
    );
  });
});

function expectWorkflowRefresh(
  event: Readonly<Record<string, unknown>>,
  expected: boolean,
  projectID = "project-1",
  workflowID = "workflow-1",
) {
  expect(shouldRefreshWorkflowEditor(eventParams(event), projectID, workflowID)).toBe(expected);
}

function expectWorkflowRefreshNotification(event: Readonly<Record<string, unknown>>, expected: boolean) {
  expect(shouldNotifyWorkflowEditorRefresh(eventParams(event), "project-1", "workflow-1")).toBe(expected);
}

function eventParams(event: Readonly<Record<string, unknown>>) {
  return { event };
}
