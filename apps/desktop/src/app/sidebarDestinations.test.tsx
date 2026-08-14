import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { isValidElement } from "react";
import type { SidebarDestination } from "@/app-facade";
import { createTestSidebarNavigator } from "@/test-support/sidebar";
import { SidebarDestinationView } from "./sidebarDestinations";
import { sidebarDestinationPolicy } from "./sidebarDestinationPolicy";
const headerAction = vi.hoisted(() => vi.fn<(action: unknown) => void>());
const fixture = vi.hoisted(() => ({
  copyText: vi.fn(async () => undefined),
  openWindow: vi.fn(async () => undefined),
  openProject: vi.fn(async () => undefined),
  openWorkflowEditor: vi.fn(async () => undefined),
  workflowEditorProps: vi.fn<(props: unknown) => void>(),
  newTaskProps: vi.fn<(props: unknown) => void>(),
}));
vi.mock("@/app-facade", () => ({
  sidebarTitle: () => "",
  useAppNavigation: () => ({
    openProject: fixture.openProject,
    openWorkflowEditor: fixture.openWorkflowEditor,
  }),
  useAppServices: () => ({
    nativeBridge: {
      capabilities: { clipboard: { writeText: true }, dialogWindows: true },
      clipboard: { writeText: fixture.copyText },
      dialogs: { openWindow: fixture.openWindow },
    },
  }),
  usePublishSidebarHeaderAction: headerAction,
  useStatusController: () => ({ push: vi.fn() }),
}));
vi.mock("@/features/task-detail", () => ({
  TaskDetailSurface: () => <div />,
}));
vi.mock("@/features/project-edit", () => ({
  ProjectDeleteButton: () => <div />,
  ProjectEditRoute: () => <div />,
}));
vi.mock("@/features/home", () => ({
  SidebarInboxNav: () => <div />,
}));
vi.mock("@/features/tasks", () => ({
  NewTaskForm: (props: Readonly<{ onPendingChange?: (pending: boolean) => void }>) => {
    fixture.newTaskProps(props);
    return (
      <>
        <button
          data-testid="new-task-pending"
          onClick={() => {
            props.onPendingChange?.(true);
          }}
        />
        <button
          data-testid="new-task-settled"
          onClick={() => {
            props.onPendingChange?.(false);
          }}
        />
      </>
    );
  },
}));

vi.mock("@/features/workflows", () => ({
  LinkWorkflowSidebar: (
    props: Readonly<{
      onCreated: (workflowID: string) => void;
      onLinked: (workflowID: string) => void;
    }>,
  ) => (
    <>
      <button
        data-testid="workflow-created"
        onClick={() => {
          props.onCreated("workflow-created");
        }}
      />
      <button
        data-testid="workflow-linked"
        onClick={() => {
          props.onLinked("workflow-linked");
        }}
      />
    </>
  ),
  WorkflowCreateForm: (
    props: Readonly<{
      onCreated: (result: Readonly<{ workflow: Readonly<{ id: string }> }>) => void;
    }>,
  ) => (
    <button
      data-testid="workflow-create-success"
      onClick={() => {
        props.onCreated({ workflow: { id: "workflow-created" } });
      }}
    />
  ),
}));

vi.mock("@/features/workflow-editor", () => ({
  WorkflowEditorRoute: (props: unknown) => {
    fixture.workflowEditorProps(props);
    return <div />;
  },
  WorkflowDeleteButton: ({
    onDeleted,
    workflowID,
  }: Readonly<{ onDeleted?: () => void; workflowID: string }>) => (
    <button data-testid={`workflow-delete-${workflowID}`} onClick={onDeleted} />
  ),
  WorkflowInspectorSidebar: ({ onMissingSelectedNode }: Readonly<{ onMissingSelectedNode: () => void }>) => (
    <button data-testid="workflow-inspector-missing" onClick={onMissingSelectedNode} />
  ),
}));

function mountDestination(destination: SidebarDestination, pageNavigator = createTestSidebarNavigator()) {
  render(<SidebarDestinationView destination={destination} navigator={pageNavigator} />);
  return pageNavigator;
}

