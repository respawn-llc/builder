import { render, screen } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { I18nextProvider } from "react-i18next";

import { RpcError, rpcErrorCodes, type WorkspaceList } from "@/api";
import { NewTaskForm } from "@/features/tasks";
import { LinkWorkflowSidebar } from "@/features/workflows";
import { appI18n, initializeI18n } from "@/i18n";
import { createTestSidebarNavigator } from "@/test-support/sidebar";
const fixture = vi.hoisted<{
  createError: Error | null;
  linkError: Error | null;
  workspaces: WorkspaceList | undefined;
}>(() => ({ createError: null, linkError: null, workspaces: undefined }));

vi.mock("@tanstack/react-query", () => ({
  useMutation: () => ({
    error: fixture.linkError,
    isError: fixture.linkError !== null,
    isPending: false,
    mutateAsync: vi.fn(),
  }),
  useQuery: () => ({ data: [], error: null, isError: false, isPending: false }),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
}));
vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  queryKeys: { allBoards: [], allProjectWorkflowLinks: [], allWorkflows: [], projectWorkflowLinks: () => [] },
  useAppServices: () => ({ api: {} }),
  useConnectionSnapshot: () => ({ phase: "connected" }),
  useStatusController: () => ({ push: vi.fn() }),
  useTextFieldSubmitShortcut: () => undefined,
}));
vi.mock("@/shared/labels", () => ({
  LabelChooser: () => null,
  orderedAssignedLabels: () => [],
  ProjectLabelsProvider: ({ children }: Readonly<{ children: ReactNode }>) => <>{children}</>,
  useProjectLabelCatalog: () => ({ data: { labels: [] } }),
}));
vi.mock("@/shared/task-mutations", () => ({
  useCreateTask: () => ({ error: fixture.createError, isPending: false, mutateAsync: vi.fn() }),
  useWorkspaces: () => ({ data: fixture.workspaces, error: null }),
}));
vi.mock("@/shared/workflow-library", () => ({
  useWorkflowPages: () => ({
    data: { pages: [{ workflows: [] }] },
    hasNextPage: false,
    isError: false,
    isFetchingNextPage: false,
    isPending: false,
  }),
  WorkflowActionsContextMenu: ({ children }: Readonly<{ children: ReactNode }>) => <>{children}</>,
}));

const missing = new RpcError({ code: rpcErrorCodes.projectNotFound, message: "gone", method: "mutation" });

beforeAll(async () => initializeI18n());
beforeEach(() => Object.assign(fixture, { createError: null, linkError: null, workspaces: undefined }));

function renderWithI18n(element: ReactElement) {
  return render(<I18nextProvider i18n={appI18n}>{element}</I18nextProvider>);
}

describe("Project-missing mutation seams", () => {
  it("dismisses New Task when creation reports the Project missing", async () => {
    fixture.createError = missing;
    const onProjectMissing = vi.fn();
    renderWithI18n(
      <NewTaskForm
        boardQueryWorkflowID="workflow-1"
        onProjectMissing={onProjectMissing}
        onSubmitted={vi.fn()}
        projectID="project-1"
        workflowID="workflow-1"
      />,
    );
    expect(onProjectMissing).toHaveBeenCalledOnce();
  });

  it("backs out of Link Workflow when linking reports the Project missing", async () => {
    fixture.linkError = missing;
    const navigator = createTestSidebarNavigator();
    renderWithI18n(
      <LinkWorkflowSidebar
        creating={false}
        navigator={navigator}
        onCreated={vi.fn()}
        onLinked={vi.fn()}
        projectID="project-1"
      />,
    );
    expect(navigator.back).toHaveBeenCalledOnce();
  });
});

describe("New Task workspace selection", () => {
  it("renders Source workspace only when the Project has a workspace choice", () => {
    const sourceWorkspaceLabel = appI18n.t("task.sourceWorkspace");
    const primaryWorkspace = {
      id: "workspace-1",
      name: "kent",
      rootPath: "/workspace",
      availability: "available" as const,
      isPrimary: true,
      updatedAt: 1,
    };
    fixture.workspaces = {
      projectID: "project-1",
      defaultWorkspaceID: "workspace-1",
      nextPageToken: null,
      workspaces: [primaryWorkspace],
    };

    const singleWorkspace = renderWithI18n(
      <NewTaskForm
        boardQueryWorkflowID="workflow-1"
        onSubmitted={vi.fn()}
        projectID="project-1"
        workflowID="workflow-1"
      />,
    );

    expect(screen.queryByText(sourceWorkspaceLabel)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: sourceWorkspaceLabel })).not.toBeInTheDocument();

    singleWorkspace.unmount();
    fixture.workspaces = {
      ...fixture.workspaces,
      workspaces: [
        primaryWorkspace,
        {
          ...primaryWorkspace,
          id: "workspace-2",
          name: "docs",
          isPrimary: false,
        },
      ],
    };
    renderWithI18n(
      <NewTaskForm
        boardQueryWorkflowID="workflow-1"
        onSubmitted={vi.fn()}
        projectID="project-1"
        workflowID="workflow-1"
      />,
    );

    expect(screen.getByRole("button", { name: sourceWorkspaceLabel })).toBeEnabled();
  });
});
