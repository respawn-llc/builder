import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";

import { RpcError, rpcErrorCodes, type WorkspaceList } from "@/api";
import { NewTaskForm } from "@/features/tasks";
import { LinkWorkflowSidebar } from "@/features/workflows";
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

beforeEach(() => Object.assign(fixture, { createError: null, linkError: null, workspaces: undefined }));

describe("Project-missing mutation seams", () => {
  it("dismisses New Task when creation reports the Project missing", async () => {
    fixture.createError = missing;
    const onProjectMissing = vi.fn();
    render(
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
    render(
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
  it("hides Source workspace when the Project has only one workspace", () => {
    fixture.workspaces = {
      projectID: "project-1",
      defaultWorkspaceID: "workspace-1",
      nextPageToken: null,
      workspaces: [
        {
          id: "workspace-1",
          name: "kent",
          rootPath: "/workspace",
          availability: "available",
          isPrimary: true,
          updatedAt: 1,
        },
      ],
    };

    const { container } = render(
      <NewTaskForm
        boardQueryWorkflowID="workflow-1"
        onSubmitted={vi.fn()}
        projectID="project-1"
        workflowID="workflow-1"
      />,
    );

    expect(screen.queryByText("task.sourceWorkspace")).not.toBeInTheDocument();
    expect(container.querySelector('[data-slot="select-trigger"]')).not.toBeInTheDocument();
  });
});