describe("Sidebar destination completion ownership", () => {
  beforeEach(() => {
    for (const mock of Object.values(fixture)) mock.mockClear();
    headerAction.mockClear();
  });
  it("deduplicates only Task Detail destinations", () => {
    const task = { kind: "taskDetail", taskID: "task-1" } as const;
    const custom = { kind: "custom", title: "same", content: null } as const;
    expect(sidebarDestinationPolicy.equals(task, { ...task })).toBe(true);
    expect(sidebarDestinationPolicy.equals(custom, { ...custom })).toBe(false);
  });
  it("opens Workflow settings without mounting the graph surface", () => {
    const navigator = createTestSidebarNavigator();
    mountDestination({ kind: "workflowSettings", workflowID: "workflow-1" }, navigator);

    expect(fixture.workflowEditorProps).toHaveBeenCalledWith({
      navigator,
      projectID: "",
      surface: "settings",
      workflowID: "workflow-1",
    });
  });
  it("locks header exit only for a pending Task Detail-originated New Task", () => {
    const initialPreparedDependency = {
      direction: "blocks",
      taskID: "task-1",
      shortID: "KENT-1",
      title: "Origin",
      workflowID: "workflow-1",
      status: {
        kind: "backlog",
        nativeState: "active",
        nodeIDs: [],
        attentionTypes: [],
      },
    } as const;
    const pageNavigator = mountDestination({
      boardQueryWorkflowID: "workflow-1",
      initialPreparedDependency,
      kind: "newTask",
      projectID: "project-1",
      workflowID: "workflow-1",
    });

    fireEvent.click(screen.getByTestId("new-task-pending"));
    expect(pageNavigator.registerAvailability).toHaveBeenLastCalledWith({ back: false, close: false });
    fireEvent.click(screen.getByTestId("new-task-settled"));
    expect(pageNavigator.registerAvailability).toHaveBeenLastCalledWith({ back: true, close: true });
    expect(fixture.newTaskProps).toHaveBeenCalledWith(
      expect.objectContaining({
        initialPreparedDependency,
        navigator: pageNavigator,
      }),
    );
  });
  it.each([{}, { parentReturnDirection: "blocked-by" as const }])(
    "keeps root and stacked New Task exit available while pending",
    (context) => {
      const pageNavigator = mountDestination({
        boardQueryWorkflowID: "workflow-1",
        ...context,
        kind: "newTask",
        projectID: "project-1",
        workflowID: "workflow-1",
      });

      fireEvent.click(screen.getByTestId("new-task-pending"));
      expect(pageNavigator.registerAvailability).toHaveBeenLastCalledWith({ back: true, close: true });
    },
  );
  it("publishes Link Workflow creation through scoped replace", () => {
    const destination = { kind: "linkWorkflow", projectID: "project-1" } as const;
    const navigator = mountDestination(destination);
    const action = headerAction.mock.lastCall?.[0];
    if (!isValidElement(action)) throw new Error("Expected the Link Workflow header action.");
    render(action);
    fireEvent.click(screen.getByRole("button", { name: "workflowLibrary.newWorkflow" }));
    expect(navigator.replace).toHaveBeenCalledWith({ ...destination, creating: true });
  });
  it.each([
    {
      destination: { kind: "workflowCreate", projectID: "project-1" } satisfies SidebarDestination,
      trigger: "workflow-create-success",
      follow: () => fixture.openWorkflowEditor,
    },
    {
      destination: {
        kind: "linkWorkflow",
        creating: true,
        projectID: "project-1",
      } satisfies SidebarDestination,
      trigger: "workflow-created",
      follow: () => fixture.openWorkflowEditor,
    },
    {
      destination: { kind: "linkWorkflow", projectID: "project-1" } satisfies SidebarDestination,
      trigger: "workflow-linked",
      follow: () => fixture.openProject,
    },
  ])("runs $trigger follow-up only after accepted scoped close", ({ destination, follow, trigger }) => {
    const acceptedNavigator = mountDestination(destination);
    fireEvent.click(screen.getByTestId(trigger));
    expect(acceptedNavigator.close).toHaveBeenCalledOnce();
    expect(follow()).toHaveBeenCalledOnce();

    const stale = createTestSidebarNavigator({ close: vi.fn(() => "stale" as const) });
    mountDestination(destination, stale);
    const staleTrigger = screen.getAllByTestId(trigger)[1];
    if (staleTrigger === undefined) throw new Error("Expected the stale destination trigger.");
    fireEvent.click(staleTrigger);
    expect(stale.close).toHaveBeenCalledOnce();
    expect(follow()).toHaveBeenCalledOnce();
  });

  it("closes Workflow Inspector through its scoped navigator when the selected node is missing", () => {
    const navigator = mountDestination({
      kind: "workflowInspect",
      selection: { kind: "workflow" },
      workflowID: "workflow-1",
    });
    fireEvent.click(screen.getByTestId("workflow-inspector-missing"));
    expect(navigator.close).toHaveBeenCalledOnce();
  });
  it.each<Readonly<{ outcome: "accepted" | "stale" }>>([{ outcome: "accepted" }, { outcome: "stale" }])(
    "scopes Workflow Inspector deletion completion when close is $outcome",
    ({ outcome }) => {
      const navigator = createTestSidebarNavigator({ close: vi.fn(() => outcome) });
      mountDestination(
        { kind: "workflowInspect", selection: { kind: "workflow" }, workflowID: "workflow-1" },
        navigator,
      );
      const deleteAction = headerAction.mock.lastCall?.[0];
      if (!isValidElement(deleteAction)) throw new Error("Expected Workflow delete.");
      render(deleteAction);
      fireEvent.click(screen.getByTestId("workflow-delete-workflow-1"));
      expect(navigator.close).toHaveBeenCalledOnce();
    },
  );

  it("publishes Workflow Inspector ID-copy actions", async () => {
    mountDestination({
      kind: "workflowInspect",
      selection: { kind: "node", nodeID: "node-1" },
      workflowID: "workflow-1",
    });
    const copyAction = headerAction.mock.lastCall?.[0];
    if (!isValidElement(copyAction)) throw new Error("Expected Workflow ID copy.");
    render(copyAction);
    fireEvent.click(screen.getByText("node-1"));
    await waitFor(() => {
      expect(fixture.copyText).toHaveBeenCalledWith("node-1");
    });

    mountDestination({
      kind: "workflowInspect",
      selection: { edgeID: "edge-1", kind: "edge" },
      workflowID: "workflow-1",
    });
    const edgeCopyAction = headerAction.mock.lastCall?.[0];
    if (!isValidElement(edgeCopyAction)) throw new Error("Expected Workflow transition ID copy.");
    render(edgeCopyAction);
    fireEvent.click(screen.getByText("edge-1"));
    await waitFor(() => {
      expect(fixture.copyText).toHaveBeenCalledWith("edge-1");
    });
  });

  it.each<Readonly<{ outcome: "accepted" | "stale" }>>([{ outcome: "accepted" }, { outcome: "stale" }])(
    "keeps pop-out completion scoped when close is $outcome",
    async ({ outcome }) => {
      const navigator = createTestSidebarNavigator({ close: vi.fn(() => outcome) });
      mountDestination({ kind: "taskDetail", taskID: "task-1" }, navigator);
      const action = headerAction.mock.lastCall?.[0];
      if (!isValidElement(action)) throw new Error("Expected the Task Detail header action.");
      render(action);
      fireEvent.click(screen.getByRole("button", { name: "app.popOut" }));
      await waitFor(() => {
        expect(fixture.openWindow).toHaveBeenCalledOnce();
      });

      expect(fixture.openWindow).toHaveBeenCalledWith(
        expect.objectContaining({ params: { taskID: "task-1" }, route: "/native-dialog/task-detail" }),
      );
      expect(navigator.close).toHaveBeenCalledOnce();
    },
  );
});
