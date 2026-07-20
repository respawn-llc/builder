import { act, render, screen, waitFor } from "@testing-library/react";

import { ProjectLabelsProvider, useProjectLabelCatalog } from "./index";
import { TestAppProviders, createTestServices } from "@/test-support/app-services";
import { installTestStorage } from "@/test-support/storage";

const alphaID = "38bf0da7-a3f7-4c15-bc5f-c8fca538e667";

describe("Project label events", () => {
  beforeEach(() => {
    installTestStorage("localStorage");
  });

  it("reconciles the bounded catalog after a typed label event", async () => {
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        handler: (_params, callIndex) => ({
          catalog: {
            project_id: "project-1",
            labels: callIndex === 0 ? [] : [{ id: alphaID, name: "Alpha" }],
          },
        }),
      },
    ]);

    render(
      <TestAppProviders services={services}>
        <ProjectLabelsProvider projectID="project-1">
          <CatalogCapture />
        </ProjectLabelsProvider>
      </TestAppProviders>,
    );
    await waitFor(() => {
      expect(screen.getByRole("status")).toHaveTextContent("0");
    });
    expect(services.transport.subscriptions).toContainEqual({
      method: "workflow.subscribeProject",
      params: { project_id: "project-1" },
    });

    services.transport.emit("workflow.project", {
      event: {
        action: "created",
        occurred_at_unix_ms: 1,
        primary_entity_id: alphaID,
        project_id: "project-1",
        resource: "label",
      },
    });

    await waitFor(() => {
      expect(screen.getByRole("status")).toHaveTextContent("Alpha");
    });
  });

  it("refreshes catalog and membership state across open, completion, error, and reconnect boundaries", async () => {
    const membershipEffects: unknown[] = [];
    const backgroundErrors: unknown[] = [];
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [],
          },
        },
      },
    ]);

    render(
      <TestAppProviders services={services}>
        <ProjectLabelsProvider
          onBackgroundError={(error) => {
            backgroundErrors.push(error);
          }}
          onMembershipRefresh={(effect) => {
            membershipEffects.push(effect);
          }}
          projectID="project-1"
        >
          <CatalogCapture />
        </ProjectLabelsProvider>
      </TestAppProviders>,
    );
    await waitFor(() => {
      expect(screen.getByRole("status")).toHaveTextContent("0");
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
      ).toHaveLength(1);
    });

    act(() => {
      services.transport.open("workflow.subscribeProject");
    });
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
      ).toHaveLength(2);
    });

    act(() => {
      services.transport.complete("workflow.subscribeProject", 1, "gap");
    });
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
      ).toHaveLength(3);
    });

    const subscriptionError = new Error("offline");
    act(() => {
      services.transport.fail("workflow.subscribeProject", subscriptionError);
    });
    await waitFor(() => {
      expect(backgroundErrors).toContain(subscriptionError);
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
      ).toHaveLength(4);
    });

    act(() => {
      services.transport.open("workflow.subscribeProject");
    });
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.project.label.list"),
      ).toHaveLength(5);
    });
    expect(membershipEffects).toEqual(
      Array.from({ length: 4 }, () => ({
        kind: "subscription.refresh",
        projectID: "project-1",
      })),
    );
  });

  it("routes only label-membership task events through the typed host callback", async () => {
    const membershipEffects: unknown[] = [];
    const services = createTestServices([
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [],
          },
        },
      },
    ]);

    render(
      <TestAppProviders services={services}>
        <ProjectLabelsProvider
          onMembershipRefresh={(effect) => {
            membershipEffects.push(effect);
          }}
          projectID="project-1"
        >
          <CatalogCapture />
        </ProjectLabelsProvider>
      </TestAppProviders>,
    );
    await waitFor(() => {
      expect(services.transport.subscriptions).toHaveLength(1);
    });

    services.transport.emit("workflow.project", {
      event: {
        action: "labels_changed",
        occurred_at_unix_ms: 1,
        primary_entity_id: "task-1",
        project_id: "project-1",
        resource: "task",
        workflow_id: "workflow-1",
      },
    });
    await waitFor(() => {
      expect(membershipEffects).toEqual([
        {
          kind: "task.labels_changed",
          projectID: "project-1",
          taskID: "task-1",
          workflowID: "workflow-1",
        },
      ]);
    });

    services.transport.emit("workflow.project", {
      event: {
        action: "updated",
        occurred_at_unix_ms: 2,
        primary_entity_id: "task-2",
        project_id: "project-1",
        resource: "task",
        workflow_id: "workflow-1",
      },
    });
    services.transport.emit("workflow.project", {
      event: {
        action: "created",
        occurred_at_unix_ms: 3,
        primary_entity_id: alphaID,
        project_id: "project-other",
        resource: "label",
      },
    });
    await waitFor(() => {
      expect(membershipEffects).toHaveLength(1);
    });
  });
});

function CatalogCapture() {
  const catalog = useProjectLabelCatalog();
  return <output role="status">{catalog.data?.labels.map((label) => label.name).join(",") ?? "0"}</output>;
}
