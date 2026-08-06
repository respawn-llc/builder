import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { isValidElement } from "react";
import type { SidebarDestination } from "@/app-facade";
import { createTestSidebarNavigator } from "@/test-support/sidebar";
import { SidebarDestinationView } from "./sidebarDestinations";
import { sidebarDestinationPolicy } from "./sidebarDestinationPolicy";
const headerAction = vi.hoisted(() => vi.fn<(action: unknown) => void>());
const fixture = vi.hoisted(() => ({
  openWindow: vi.fn(async () => undefined),
  openProject: vi.fn(async () => undefined),
  openWorkflowEditor: vi.fn(async () => undefined),
}));
vi.mock("@/app-facade", () => ({
  sidebarTitle: () => "",
  useAppNavigation: () => ({
    openProject: fixture.openProject,
    openWorkflowEditor: fixture.openWorkflowEditor,
  }),
  useAppServices: () => ({
    nativeBridge: {
      capabilities: { dialogWindows: true },
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
  NewTaskForm: (props: Readonly<{
    onPendingChange?: (pending: boolean) => void;
    onProjectMissing?: () => void;
    onSubmitted: (taskID: string) => void;
  }>) => (
    <>
      <button data-testid="new-task-pending" onClick={() => { props.onPendingChange?.(true); }} />
      <button data-testid="new-task-settled" onClick={() => { props.onPendingChange?.(false); }} />
      <button data-testid="new-task-missing" onClick={props.onProjectMissing} />
      <button data-testid="new-task-success" onClick={() => { props.onSubmitted("task-created"); }} />
    </>
  ),
}));

vi.mock("@/features/workflows", () => ({
  LinkWorkflowSidebar: (props: Readonly<{
    onCreated: (workflowID: string) => void;
    onLinked: (workflowID: string) => void;
  }>) => (
    <>
      <button data-testid="workflow-created" onClick={() => { props.onCreated("workflow-created"); }} />
      <button data-testid="workflow-linked" onClick={() => { props.onLinked("workflow-linked"); }} />
    </>
  ),
  WorkflowCreateForm: (props: Readonly<{
    onCreated: (result: Readonly<{ workflow: Readonly<{ id: string }> }>) => void;
    onProjectMissing?: () => void;
  }>) => (
    <>
      <button
        data-testid="workflow-create-success"
        onClick={() => { props.onCreated({ workflow: { id: "workflow-created" } }); }}
      />
      <button data-testid="workflow-create-missing" onClick={props.onProjectMissing} />
    </>
  ),
}));

vi.mock("@/features/workflow-editor", () => ({
  WorkflowEditorRoute: () => <div />,
  WorkflowInspectorSidebar: (props: Readonly<{ onMissingSelectedNode: () => void }>) => (
    <button data-testid="workflow-inspector-missing" onClick={props.onMissingSelectedNode} />
  ),
}));

function mountDestination(
  destination: SidebarDestination,
  pageNavigator = createTestSidebarNavigator(),
) {
  render(
    <SidebarDestinationView
      destination={destination}
      navigator={pageNavigator}
    />,
  );
  return pageNavigator;
}

describe("Sidebar destination completion ownership", () => {
  beforeEach(() => {
    fixture.openWindow.mockClear();
    fixture.openProject.mockClear();
    fixture.openWorkflowEditor.mockClear();
    headerAction.mockClear();
  });

  it("deduplicates only Task Detail destinations", () => {
    expect(
      sidebarDestinationPolicy.equals(
        { kind: "taskDetail", taskID: "task-1" },
        { kind: "taskDetail", taskID: "task-1" },
      ),
    ).toBe(true);
    expect(
      sidebarDestinationPolicy.equals(
        { kind: "custom", title: "same", content: null },
        { kind: "custom", title: "same", content: null },
      ),
    ).toBe(false);
  });

  it("locks header exit only for a pending related New Task and replaces it on success", () => {
    const pageNavigator = mountDestination({
      boardQueryWorkflowID: "workflow-1",
      kind: "newTask",
      pendingRelationship: { newTaskRole: "blocker", originTaskID: "task-1" },
      projectID: "project-1",
      workflowID: "workflow-1",
    });

    fireEvent.click(screen.getByTestId("new-task-pending"));
    expect(pageNavigator.registerAvailability).toHaveBeenLastCalledWith({ back: false, close: false });
    fireEvent.click(screen.getByTestId("new-task-settled"));
    expect(pageNavigator.registerAvailability).toHaveBeenLastCalledWith({ back: true, close: true });
    fireEvent.click(screen.getByTestId("new-task-success"));
    expect(pageNavigator.replace).toHaveBeenCalledWith({ kind: "taskDetail", taskID: "task-created" });
    expect(pageNavigator.close).not.toHaveBeenCalled();
  });

  it("keeps ordinary New Task exit available while pending and scopes success/missing dismissal", () => {
    const pageNavigator = mountDestination({
      boardQueryWorkflowID: "workflow-1",
      kind: "newTask",
      projectID: "project-1",
      workflowID: "workflow-1",
    });

    fireEvent.click(screen.getByTestId("new-task-pending"));
    expect(pageNavigator.registerAvailability).toHaveBeenLastCalledWith({ back: true, close: true });
    fireEvent.click(screen.getByTestId("new-task-missing"));
    fireEvent.click(screen.getByTestId("new-task-success"));
    expect(pageNavigator.back).toHaveBeenCalledOnce();
    expect(pageNavigator.close).toHaveBeenCalledOnce();
  });

  it.each([
    {
      destination: { kind: "workflowCreate", projectID: "project-1" } satisfies SidebarDestination,
      trigger: "workflow-create-success",
      follow: () => fixture.openWorkflowEditor,
    },
    {
      destination: { kind: "linkWorkflow", creating: true, projectID: "project-1" } satisfies SidebarDestination,
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

  it("uses scoped page operations for Project-missing and Workflow Inspector missing-node", () => {
    const createNavigator = mountDestination({ kind: "workflowCreate", projectID: "project-1" });
    fireEvent.click(screen.getByTestId("workflow-create-missing"));
    expect(createNavigator.back).toHaveBeenCalledOnce();

    const inspectorNavigator = mountDestination({
      kind: "workflowInspect",
      selection: { kind: "workflow" },
      workflowID: "workflow-1",
    });
    fireEvent.click(screen.getByTestId("workflow-inspector-missing"));
    expect(inspectorNavigator.close).toHaveBeenCalledOnce();
  });

  it.each<Readonly<{ outcome: "accepted" | "stale" }>>([
    { outcome: "accepted" },
    { outcome: "stale" },
  ])("keeps pop-out completion scoped when close is $outcome", async ({ outcome }) => {
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
      expect.objectContaining({
        params: { taskID: "task-1" },
        route: "/native-dialog/task-detail",
      }),
    );
    expect(navigator.close).toHaveBeenCalledOnce();
  });
});
