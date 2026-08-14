import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentProps, ReactNode } from "react";

import {
  RpcError,
  rpcErrorCodes,
  type TaskDependencyDirection,
  type WorkspaceCatalogPage,
  type WorkspaceCatalogRow,
} from "@/api";
import type { TaskSearchResult } from "@/app-facade";
import type { PreparedTaskDependency } from "@/shared/task-dependencies";
import { createTestSidebarNavigator } from "@/test-support/sidebar";
import type { SelectFieldPaging } from "@/ui";
import type * as UiModule from "@/ui";

interface TestSelectProps {
  onValueChange(value: string): void;
  options: readonly { label: ReactNode; value: string }[];
  paging?: SelectFieldPaging;
  value: string | undefined;
  disabled?: boolean;
}

interface TestState {
  catalog: {
    data: { pages: readonly WorkspaceCatalogPage[]; pageParams: readonly number[] } | undefined;
    error: Error | null;
    fetchNextPage: ReturnType<typeof vi.fn>;
    fetchPreviousPage: ReturnType<typeof vi.fn>;
    hasNextPage: boolean;
    isError: boolean;
    isFetchNextPageError: boolean;
    isFetchingNextPage: boolean;
    isFetchingPreviousPage: boolean;
    isPending: boolean;
    refetch: ReturnType<typeof vi.fn>;
  };
  exact: {
    data: { kind: "attached"; workspace: WorkspaceCatalogRow } | { kind: "not_attached" } | undefined;
    error: Error | null;
    isError: boolean;
    isPending: boolean;
    refetch: ReturnType<typeof vi.fn>;
  };
  select: TestSelectProps | undefined;
  create: ReturnType<typeof vi.fn>;
  loggerAppend: ReturnType<typeof vi.fn>;
  resetQueries: ReturnType<typeof vi.fn>;
  searchResults: readonly TaskSearchResult[];
  statusPush: ReturnType<typeof vi.fn>;
}

const state = vi.hoisted((): TestState => ({
  catalog: {
    data: undefined,
    error: null,
    fetchNextPage: vi.fn(async () => undefined),
    fetchPreviousPage: vi.fn(async () => undefined),
    hasNextPage: false,
    isError: false,
    isFetchNextPageError: false,
    isFetchingNextPage: false,
    isFetchingPreviousPage: false,
    isPending: true,
    refetch: vi.fn(async () => undefined),
  },
  exact: {
    data: undefined,
    error: null,
    isError: false,
    isPending: true,
    refetch: vi.fn(async () => undefined),
  },
  select: undefined,
  create: vi.fn(async () => ({
    id: "task-created",
    shortID: "KENT-42",
    title: "Task",
    workflowID: "workflow-1",
  })),
  loggerAppend: vi.fn(async () => undefined),
  resetQueries: vi.fn(async () => undefined),
  searchResults: [],
  statusPush: vi.fn(),
}));

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal()),
  useInfiniteQuery: () => state.catalog,
  useQuery: () => state.exact,
  useQueryClient: () => ({ resetQueries: state.resetQueries }),
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("@/app-facade", () => ({
  projectWorkspaceQueryOptions: () => ({}),
  queryKeys: {
    projectWorkspaceCatalog: (projectID: string) => ["project-catalog", projectID, "workspaces"],
  },
  taskSearchDebounceMs: 0,
  useDebouncedText: (value: string) => value,
  useAppServices: () => ({ api: {}, logger: { append: state.loggerAppend } }),
  useConnectionSnapshot: () => ({ phase: "connected" }),
  useStatusController: () => ({ dismiss: state.statusPush, push: state.statusPush }),
  useTaskSearch: () => ({
    displayedQuery: null,
    normalizedTooShort: false,
    paginationUsesVisibleData: true,
    request: {
      data: undefined,
      error: null,
      fetchNextPage: vi.fn(),
      hasNextPage: false,
      isError: false,
      isFetchNextPageError: false,
      isFetching: false,
      isFetchingNextPage: false,
      refetch: vi.fn(),
    },
    results: state.searchResults,
    searchable: state.searchResults.length > 0,
  }),
  useTextFieldSubmitShortcut: () => undefined,
  workspaceCatalogInfiniteQueryOptions: () => ({}),
}));
vi.mock("@/shared/labels", () => ({
  LabelChooser: () => null,
  ProjectLabelsProvider: ({ children }: Readonly<{ children: ReactNode }>) => <>{children}</>,
  orderedAssignedLabels: () => [],
  useProjectLabelCatalog: () => ({ data: { labels: [] } }),
}));
vi.mock("@/shared/native-dialog", () => ({
  NativeDialogWindow: ({ children }: Readonly<{ children: ReactNode }>) => <>{children}</>,
}));
vi.mock("@/shared/task-mutations", () => ({
  useCreateTask: () => ({ error: null, isPending: false, mutateAsync: state.create }),
}));
vi.mock("@/ui", async (importOriginal) => ({
  ...(await importOriginal<typeof UiModule>()),
  Badge: ({ children }: Readonly<{ children: ReactNode }>) => <>{children}</>,
  Button: ({ children, ...props }: ComponentProps<"button">) => <button {...props}>{children}</button>,
  Dialog: ({ children }: Readonly<{ children: ReactNode }>) => <>{children}</>,
  FieldShell: ({ children }: Readonly<{ children: ReactNode }>) => <>{children}</>,
  InfiniteListBoundary: ({
    state: boundary,
  }: {
    state: { state: string; onRetry?: (() => void) | undefined };
  }) => {
    const { onRetry } = boundary;
    return boundary.state === "error" ? <button onClick={onRetry}>exact-retry</button> : null;
  },
  LabelChooser: () => null,
  SelectField: (props: TestSelectProps) => {
    state.select = props;
    return (
      <>
        {props.options.map((option) => (
          <button
            disabled={props.disabled}
            key={option.value}
            onClick={() => {
              props.onValueChange(option.value);
            }}
          >
            {option.label}
          </button>
        ))}
        {props.paging?.nextBoundary?.state === "error" ? (
          <button onClick={props.paging.nextBoundary.onRetry}>edge-retry</button>
        ) : null}
      </>
    );
  },
  TextArea: (props: ComponentProps<"textarea"> & { label: string }) => (
    <textarea aria-label={props.label} {...props} />
  ),
  TextInput: ({ error, label, ...props }: ComponentProps<"input"> & { error?: ReactNode; label: string }) => (
    <>
      <input aria-label={label} {...props} />
      {error}
    </>
  ),
  cx: (...values: readonly (string | undefined)[]) => values.filter(Boolean).join(" "),
}));

