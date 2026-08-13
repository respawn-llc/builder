import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";

import type { WorkflowProjectEvent, WorkflowProjectEventHandler } from "@/api";

const fixture = vi.hoisted(() => {
  const noHandler = (): WorkflowProjectEventHandler | null => null;
  return {
    projectHandler: noHandler(),
    push: vi.fn(),
    translate: vi.fn((key: string) => key),
    workflowHandler: noHandler(),
  };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: fixture.translate }),
}));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppServices: () => ({
    api: {
      getWorkflow: async () => ({ workflow: { version: 1 } }),
      listProjectWorkflowLinks: async () => [{ projectID: "project-1", workflowID: "workflow-1" }],
      subscribeProject: (_projectID: string, handler: WorkflowProjectEventHandler) => {
        fixture.projectHandler = handler;
        return { close: vi.fn() };
      },
      subscribeWorkflow: (_workflowID: string, handler: WorkflowProjectEventHandler) => {
        fixture.workflowHandler = handler;
        return { close: vi.fn() };
      },
      validateWorkflow: async () => ({ errors: [], valid: true }),
    },
  }),
  useConnectionSnapshot: () => ({ generation: 1, phase: "connected" }),
  useStatusController: () => ({ push: fixture.push }),
}));

import {
  shouldNotifyWorkflowEditorRefresh,
  shouldRefreshWorkflowEditor,
  useWorkflowEditorData,
} from "./useWorkflowEditorData";

function workflowEvent(
  action: WorkflowProjectEvent["action"],
  workflowID = "workflow-1",
): WorkflowProjectEvent {
  return {
    action,
    occurredAtUnixMs: 1,
    primaryEntityID: workflowID,
    projectID: null,
    relatedIDs: [],
    resource: "workflow",
    workflowID,
  };
}

describe("Workflow Editor event effects", () => {
  beforeEach(() => {
    fixture.projectHandler = null;
    fixture.push.mockClear();
    fixture.translate.mockClear();
    fixture.workflowHandler = null;
  });

  it("refreshes and notifies for a matching graph save only", () => {
    const event = workflowEvent("graph_saved");

    expect(shouldRefreshWorkflowEditor(event, "project-1", "workflow-1")).toBe(true);
    expect(shouldNotifyWorkflowEditorRefresh(event, "project-1", "workflow-1")).toBe(true);
    expect(shouldRefreshWorkflowEditor(event, "project-1", "workflow-2")).toBe(false);
  });

  it("invalidates every editor owner for a matching graph save and ignores unrelated events", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const invalidation = vi.spyOn(queryClient, "invalidateQueries");
    const view = renderHook(() => useWorkflowEditorData("project-1", "workflow-1"), {
      wrapper: queryWrapper(queryClient),
    });
    await waitFor(() => {
      expect(fixture.workflowHandler).not.toBeNull();
      expect(fixture.projectHandler).not.toBeNull();
    });
    invalidation.mockClear();

    act(() => {
      fixture.workflowHandler?.onEvent(workflowEvent("graph_saved"));
    });
    await waitFor(() => {
      expect(invalidation).toHaveBeenCalledTimes(5);
      expect(fixture.push).toHaveBeenCalledOnce();
    });
    const invalidatedQueryKeys = invalidation.mock.calls.map((call) => {
      const [request] = call;
      if (request === undefined) {
        throw new Error("Workflow Editor invalidated a query without a request.");
      }
      return request.queryKey;
    });
    expect(invalidatedQueryKeys).toEqual(
      expect.arrayContaining([
        ["project-workflow-links", "project-1"],
        ["board", "project-1", "workflow-1"],
        ["board-node-cards", "project-1", "workflow-1"],
        ["workflow-definition", "workflow-1"],
        ["workflow-validation", "workflow-1", "execution"],
      ]),
    );

    invalidation.mockClear();
    fixture.push.mockClear();
    act(() => {
      fixture.workflowHandler?.onEvent(workflowEvent("graph_saved", "workflow-2"));
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(invalidation).not.toHaveBeenCalled();
    expect(fixture.push).not.toHaveBeenCalled();

    act(() => {
      fixture.workflowHandler?.onEvent(workflowEvent("deleted"));
    });
    await waitFor(() => {
      expect(invalidation).toHaveBeenCalledTimes(5);
    });
    expect(fixture.push).not.toHaveBeenCalled();

    view.unmount();
    queryClient.clear();
  });

  it("refreshes a deleted Workflow without showing the normal update notice", () => {
    const event = workflowEvent("deleted");

    expect(shouldRefreshWorkflowEditor(event, "project-1", "workflow-1")).toBe(true);
    expect(shouldNotifyWorkflowEditorRefresh(event, "project-1", "workflow-1")).toBe(false);
  });

  it("surfaces terminal subscription failure without refreshing editor state", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const invalidation = vi.spyOn(queryClient, "invalidateQueries");
    const view = renderHook(() => useWorkflowEditorData("project-1", "workflow-1"), {
      wrapper: queryWrapper(queryClient),
    });
    await waitFor(() => {
      expect(fixture.workflowHandler).not.toBeNull();
      expect(fixture.projectHandler).not.toBeNull();
    });
    invalidation.mockClear();

    act(() => {
      fixture.workflowHandler?.onError(new Error("stream contract failed"));
    });

    expect(invalidation).not.toHaveBeenCalled();
    expect(fixture.push).toHaveBeenCalledWith({
      body: "stream contract failed",
      durationMs: Infinity,
      id: "workflow-editor-workflow-subscription-failed",
      tone: "danger",
      title: "workflowEditor.subscriptionFailed",
    });

    view.unmount();
    queryClient.clear();
  });
});

function queryWrapper(queryClient: QueryClient) {
  return function QueryWrapper({ children }: Readonly<{ children: ReactNode }>) {
    return createElement(QueryClientProvider, { children, client: queryClient });
  };
}
