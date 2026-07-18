import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import type { WorkflowDeleteImpact } from "@/api";
import { appI18n, initializeI18n } from "@/i18n";
import { useWorkflowDeleteLauncher, workflowDeleteInputFromImpact } from "@/shared/workflow-deletion";

type MatchOptions = Readonly<{
  params?: true | Readonly<{ workflowId?: string | undefined }> | undefined;
  search?: Readonly<{ workflowId?: string | undefined }> | undefined;
  to: string;
}>;

const mocks = vi.hoisted(() => {
  const state: { route: "editor" | "library" | "project" } = {
    route: "library",
  };
  return {
    api: { deleteWorkflow: vi.fn(), previewWorkflowDelete: vi.fn() },
    closeSidebar: vi.fn(),
    matchRoute: vi.fn(),
    openWorkflowLibrary: vi.fn(),
    push: vi.fn(),
    queryClient: { invalidateQueries: vi.fn(), removeQueries: vi.fn() },
    state,
  };
});

vi.mock("@tanstack/react-query", async () => ({
  ...(await vi.importActual<Record<string, unknown>>("@tanstack/react-query")),
  useQueryClient: () => mocks.queryClient,
}));
vi.mock("@tanstack/react-router", async () => ({
  ...(await vi.importActual<Record<string, unknown>>("@tanstack/react-router")),
  useMatchRoute: () => mocks.matchRoute,
}));
vi.mock("@/app-facade", async () => ({
  ...(await vi.importActual<Record<string, unknown>>("@/app-facade")),
  useAppNavigation: () => ({ openWorkflowLibrary: mocks.openWorkflowLibrary }),
  useAppServices: () => ({ api: mocks.api }),
  useConnectionSnapshot: () => ({ phase: "connected" }),
  useSidebar: () => ({ activeDestination: null, closeSidebar: mocks.closeSidebar }),
  useStatusController: () => ({ push: mocks.push }),
}));

void initializeI18n();

beforeEach(() => {
  vi.clearAllMocks();
  mocks.state.route = "library";
  mocks.api.previewWorkflowDelete.mockResolvedValue(impact);
  mocks.api.deleteWorkflow.mockResolvedValue({ blockers: [], deleted: true });
  mocks.queryClient.invalidateQueries.mockResolvedValue(undefined);
  mocks.openWorkflowLibrary.mockResolvedValue("completed");
  mocks.matchRoute.mockImplementation((options: MatchOptions) => {
    const workflowID = options.params === true ? undefined : options.params?.workflowId;
    if (options.to === "/workflows/$workflowId/editor") {
      return mocks.state.route === "editor" && workflowID === impact.workflowID ? {} : false;
    }
    if (options.to === "/projects/$projectId") return mocks.state.route === "project" && options.params === undefined &&
      options.search?.workflowId === impact.workflowID ? {} : false;
    return false;
  });
});