import { NewTaskForm } from "./NewTaskDialog";

const row = (id: string, isDefault = false): WorkspaceCatalogRow => ({
  id,
  isDefault,
  name: id,
  rootPath: `/${id}`,
});
const page = (workspaces = [row("default", true), row("other")]): WorkspaceCatalogPage => ({
  projectID: "project-1",
  offset: 0,
  workspaces,
  nextOffset: 100,
});
const props = {
  boardQueryWorkflowID: "workflow-1",
  initialSourceWorkspaceID: "source",
  navigator: createTestSidebarNavigator(),
  projectID: "project-1",
  workflowID: "workflow-1",
};
function loadCatalog(workspaces?: WorkspaceCatalogRow[]) {
  state.catalog.data = { pages: [page(workspaces)], pageParams: [0] };
  state.catalog.isError = state.catalog.isPending = false;
}
function rerender(view: ReturnType<typeof render>) {
  view.rerender(<NewTaskForm {...props} />);
}

describe("New Task Workspace catalog integration", () => {
  beforeEach(() => {
    Object.assign(state.catalog, {
      data: undefined,
      error: null,
      hasNextPage: false,
      isError: false,
      isFetchNextPageError: false,
      isFetchingNextPage: false,
      isPending: true,
    });
    Object.assign(state.exact, { data: undefined, error: null, isError: false, isPending: true });
    state.catalog.fetchNextPage.mockClear();
    state.catalog.fetchPreviousPage.mockClear();
    state.catalog.refetch.mockClear();
    state.exact.refetch.mockClear();
    state.create.mockClear();
    state.loggerAppend.mockClear();
    state.resetQueries.mockClear();
    state.searchResults = [];
    state.statusPush.mockClear();
    state.select = undefined;
  });

  it.each(["catalog-first", "exact-first"])(
    "selects the initiating Workspace for %s response order",
    (order) => {
      if (order === "catalog-first") loadCatalog();
      else {
        state.exact.data = { kind: "attached", workspace: row("source") };
        state.exact.isPending = false;
      }
      const view = render(<NewTaskForm {...props} />);
      if (order === "catalog-first") {
        state.exact.data = { kind: "attached", workspace: row("source") };
        state.exact.isPending = false;
      } else loadCatalog();
      rerender(view);
      expect(state.select?.value).toBe("source");
    },
  );

  it.each(["catalog-first", "exact-first"])(
    "selects the default only after detached initiating and catalog results arrive %s",
    (order) => {
      if (order === "catalog-first") loadCatalog();
      else {
        state.exact.data = { kind: "not_attached" };
        state.exact.isPending = false;
      }
      const view = render(<NewTaskForm {...props} />);
      expect(state.select?.value).toBeUndefined();

      if (order === "catalog-first") {
        state.exact.data = { kind: "not_attached" };
        state.exact.isPending = false;
      } else loadCatalog();
      rerender(view);

      expect(state.select?.value).toBe("default");
    },
  );

  it("preserves Task drafts when a late initiating Workspace result commits selection", () => {
    loadCatalog();
    const view = render(<NewTaskForm {...props} />);
    fireEvent.change(screen.getByRole("textbox", { name: "task.name" }), {
      target: { value: "Draft title" },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "task.body" }), {
      target: { value: "Draft body" },
    });
    expect(state.select?.value).toBeUndefined();

    state.exact.data = { kind: "attached", workspace: row("source") };
    state.exact.isPending = false;
    rerender(view);

    expect(state.select?.value).toBe("source");
    expect(screen.getByRole("textbox", { name: "task.name" })).toHaveValue("Draft title");
    expect(screen.getByRole("textbox", { name: "task.body" })).toHaveValue("Draft body");
    expect(screen.getByRole("button", { name: "task.create" })).toBeEnabled();
  });

  it("restarts once from the default-first page when the retained catalog window starts after zero", () => {
    state.catalog.data = {
      pages: [
        {
          projectID: "project-1",
          offset: 200,
          workspaces: [row("retained")],
          nextOffset: 300,
        },
      ],
      pageParams: [200],
    };
    state.catalog.isPending = false;
    state.exact.data = { kind: "not_attached" };
    state.exact.isPending = false;

    render(<NewTaskForm {...props} />);

    expect(state.resetQueries).toHaveBeenCalledWith({
      exact: true,
      queryKey: ["project-catalog", "project-1", "workspaces"],
    });
    expect(state.select?.value).toBeUndefined();
    expect(screen.getByRole("button", { name: "task.create" })).toBeDisabled();
  });

  it("keeps attached exact selection usable through first-page failure and Retry", () => {
    state.exact.data = { kind: "attached", workspace: row("source") };
    state.exact.isPending = false;
    Object.assign(state.catalog, { error: new Error("catalog"), isError: true, isPending: false });
    render(<NewTaskForm {...props} />);
    expect(state.select?.value).toBe("source");
    const initialBoundary = state.select?.paging?.initialBoundary;
    if (initialBoundary?.state === "error") {
      initialBoundary.onRetry();
    }
    expect(state.catalog.refetch).toHaveBeenCalledOnce();
  });

  it("retries exact failure without fallback and never overwrites an explicit choice", () => {
    loadCatalog();
    Object.assign(state.exact, { error: new Error("exact"), isError: true, isPending: false });
    const view = render(<NewTaskForm {...props} />);
    expect(state.select?.value).toBeUndefined();
    fireEvent.click(screen.getByRole("button", { name: "exact-retry" }));
    expect(state.exact.refetch).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("button", { name: "other" }));
    state.exact.data = { kind: "attached", workspace: row("source") };
    state.exact.isError = false;
    rerender(view);
    expect(state.select?.value).toBe("other");
  });

  it("keeps one loaded Workspace selectable while the exact read remains failed", () => {
    loadCatalog([row("only", true)]);
    Object.assign(state.exact, { error: new Error("exact"), isError: true, isPending: false });
    render(<NewTaskForm {...props} />);

    expect(state.select?.disabled).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "only" }));

    expect(state.select?.value).toBe("only");
    expect(screen.getByRole("button", { name: "task.create" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "exact-retry" })).toBeInTheDocument();
  });

  it("pins initiating and evicted selected rows once while fresh loaded rows replace snapshots", () => {
    loadCatalog([row("selected")]);
    state.exact.data = { kind: "attached", workspace: row("source") };
    state.exact.isPending = false;
    const view = render(<NewTaskForm {...props} />);
    fireEvent.click(screen.getByRole("button", { name: "selected" }));
    state.catalog.data = {
      pages: [{ projectID: "project-1", offset: 400, workspaces: [row("retained")], nextOffset: null }],
      pageParams: [400],
    };
    rerender(view);
    expect(state.select?.options.map(({ value }) => value)).toEqual(["source", "selected", "retained"]);
  });

  it("falls back only after typed detachment and retries a failed page edge", () => {
    loadCatalog();
    state.catalog.hasNextPage = state.catalog.isFetchNextPageError = true;
    state.exact.data = { kind: "not_attached" };
    state.exact.isPending = false;
    render(<NewTaskForm {...props} />);
    expect(state.select?.value).toBe("default");
    fireEvent.click(screen.getByRole("button", { name: "edge-retry" }));
    expect(state.catalog.fetchNextPage).toHaveBeenCalledOnce();
  });

  it("propagates missing Project and submits the displayed Workspace identity", async () => {
    loadCatalog();
    state.exact.data = { kind: "not_attached" };
    state.exact.isPending = false;
    const navigator = createTestSidebarNavigator();
    const view = render(<NewTaskForm {...props} />);
    fireEvent.click(screen.getByRole("button", { name: "other" }));
    fireEvent.change(screen.getByRole("textbox", { name: "task.name" }), { target: { value: "Task" } });
    fireEvent.click(screen.getByRole("button", { name: "task.create" }));
    await vi.waitFor(() => {
      expect(state.create).toHaveBeenCalledWith(expect.objectContaining({ sourceWorkspaceID: "other" }));
    });
    view.unmount();
    state.exact.error = new RpcError({
      code: rpcErrorCodes.projectNotFound,
      message: "gone",
      method: "project.workspace.get",
    });
    state.exact.data = undefined;
    state.exact.isError = true;
    render(<NewTaskForm {...props} navigator={navigator} />);
    await vi.waitFor(() => {
      expect(navigator.back).toHaveBeenCalled();
    });
  });

  it("shows ordinary and Task Detail-originated prepared dependencies and delegates their actions", () => {
    loadCatalog();
    state.exact.data = { kind: "attached", workspace: row("source") };
    state.exact.isPending = false;
    const view = render(<NewTaskForm {...props} />);
    expect(screen.getByText("task.dependencies")).toBeInTheDocument();
    expect(screen.queryByTestId(/^dependency-row-/)).not.toBeInTheDocument();
    view.unmount();

    const navigator = createTestSidebarNavigator();
    render(
      <NewTaskForm
        {...props}
        initialPreparedDependency={preparedDependency("blocks", "task-origin")}
        navigator={navigator}
      />,
    );
    fireEvent.click(screen.getByTestId("dependency-row-task-origin"));
    expect(navigator.push).toHaveBeenCalledWith({ kind: "taskDetail", taskID: "task-origin" });
    fireEvent.click(screen.getByTestId("dependency-remove-task-origin"));
    expect(screen.queryByTestId("dependency-row-task-origin")).not.toBeInTheDocument();
  });

  it("pushes a stacked child without an unsaved-parent relationship and captures the complete Draft", () => {
    loadCatalog();
    state.exact.data = { kind: "attached", workspace: row("source") };
    state.exact.isPending = false;
    const navigator = createTestSidebarNavigator();
    render(<NewTaskForm {...props} navigator={navigator} />);
    fireEvent.change(screen.getByRole("textbox", { name: "task.name" }), {
      target: { value: "Parent title" },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "task.body" }), {
      target: { value: "Parent body" },
    });
    fireEvent.click(screen.getByTestId("dependency-add-blocks"));
    fireEvent.click(screen.getByRole("button", { name: "task.dependenciesCreateTask" }));

    expect(navigator.push).toHaveBeenCalledWith({
      boardQueryWorkflowID: "workflow-1",
      initialSourceWorkspaceID: "source",
      kind: "newTask",
      parentReturnDirection: "blocks",
      projectID: "project-1",
      workflowID: "workflow-1",
    });
    const capture = vi.mocked(navigator.registerCapture).mock.lastCall?.[0];
    if (capture === undefined) throw new Error("Expected New Task Draft capture.");
    expect(capture()).toEqual({
      formValues: {
        body: "Parent body",
        sourceWorkspaceID: "source",
        title: "Parent title",
      },
      preparedDependencies: [],
      selectedLabelIDs: [],
    });
  });

  it("restores the authored Draft with picker state closed and recomputes validation on submission", async () => {
    loadCatalog();
    state.exact.data = { kind: "attached", workspace: row("source") };
    state.exact.isPending = false;
    const retainedState = {
      formValues: { body: "Body", sourceWorkspaceID: "source", title: "" },
      preparedDependencies: [preparedDependency("blocked-by", "task-restored")],
      selectedLabelIDs: [],
    };
    render(<NewTaskForm {...props} retainedState={retainedState} />);
    expect(screen.getByRole("textbox", { name: "task.name" })).toHaveValue("");
    expect(screen.getByRole("textbox", { name: "task.body" })).toHaveValue("Body");
    expect(screen.getByTestId("dependency-row-task-restored")).toBeInTheDocument();
    expect(screen.queryByText("form.required")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "task.create" }));
    expect(await screen.findByText("form.required")).toBeInTheDocument();
  });

  it("submits every prepared relationship atomically and returns through one Back operation", async () => {
    loadCatalog();
    state.exact.data = { kind: "attached", workspace: row("source") };
    state.exact.isPending = false;
    const navigator = createTestSidebarNavigator();
    render(
      <NewTaskForm
        {...props}
        navigator={navigator}
        retainedState={{
          formValues: { body: "", sourceWorkspaceID: "source", title: "" },
          preparedDependencies: [
            preparedDependency("blocks", "task-origin"),
            preparedDependency("blocked-by", "task-blocked"),
          ],
          selectedLabelIDs: [],
        }}
      />,
    );
    fireEvent.change(screen.getByRole("textbox", { name: "task.name" }), {
      target: { value: "Task" },
    });
    fireEvent.click(screen.getByRole("button", { name: "task.create" }));
    await vi.waitFor(() => {
      expect(state.create).toHaveBeenCalledWith(
        expect.objectContaining({
          dependencyIntents: [
            { relatedTaskID: "task-origin", newTaskRole: "blocker" },
            { relatedTaskID: "task-blocked", newTaskRole: "blocked" },
          ],
        }),
      );
    });
    expect(navigator.back).toHaveBeenCalledOnce();
    expect(navigator.close).not.toHaveBeenCalled();
    expect(navigator.replace).not.toHaveBeenCalled();
  });

  it("stages searched dependencies through the 49→50 limit and cannot select a 51st", async () => {
    loadCatalog();
    state.exact.data = { kind: "attached", workspace: row("source") };
    state.exact.isPending = false;
    state.searchResults = [candidate("task-49"), candidate("task-50"), candidate("task-51")];
    const user = userEvent.setup();
    render(
      <NewTaskForm
        {...props}
        retainedState={{
          formValues: { body: "", sourceWorkspaceID: "source", title: "" },
          preparedDependencies: Array.from({ length: 48 }, (_, index) =>
            preparedDependency("blocked-by", `task-${String(index)}`),
          ),
          selectedLabelIDs: [],
        }}
      />,
    );
    await user.click(screen.getByTestId("dependency-add-blocked-by"));
    await user.click(screen.getByTestId("dependency-candidate-task-49"));
    expect(screen.getByTestId("dependency-candidate-task-50")).toBeInTheDocument();
    await user.click(screen.getByTestId("dependency-candidate-task-50"));
    expect(screen.getByTestId("dependency-row-task-49")).toBeInTheDocument();
    expect(screen.getByTestId("dependency-row-task-50")).toBeInTheDocument();
    expect(screen.queryByTestId("dependency-candidate-task-51")).not.toBeInTheDocument();
    expect(screen.getByTestId("dependency-add-blocked-by")).toBeDisabled();
  });

  it("returns an active stacked child summary with backlog status and keeps Back available while pending", async () => {
    loadCatalog();
    state.exact.data = { kind: "attached", workspace: row("source") };
    state.exact.isPending = false;
    const navigator = createTestSidebarNavigator();
    render(<NewTaskForm {...props} navigator={navigator} parentReturnDirection="blocked-by" />);
    fireEvent.change(screen.getByRole("textbox", { name: "task.name" }), {
      target: { value: "Child" },
    });
    fireEvent.click(screen.getByRole("button", { name: "task.create" }));

    await vi.waitFor(() => {
      expect(navigator.back).toHaveBeenCalledWith({
        kind: "newTaskCreated",
        direction: "blocked-by",
        task: {
          id: "task-created",
          shortID: "KENT-42",
          status: {
            kind: "backlog",
            nativeState: "active",
            nodeIDs: [],
            attentionTypes: [],
          },
          title: "Task",
          workflowID: "workflow-1",
        },
      });
    });
  });

  it("keeps the Draft and emits one Sonner failure when atomic creation reports Project missing", async () => {
    loadCatalog();
    state.exact.data = { kind: "attached", workspace: row("source") };
    state.exact.isPending = false;
    state.create.mockRejectedValueOnce(
      new RpcError({
        code: rpcErrorCodes.projectNotFound,
        message: "Project no longer exists",
        method: "workflow.task.create",
      }),
    );
    const navigator = createTestSidebarNavigator();
    render(<NewTaskForm {...props} navigator={navigator} />);
    fireEvent.change(screen.getByRole("textbox", { name: "task.name" }), {
      target: { value: "Keep me" },
    });
    fireEvent.click(screen.getByRole("button", { name: "task.create" }));

    await vi.waitFor(() => {
      expect(state.statusPush).toHaveBeenCalledOnce();
    });
    expect(screen.getByRole("textbox", { name: "task.name" })).toHaveValue("Keep me");
    expect(navigator.back).not.toHaveBeenCalled();
    expect(screen.queryByText("Project no longer exists")).not.toBeInTheDocument();
  });

  it("presents reciprocal rejection as product copy and preserves authored dependencies", async () => {
    loadCatalog();
    state.exact.data = { kind: "attached", workspace: row("source") };
    state.exact.isPending = false;
    state.searchResults = [candidate("task-related")];
    state.create.mockRejectedValueOnce(
      new RpcError({
        code: rpcErrorCodes.workflowTaskDependency,
        message: "workflow task dependency error: reciprocal_dependency",
        method: "workflow.task.create",
        data: {
          type: "workflow_task_dependency_error",
          reason: "reciprocal_dependency",
          blocker_task_id: "task-created",
          blocked_task_id: "task-related",
        },
      }),
    );
    const user = userEvent.setup();
    render(
      <NewTaskForm
        {...props}
        retainedState={{
          formValues: { body: "Keep body", sourceWorkspaceID: "source", title: "Keep title" },
          preparedDependencies: [preparedDependency("blocks", "task-related")],
          selectedLabelIDs: [],
        }}
      />,
    );
    const addDependency = screen.getByTestId("dependency-add-blocked-by");
    await user.click(addDependency);
    await user.click(screen.getByTestId("dependency-candidate-task-related"));
    fireEvent.submit(addDependency);
    await vi.waitFor(() => {
      expect(state.statusPush).toHaveBeenCalledOnce();
    });
    expect(state.loggerAppend.mock.lastCall?.[2]).toHaveProperty("error");
    expect(state.loggerAppend.mock.lastCall?.[2]).toHaveProperty("reason", "reciprocal_dependency");
    expect(screen.getAllByTestId("dependency-row-task-related")).toHaveLength(2);
    const [removeDependency] = screen.getAllByTestId("dependency-remove-task-related");
    if (removeDependency === undefined) throw new Error("Expected a removable prepared dependency.");
    await user.click(removeDependency);
    fireEvent.submit(addDependency);
    await vi.waitFor(() => {
      expect(state.statusPush).toHaveBeenLastCalledWith("new-task-create-error");
    });
  });
});

function preparedDependency(direction: TaskDependencyDirection, taskID: string): PreparedTaskDependency {
  return {
    direction,
    taskID,
    shortID: taskID,
    title: taskID,
    workflowID: "workflow-1",
    status: {
      kind: "backlog",
      nativeState: "active",
      nodeIDs: [],
      attentionTypes: [],
    },
  };
}

function candidate(taskID: string): TaskSearchResult {
  const dependency = preparedDependency("blocked-by", taskID);
  return {
    key: taskID,
    group: {
      projectID: "project-1",
      projectKey: "KENT",
      totalHitCount: 1,
      hits: [
        {
          ordinal: 1,
          source: { kind: "title" },
          literal: { before: "", match: taskID, after: "", leftTruncated: false, rightTruncated: false },
        },
      ],
      ...dependency,
    },
  };
}
