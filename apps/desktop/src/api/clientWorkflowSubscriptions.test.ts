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
    related_ids: ["session-1", "ask-1"],
    resource: "task",
    workflow_id: "11111111-1111-4111-8111-111111111111",
  },
} as const;

const workflowProjectEvent: WorkflowProjectEvent = {
  action: "question_waiting",
  occurredAtUnixMs: 1,
  primaryEntityID: "task-1",
  projectID: "project-1",
  relatedIDs: ["session-1", "ask-1"],
  resource: "task",
  workflowID: "11111111-1111-4111-8111-111111111111",
};

describe("ApiClient workflow subscriptions", () => {
  it("adapts dependency change pairs through the typed Task event contract", () => {
    const transport = new FakeRpcTransport([]);
    const client: ApiService = new ApiClient(transport);
    const events: WorkflowProjectEvent[] = [];

    client.subscribeProject("project-1", eventCollector(events));
    transport.emit("workflow.project", {
      event: {
        resource: "task",
        action: "dependencies_changed",
        occurred_at_unix_ms: 1,
        primary_entity_id: "task-1",
        project_id: "project-1",
        workflow_id: "11111111-1111-4111-8111-111111111111",
        related_ids: ["task-2"],
      },
    });

    expect(events).toEqual([
      expect.objectContaining({
        action: "dependencies_changed",
        primaryEntityID: "task-1",
        relatedIDs: ["task-2"],
      }),
    ]);
  });

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

    client.subscribeWorkflow("11111111-1111-4111-8111-111111111111", eventCollector(events));
    transport.emit("workflow.event", workflowProjectWireEvent);

    expect(events).toEqual([workflowProjectEvent]);
  });

  it("adapts label catalog and task assignment events through typed scopes", () => {
    const transport = new FakeRpcTransport([]);
    const client: ApiService = new ApiClient(transport);
    const events: WorkflowProjectEvent[] = [];

    client.subscribeProject("project-1", eventCollector(events));
    transport.emit("workflow.project", {
      event: {
        action: "reordered",
        occurred_at_unix_ms: 1,
        primary_entity_id: "project-1",
        project_id: "project-1",
        resource: "label_catalog",
      },
    });
    transport.emit("workflow.project", {
      event: {
        action: "renamed",
        occurred_at_unix_ms: 2,
        primary_entity_id: "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf",
        project_id: "project-1",
        resource: "label",
      },
    });
    transport.emit("workflow.project", {
      event: {
        action: "labels_changed",
        occurred_at_unix_ms: 3,
        primary_entity_id: "task-1",
        project_id: "project-1",
        resource: "task",
        workflow_id: "11111111-1111-4111-8111-111111111111",
      },
    });

    expect(events).toEqual([
      {
        action: "reordered",
        occurredAtUnixMs: 1,
        primaryEntityID: "project-1",
        projectID: "project-1",
        relatedIDs: [],
        resource: "label_catalog",
        workflowID: null,
      },
      {
        action: "renamed",
        occurredAtUnixMs: 2,
        primaryEntityID: "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf",
        projectID: "project-1",
        relatedIDs: [],
        resource: "label",
        workflowID: null,
      },
      {
        action: "labels_changed",
        occurredAtUnixMs: 3,
        primaryEntityID: "task-1",
        projectID: "project-1",
        relatedIDs: [],
        resource: "task",
        workflowID: "11111111-1111-4111-8111-111111111111",
      },
    ]);
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

    client.subscribeWorkflow("11111111-1111-4111-8111-111111111111", eventCollector(events, errors));
    transport.emit("workflow.event", {
      event: {
        action: "linked",
        primary_entity_id: "task-1",
        project_id: "project-1",
        resource: "task",
        workflow_id: "11111111-1111-4111-8111-111111111111",
      },
    });

    expect(events).toEqual([]);
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(ContractError);
  });

  it("rejects the removed task-canceled event action", () => {
    const transport = new FakeRpcTransport([]);
    const client: ApiService = new ApiClient(transport);
    const events: WorkflowProjectEvent[] = [];
    const errors: Error[] = [];

    client.subscribeProject("project-1", eventCollector(events, errors));
    transport.emit("workflow.project", {
      event: {
        action: "canceled",
        occurred_at_unix_ms: 1,
        primary_entity_id: "task-1",
        project_id: "project-1",
        resource: "task",
        workflow_id: "11111111-1111-4111-8111-111111111111",
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
