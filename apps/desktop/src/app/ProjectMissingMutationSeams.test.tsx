import { render } from "@testing-library/react";
import type { ReactNode } from "react";

import { RpcError, rpcErrorCodes } from "@/api";
import { NewTaskForm } from "@/features/tasks";
import { LinkWorkflowSidebar } from "@/features/workflows";
import { createTestSidebarNavigator } from "@/test-support/sidebar";
const fixture = vi.hoisted<{ createError: Error | null; linkError: Error | null }>(() => ({ createError: null, linkError: null }));

vi.mock("@tanstack/react-query", () => ({
  useMutation: () => ({ error: fixture.linkError, isError: fixture.linkError !== null, isPending: false, mutateAsync: vi.fn() }),
  useQuery: () => ({ data: [], error: null, isError: false, isPending: false }),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
}));
vi.mock("@/app-facade", () => ({
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
  useWorkspaces: () => ({ data: undefined, error: null }),
}));
vi.mock("@/shared/workflow-library", () => ({
  useWorkflowPages: () => ({ data: { pages: [{ workflows: [] }] }, hasNextPage: false, isError: false, isFetchingNextPage: false, isPending: false }),
  WorkflowActionsContextMenu: ({ children }: Readonly<{ children: ReactNode }>) => <>{children}</>,
}));

const missing = new RpcError({ code: rpcErrorCodes.projectNotFound, message: "gone", method: "mutation" });

describe("Project-missing mutation seams", () => {
  beforeEach(() => Object.assign(fixture, { createError: null, linkError: null }));

  it("dismisses New Task when creation reports the Project missing", async () => {
    fixture.createError = missing;
    const onProjectMissing = vi.fn();
    render(<NewTaskForm boardQueryWorkflowID="workflow-1" onProjectMissing={onProjectMissing} onSubmitted={vi.fn()} projectID="project-1" workflowID="workflow-1" />);
    expect(onProjectMissing).toHaveBeenCalledOnce();
  });

  it("backs out of Link Workflow when linking reports the Project missing", async () => {
    fixture.linkError = missing;
    const navigator = createTestSidebarNavigator();
    render(<LinkWorkflowSidebar creating={false} navigator={navigator} onCreated={vi.fn()} onLinked={vi.fn()} projectID="project-1" />);
    expect(navigator.back).toHaveBeenCalledOnce();
  });
});
