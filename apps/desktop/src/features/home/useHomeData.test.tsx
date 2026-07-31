import { act, render, screen, waitFor } from "@testing-library/react";
import { useEffect, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";

import type { JsonValue } from "@/api";
import { SidebarContext, type SidebarController, type SidebarDestination } from "@/app-facade";
import { createTestServices, TestAppProviders, type TestAppServices } from "@/test-support/app-services";
import type { FakeRpcTransport, FakeRoute } from "@/test-support/api";
import { flushQueuedWork, installAnimationFrameTestSupport } from "@/test-support/scheduling";
import {
  workflowAttentionCalls,
  workflowAttentionRpcMethods,
} from "@/test-support/workflow-attention";
import { SidebarInboxNav } from "./SidebarInboxNav";
import { useGlobalAttentionEvents, useGlobalAttentionPages } from "./useHomeData";

describe("Home global attention data", () => {
  beforeEach(() => {
    installAnimationFrameTestSupport();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("loads once before opening the stream, refreshes for events, and reconciles after recovery", async () => {
    const services = createAttentionServices();
    renderHome(services, <HomeAttentionEventsHarness />);

    await expectAttentionCalls(services.transport, 1);

    act(() => {
      services.transport.open(workflowAttentionRpcMethods.subscribeProject);
    });
    await flushQueuedWork();
    expect(attentionPageTokens(services.transport)).toEqual([""]);

    act(() => {
      services.transport.emit(workflowAttentionRpcMethods.projectEvent, attentionChangingProjectEvent);
    });
    await expectAttentionCalls(services.transport, 2);

    act(() => {
      services.transport.fail(workflowAttentionRpcMethods.subscribeProject, new Error("stream failed"));
    });
    await flushQueuedWork();
    expect(attentionPageTokens(services.transport)).toHaveLength(2);

    act(() => {
      services.transport.open(workflowAttentionRpcMethods.subscribeProject);
    });
    await expectAttentionCalls(services.transport, 3);
  });

  it("reconciles a stream that fails before its first successful open", async () => {
    const services = createAttentionServices();
    renderHome(services, <HomeAttentionEventsHarness />);

    await expectAttentionCalls(services.transport, 1);

    act(() => {
      services.transport.fail(
        workflowAttentionRpcMethods.subscribeProject,
        new Error("stream failed before open"),
      );
    });
    await flushQueuedWork();
    expect(attentionPageTokens(services.transport)).toEqual([""]);

    act(() => {
      services.transport.open(workflowAttentionRpcMethods.subscribeProject);
    });
    await expectAttentionCalls(services.transport, 2);
  });

  it("reconciles after a decoder error while the Project stream remains open", async () => {
    const services = createAttentionServices();
    renderHome(services, <HomeAttentionEventsHarness />);

    await expectAttentionCalls(services.transport, 1);
    act(() => {
      services.transport.open(workflowAttentionRpcMethods.subscribeProject);
    });
    await flushQueuedWork();

    act(() => {
      services.transport.emit(workflowAttentionRpcMethods.projectEvent, invalidProjectEvent);
    });
    await expectAttentionCalls(services.transport, 2);
  });

  it("does not refetch Sidebar navigation when Home already owns populated attention data", async () => {
    const services = createAttentionServices();
    const view = renderHome(services, <HomeAttentionQueryHarness />);

    await expectAttentionCalls(services.transport, 1);

    view.rerender(
      <TestAppProviders services={services}>
        <SidebarContext.Provider value={sidebarController}>
          <HomeAttentionQueryHarness />
          <SidebarInboxNav destination={taskDetailDestination} />
        </SidebarContext.Provider>
      </TestAppProviders>,
    );
    await flushQueuedWork();

    expect(attentionPageTokens(services.transport)).toEqual([""]);
  });

  it("loads a cold cache when Sidebar is the only global attention observer", async () => {
    const services = createAttentionServices();
    renderHome(services, <SidebarInboxNav destination={taskDetailDestination} />);

    await expectAttentionCalls(services.transport, 1);
    expect(attentionPageTokens(services.transport)).toEqual([""]);
  });

  it("refreshes stale data for a sole Sidebar observer and renders the refreshed navigation", async () => {
    const services = createAttentionServices((pageToken, callIndex) => {
      if (pageToken !== "") {
        return attentionResponse([]);
      }
      return attentionResponse(callIndex === 0 ? [attentionItem("task-1")] : [
        attentionItem("task-1"),
        attentionItem("task-2"),
      ]);
    });
    const openedDestinations: SidebarDestination[] = [];
    const controller = createSidebarController((destination) => {
      openedDestinations.push(destination);
    });
    const view = renderHome(services, <HomeAttentionQueryHarness />);

    await expectAttentionCalls(services.transport, 1);
    view.rerender(
      <TestAppProviders services={services}>
        <SidebarContext.Provider value={sidebarController}>
          <div />
        </SidebarContext.Provider>
      </TestAppProviders>,
    );
    view.rerender(
      <TestAppProviders services={services}>
        <SidebarContext.Provider value={controller}>
          <SidebarInboxNav destination={taskDetailDestination} />
        </SidebarContext.Provider>
      </TestAppProviders>,
    );

    await expectAttentionCalls(services.transport, 2);
    await waitFor(() => {
      expect(screen.getAllByRole("button")).toHaveLength(1);
    });
    const nextButton = screen.getAllByRole("button")[0];
    if (nextButton === undefined) {
      throw new Error("Sidebar Inbox navigation did not render its available control.");
    }
    act(() => {
      nextButton.click();
    });
    expect(openedDestinations).toHaveLength(1);
    expect(openedDestinations[0]).toMatchObject({
      inboxNav: true,
      kind: "taskDetail",
      taskID: "task-2",
    });
  });

  it("forwards the production infinite-query next-page token exactly once", async () => {
    let attentionQuery: ReturnType<typeof useGlobalAttentionPages> | undefined;
    const services = createAttentionServices((pageToken) =>
      pageToken === "" ? attentionResponse([], "page-2") : attentionResponse([]),
    );
    renderHome(
      services,
      <HomeAttentionQueryHarness
        onQuery={(query) => {
          attentionQuery = query;
        }}
      />,
    );

    await expectAttentionCalls(services.transport, 1);
    await waitFor(() => {
      expect(attentionQuery?.hasNextPage).toBe(true);
    });
    const query = attentionQuery;
    if (query === undefined) {
      throw new Error("Home attention query was not exposed by the rendered harness.");
    }

    await act(async () => {
      await query.fetchNextPage();
    });
    await expectAttentionCalls(services.transport, 2);
    expect(attentionPageTokens(services.transport)).toEqual(["", "page-2"]);
  });
});

function renderHome(services: TestAppServices, children: ReactNode) {
  return render(
    <TestAppProviders services={services}>
      <SidebarContext.Provider value={sidebarController}>{children}</SidebarContext.Provider>
    </TestAppProviders>,
  );
}

function HomeAttentionEventsHarness() {
  useGlobalAttentionPages();
  useGlobalAttentionEvents();
  return null;
}

function HomeAttentionQueryHarness({
  onQuery,
}: Readonly<{
  onQuery?: (query: ReturnType<typeof useGlobalAttentionPages>) => void;
}>) {
  const query = useGlobalAttentionPages();
  useEffect(() => {
    onQuery?.(query);
  }, [onQuery, query]);
  return null;
}

type AttentionPageFactory = (
  pageToken: string,
  callIndex: number,
) => Readonly<Record<string, JsonValue>>;

function createAttentionServices(page: AttentionPageFactory = () => attentionResponse([])): TestAppServices {
  return createTestServices([attentionRoute(page)]);
}

function attentionRoute(
  page: (pageToken: string, callIndex: number) => Readonly<Record<string, JsonValue>>,
): FakeRoute {
  return {
    method: workflowAttentionRpcMethods.list,
    handler(params, callIndex) {
      const pageToken = attentionRequestParamsSchema.parse(params).page_token;
      return page(pageToken, callIndex);
    },
  };
}

function attentionResponse(items: readonly Readonly<Record<string, JsonValue>>[], nextPageToken = "") {
  return {
    items,
    next_page_token: nextPageToken,
    generated_at_unix_ms: 1,
  } satisfies Readonly<Record<string, JsonValue>>;
}

function attentionItem(taskID: string): Readonly<Record<string, JsonValue>> {
  return {
    id: `approval:${taskID}`,
    kind: "approval",
    project_id: "project-1",
    workflow_id: "workflow-1",
    task_id: taskID,
    task_short_id: taskID,
    task_title: taskID,
    approval_id: `approval-${taskID}`,
    message: "Approval required",
    approval_snapshot: {
      source_node_display_name: "Review",
      targets: [{ display_name: "Done" }],
      commentary: "",
      output_values: {},
      workflow_revision_seen: 1,
    },
    occurred_at_unix_ms: 1,
  };
}

function attentionPageTokens(transport: FakeRpcTransport): string[] {
  return workflowAttentionCalls(transport).map((call) => attentionRequestParamsSchema.parse(call.params).page_token);
}

async function expectAttentionCalls(transport: FakeRpcTransport, count: number): Promise<void> {
  await waitFor(() => {
    expect(workflowAttentionCalls(transport)).toHaveLength(count);
  });
}

const taskDetailDestination = {
  kind: "taskDetail",
  inboxNav: true,
  taskID: "task-1",
} as const;

const sidebarController = createSidebarController();

function createSidebarController(
  onOpen: (destination: SidebarDestination) => void = () => {
    return;
  },
): SidebarController {
  return {
    activeDestination: null,
    closeSidebar() {
      return;
    },
    async openSidebar(destination) {
      onOpen(destination);
      return { status: "canceled", reason: "closed" };
    },
    phase: "open",
    resolveSidebar() {
      return;
    },
    resizeSidebar() {
      return;
    },
    sidebarWidthPx: 320,
  };
}

const attentionChangingProjectEvent = {
  event: {
    action: "updated",
    occurred_at_unix_ms: 2,
    primary_entity_id: "task-1",
    project_id: "project-1",
    related_ids: [],
    resource: "task",
    workflow_id: "workflow-1",
  },
} as const;

const invalidProjectEvent = {
  event: {
    action: "updated",
    occurred_at_unix_ms: 3,
    primary_entity_id: "task-1",
    project_id: "project-1",
    related_ids: [],
    resource: "task",
  },
} as const;

const attentionRequestParamsSchema = z
  .object({
    page_size: z.number(),
    page_token: z.string(),
  })
  .strict();
