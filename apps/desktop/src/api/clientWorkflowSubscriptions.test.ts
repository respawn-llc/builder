import type { ApiService } from "./apiService";
import { ApiClient } from "./client";
import { ContractError } from "./errors";
import { FakeRpcTransport } from "@/test-support/api";
import type { WorkflowProjectEvent } from "./workflowProjectEvents";

const workflowProjectWireEvent = {
  event: {
    action: "question_waiting",
    occurred_at_unix_ms: 1,
    primary_entity_id: "task-1",
    project_id: "project-1",
    related_ids: ["run-1", "ask-1"],
    resource: "task",
    workflow_id: "workflow-1",
  },
} as const;

const workflowProjectEvent: WorkflowProjectEvent = {
  action: "question_waiting",
  occurredAtUnixMs: 1,
  primaryEntityID: "task-1",
  projectID: "project-1",
  relatedIDs: ["run-1", "ask-1"],
  resource: "task",
  workflowID: "workflow-1",
};

describe("ApiClient workflow subscriptions", () => {
  it("adapts project subscription events before feature code receives them", () => {
    const transport = new FakeRpcTransport([]);
    const client: ApiService = new ApiClient(transport);
    const events: WorkflowProjectEvent[] = [];

    client.subscribeProject("project-1", eventCollector(events));
    transport.emit("workflow.project", workflowProjectWireEvent);

    expect(events).toEqual([workflowProjectEvent]);
  });

  it("adapts workflow subscription events before feature code receives them", () => {
    const transport = new FakeRpcTransport([]);
    const client: ApiService = new ApiClient(transport);
    const events: WorkflowProjectEvent[] = [];

    client.subscribeWorkflow("workflow-1", eventCollector(events));
    transport.emit("workflow.event", workflowProjectWireEvent);

    expect(events).toEqual([workflowProjectEvent]);
  });

  it("rejects subscription event methods that do not match the subscribed stream", () => {
    const transport = new FakeRpcTransport([]);
    const client: ApiService = new ApiClient(transport);
    const events: WorkflowProjectEvent[] = [];
    const errors: Error[] = [];

    client.subscribeProject("project-1", eventCollector(events, errors));
    transport.emit("workflow.event", workflowProjectWireEvent);

    expect(events).toEqual([]);
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(ContractError);
  });

  it("surfaces malformed workflow subscription events as contract errors", () => {
    const transport = new FakeRpcTransport([]);
    const client: ApiService = new ApiClient(transport);
    const events: WorkflowProjectEvent[] = [];
    const errors: Error[] = [];

    client.subscribeWorkflow("workflow-1", eventCollector(events, errors));
    transport.emit("workflow.event", {
      event: {
        action: "linked",
        primary_entity_id: "task-1",
        project_id: "project-1",
        resource: "task",
        workflow_id: "workflow-1",
      },
    });

    expect(events).toEqual([]);
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(ContractError);
  });
});

function eventCollector(events: WorkflowProjectEvent[], errors: Error[] = []) {
  return {
    onEvent(event: WorkflowProjectEvent) {
      events.push(event);
    },
    onComplete() {
      return;
    },
    onError(error: Error) {
      errors.push(error);
    },
  };
}