describe("workflow deletion launcher", () => {
  it("keeps preview and submission state owned by the current workflow", async () => {
    const preview = deferred<WorkflowDeleteImpact>();
    const deletion = deferred<unknown>();
    mocks.api.previewWorkflowDelete.mockReturnValueOnce(preview.promise);
    const view = render(<View workflowID="workflow-1" />);
    fireEvent.click(screen.getByRole("button", { name: "open twice" }));
    expect(mocks.api.previewWorkflowDelete).toHaveBeenCalledOnce();
    view.rerender(<View workflowID="workflow-2" />);
    await resolve(preview, impact);
    expect(screen.queryByRole("dialog")).toBeNull();

    mocks.api.previewWorkflowDelete.mockResolvedValueOnce({ ...impact, workflowID: "workflow-2" });
    mocks.api.deleteWorkflow.mockReturnValueOnce(deletion.promise);
    fireEvent.click(confirm(await openDialog()));
    view.rerender(<View workflowID="workflow-3" />);
    const blocker = "old owner blocked";
    await resolve(deletion, { blockers: [{ message: blocker }], deleted: false });
    mocks.api.previewWorkflowDelete.mockResolvedValueOnce({ ...impact, workflowID: "workflow-3" });
    await openDialog();
    expect(screen.queryByText(blocker)).toBeNull();
  });

  it("locks submission, preserves retry errors, and never resends a committed delete", async () => {
    const deletion = deferred<unknown>();
    mocks.api.deleteWorkflow.mockReturnValueOnce(deletion.promise);
    render(<View workflowID={impact.workflowID} />);
    const dialog = await openDialog();
    const action = confirm(dialog);
    fireEvent.click(action);
    fireEvent.click(action);
    expect(mocks.api.deleteWorkflow).toHaveBeenCalledOnce();
    expect(mocks.api.deleteWorkflow).toHaveBeenCalledWith(workflowDeleteInputFromImpact(impact));
    within(dialog).getAllByRole("button").forEach((button) => {
      expect(button).toBeDisabled();
    });
    fireEvent.keyDown(document, { key: "Escape" });

    const blocker = "A workflow task is active.";
    await resolve(deletion, { blockers: [{ message: blocker }], deleted: false });
    expect(await screen.findByText(blocker)).toBeInTheDocument();
    const apiError = new Error("delete unavailable");
    mocks.api.deleteWorkflow.mockRejectedValueOnce(apiError).mockResolvedValueOnce({ blockers: [], deleted: true });
    fireEvent.click(confirm(await dialogElement()));
    expect(await screen.findByText(apiError.message)).toBeInTheDocument();
    fireEvent.click(confirm(await dialogElement()));
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).toBeNull();
    });
  });

  it("closes and warns without success or resend when project navigation fails", async () => {
    mocks.state.route = "project";
    mocks.openWorkflowLibrary.mockResolvedValue("failed");
    render(<View workflowID={impact.workflowID} />);
    fireEvent.click(confirm(await openDialog()));
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).toBeNull();
    });
    expect(mocks.push).toHaveBeenCalledWith(
      expect.objectContaining({ id: "workflow-delete-committed-notify-error", tone: "warning" }),
    );
    expect(mocks.push).not.toHaveBeenCalledWith(expect.objectContaining({ tone: "success" }));
    fireEvent.click(screen.getByRole("button", { name: "open" }));
    expect(mocks.api.deleteWorkflow).toHaveBeenCalledOnce();
  });
});

function View({ workflowID }: Readonly<{ workflowID: string }>) {
  const launcher = useWorkflowDeleteLauncher(workflowID);
  return (
    <>
      <button disabled={launcher.disabled} onClick={() => void launcher.openWorkflowDelete()}>
        open
      </button>
      <button onClick={() => void Promise.all([launcher.openWorkflowDelete(), launcher.openWorkflowDelete()])}>
        open twice
      </button>
      {launcher.dialog}
    </>
  );
}

async function openDialog(): Promise<HTMLElement> {
  fireEvent.click(screen.getByRole("button", { name: "open" }));
  return dialogElement();
}

async function dialogElement(): Promise<HTMLElement> {
  return screen.findByRole("dialog", { name: appI18n.t("workflowEditor.workflowDeleteTitle") });
}

function confirm(dialog: HTMLElement): HTMLElement {
  return within(dialog).getByRole("button", { name: appI18n.t("workflowEditor.workflowDeleteConfirm") });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => {
    resolve = next;
  });
  return { promise, resolve };
}

async function resolve<T>(value: ReturnType<typeof deferred<T>>, result: T): Promise<void> {
  await act(async () => {
    value.resolve(result);
  });
}

const impact: WorkflowDeleteImpact = {
  activeRunCount: 0,
  blockedTaskCount: 0,
  defaultReplacementProjectCount: 0,
  linkCount: 1,
  projectCount: 1,
  runnableRunCount: 0,
  taskCount: 2,
  version: 7,
  workflowID: "workflow-1",
};
