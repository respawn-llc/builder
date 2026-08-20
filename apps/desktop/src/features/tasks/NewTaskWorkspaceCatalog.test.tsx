import { fireEvent, render, screen } from "@testing-library/react";
import type { ComponentProps, ReactNode } from "react";

import { RpcError, rpcErrorCodes, type WorkspaceCatalogPage, type WorkspaceCatalogRow } from "@/api";
import type { SelectFieldPaging } from "@/ui";

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
  resetQueries: ReturnType<typeof vi.fn>;
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
  create: vi.fn(async () => "task-created"),
  resetQueries: vi.fn(async () => undefined),
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
  useAppServices: () => ({ api: {} }),
  useConnectionSnapshot: () => ({ phase: "connected" }),
  useStatusController: () => ({ push: vi.fn() }),
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
vi.mock("@/ui", () => ({
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
  TextInput: (props: ComponentProps<"input"> & { label: string }) => (
    <input aria-label={props.label} {...props} />
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
  onSubmitted: vi.fn(),
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
    state.resetQueries.mockClear();
    state.select = undefined;
    props.onSubmitted.mockClear();
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
    const onProjectMissing = vi.fn();
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
    render(<NewTaskForm {...props} onProjectMissing={onProjectMissing} />);
    await vi.waitFor(() => {
      expect(onProjectMissing).toHaveBeenCalled();
    });
  });
});
