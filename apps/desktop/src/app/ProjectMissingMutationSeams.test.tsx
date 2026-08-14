import { render } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { I18nextProvider } from "react-i18next";

import { RpcError, rpcErrorCodes } from "@/api";
import { NewTaskForm } from "@/features/tasks";
import { LinkWorkflowSidebar } from "@/features/workflows";
import { appI18n, initializeI18n } from "@/i18n";
import type * as TaskDependenciesModule from "@/shared/task-dependencies";
import { createTestSidebarNavigator } from "@/test-support/sidebar";
const fixture = vi.hoisted<{
  createError: Error | null;
  linkError: Error | null;
}>(() => ({ createError: null, linkError: null }));

vi.mock("@tanstack/react-query", () => ({
  useMutation: () => ({
    error: fixture.linkError,
    isError: fixture.linkError !== null,
    isPending: false,
    mutateAsync: vi.fn(),
  }),
  useInfiniteQuery: () => ({
    data: undefined,
    error: null,
    hasNextPage: false,
    isError: false,
    isFetchingNextPage: false,
    isPending: false,
    fetchNextPage: vi.fn(),
    refetch: vi.fn(),
  }),
  useQuery: () => ({ data: undefined, error: null, isError: false, isPending: false, refetch: vi.fn() }),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
}));
vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  queryKeys: { allBoards: [], allProjectWorkflowLinks: [], allWorkflows: [], projectWorkflowLinks: () => [] },
  useAppServices: () => ({ api: {} }),
  useConnectionSnapshot: () => ({ phase: "connected" }),
  projectWorkspaceQueryOptions: () => ({}),
  useStatusController: () => ({ push: vi.fn() }),
  useTextFieldSubmitShortcut: () => undefined,
  workspaceCatalogInfiniteQueryOptions: () => ({}),
}));
vi.mock("@/shared/labels", () => ({
  LabelChooser: () => null,
  orderedAssignedLabels: () => [],
  ProjectLabelsProvider: ({ children }: Readonly<{ children: ReactNode }>) => <>{children}</>,
  useProjectLabelCatalog: () => ({ data: { labels: [] } }),
}));
vi.mock("@/shared/task-mutations", () => ({
  useCreateTask: () => ({ error: fixture.createError, isPending: false, mutateAsync: vi.fn() }),
}));
vi.mock("@/shared/task-dependencies", async (importOriginal) => ({
  ...(await importOriginal<typeof TaskDependenciesModule>()),
  DependenciesArea: () => null,
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
beforeEach(() => Object.assign(fixture, { createError: null, linkError: null }));

function renderWithI18n(element: ReactElement) {
  return render(<I18nextProvider i18n={appI18n}>{element}</I18nextProvider>);
}

describe("Project-missing mutation seams", () => {
  it("dismisses New Task when creation reports the Project missing", async () => {
    fixture.createError = missing;
    const navigator = createTestSidebarNavigator();
    renderWithI18n(
      <NewTaskForm
        boardQueryWorkflowID="workflow-1"
        navigator={navigator}
        projectID="project-1"
        workflowID="workflow-1"
      />,
    );
    expect(navigator.back).toHaveBeenCalledOnce();
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
