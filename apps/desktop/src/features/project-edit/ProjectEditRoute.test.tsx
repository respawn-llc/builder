import { fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";

import { RpcError, type ProjectEdit, type WorkspaceCatalogPage } from "@/api";
import { ProjectEditRoute } from "./ProjectEditRoute";

interface MetadataState {
  data: ProjectEdit | undefined;
  error: Error | null;
  isError: boolean;
  isPending: boolean;
  refetch: ReturnType<typeof vi.fn>;
}

interface CatalogState {
  data: Readonly<{ pages: readonly WorkspaceCatalogPage[]; pageParams: readonly number[] }> | undefined;
  error: Error | null;
  fetchNextPage: ReturnType<typeof vi.fn>;
  hasNextPage: boolean;
  isError: boolean;
  isFetchNextPageError: boolean;
  isFetchingNextPage: boolean;
  isPending: boolean;
  refetch: ReturnType<typeof vi.fn>;
}

const statusPush = vi.hoisted(() => vi.fn());
const project = vi.hoisted(() => ({
  displayName: "Kent",
  projectID: "project-1",
  projectKey: "KNT",
}));
const metadataState = vi.hoisted((): MetadataState => ({
  data: project,
  error: null,
  isError: false,
  isPending: false,
  refetch: vi.fn(async () => undefined),
}));
const catalogState = vi.hoisted((): CatalogState => ({
  data: {
    pages: [{ projectID: "project-1", offset: 0, workspaces: [], nextOffset: null }],
    pageParams: [0],
  },
  error: null,
  fetchNextPage: vi.fn(async () => undefined),
  hasNextPage: false,
  isError: false,
  isFetchNextPageError: false,
  isFetchingNextPage: false,
  isPending: false,
  refetch: vi.fn(async () => undefined),
}));
const selectDirectory = vi.hoisted(() =>
  vi.fn<() => Promise<{ path: string } | null>>(async () => null),
);
const attachMutation = vi.hoisted(() => ({
  isPending: false,
  mutateAsync: vi.fn(),
}));
const sidebarBackWhen = vi.hoisted(() => vi.fn());

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal()),
  useInfiniteQuery: () => catalogState,
}));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppServices: () => ({
    nativeBridge: {
      capabilities: { dialogWindows: false },
      directories: { selectDirectory },
      projectWorkspace: {
        onChanged: vi.fn(async () => () => undefined),
        onUnlinkRequested: vi.fn(async () => () => undefined),
      },
    },
  }),
  useConnectionSnapshot: () => ({ phase: "connected" }),
  useNativeDialogFallback: () => ({ fallback: null, open: vi.fn(async () => undefined) }),
  usePublishSidebarHeaderAction: () => undefined,
  useSidebarBackWhen: sidebarBackWhen,
  useSidebarHeaderOffset: () => 0,
  useStatusController: () => ({ push: statusPush }),
  useTextFieldSubmitShortcut: () => vi.fn(),
  useWindowChromeTitle: () => undefined,
}));

vi.mock("./useProjectEditData", () => {
  const mutation = () => ({ isPending: false, mutateAsync: vi.fn(async () => undefined) });
  return {
    useProjectDefaultWorkspaceSave: mutation,
    useProjectEdit: () => metadataState,
    useProjectSave: mutation,
    useProjectWorkspaceAttach: () => attachMutation,
    useProjectWorkspaceChangedEvents: () => undefined,
    useProjectWorkspaceUnlink: mutation,
    useProjectWorkspaceUnlinkRequests: () => undefined,
  };
});

vi.mock("./ProjectEditParts", () => ({
  ProjectKeyField: ({
    keyDraft,
    onKeyChange,
  }: Readonly<{ keyDraft: string; onKeyChange(value: string): void }>) => (
    <input
      aria-label="project-key"
      onChange={(event) => {
        onKeyChange(event.target.value);
      }}
      value={keyDraft}
    />
  ),
  ProjectNameField: ({
    nameDraft,
    onNameChange,
  }: Readonly<{ nameDraft: string; onNameChange(value: string): void }>) => (
    <input
      aria-label="project-name"
      onChange={(event) => {
        onNameChange(event.target.value);
      }}
      value={nameDraft}
    />
  ),
  WorkspaceRow: ({
    disabled,
    onMakeDefault,
    onUnlink,
    workspace,
  }: Readonly<{
    disabled: boolean;
    onMakeDefault(): void;
    onUnlink(): void;
    workspace: { id: string };
  }>) => (
    <span>
      {workspace.id}
      <button disabled={disabled} onClick={onMakeDefault}>
        default-{workspace.id}
      </button>
      <button disabled={disabled} onClick={onUnlink}>
        unlink-{workspace.id}
      </button>
    </span>
  ),
  WorkspaceUnlinkFallbackDialog: () => null,
  workspaceUnlinkDialogWidth: 400,
}));

vi.mock("@/ui", () => ({
  Button: ({ children, ...props }: Readonly<{ children: ReactNode; [key: string]: unknown }>) => (
    <button {...props}>{children}</button>
  ),
  ErrorState: ({ onRetry, title }: Readonly<{ onRetry(): void; title: string }>) => (
    <button aria-label={`retry-${title}`} onClick={onRetry}>
      retry
    </button>
  ),
  HelpHint: () => null,
  LoadingState: () => null,
  VirtualizedInfiniteList: (
    props: Readonly<{
      empty?: ReactNode;
      header: ReactNode;
      items: readonly { workspace: { id: string } }[];
      nextBoundary?: Readonly<{ state: "error"; onRetry(): void }>;
      renderItem(item: { workspace: { id: string } }): ReactNode;
    }>,
  ) => {
    return (
      <>
        {props.header}
        {props.items.map((item, index) => (
          <span key={`${item.workspace.id}-${String(index)}`}>{props.renderItem(item)}</span>
        ))}
        {props.items.length === 0 ? props.empty : null}
        {props.nextBoundary?.state === "error" ? (
          <button onClick={props.nextBoundary.onRetry}>retry</button>
        ) : null}
      </>
    );
  },
}));

describe("ProjectEditRoute sidebar header composition", () => {
  beforeEach(() => {
    statusPush.mockClear();
    selectDirectory.mockReset();
    selectDirectory.mockResolvedValue(null);
    attachMutation.mutateAsync.mockClear();
    attachMutation.mutateAsync.mockResolvedValue({ binding: {}, outcome: "attached" });
    metadataState.data = project;
    metadataState.error = null;
    metadataState.isError = false;
    metadataState.isPending = false;
    metadataState.refetch.mockClear();
    catalogState.data = {
      pages: [{ projectID: "project-1", offset: 0, workspaces: [], nextOffset: null }],
      pageParams: [0],
    };
    catalogState.error = null;
    catalogState.hasNextPage = false;
    catalogState.isError = false;
    catalogState.isFetchNextPageError = false;
    catalogState.isFetchingNextPage = false;
    catalogState.isPending = false;
    catalogState.refetch.mockClear();
    sidebarBackWhen.mockClear();
  });

  it("keeps metadata editable and Attach available while the first catalog page owns Retry", () => {
    catalogState.data = undefined;
    catalogState.error = new Error("catalog failed");
    catalogState.isError = true;

    render(<ProjectEditRoute projectId="project-1" />);

    expect(screen.getByRole("textbox", { name: "project-name" })).toHaveValue("Kent");
    expect(screen.getByRole("button", { name: "projectEdit.attachWorkspace" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "retry-projectEdit.workspaces" }));
    expect(catalogState.refetch).toHaveBeenCalledOnce();
    expect(metadataState.refetch).not.toHaveBeenCalled();
  });

  it("keeps loaded Workspace actions while metadata owns Retry", () => {
    metadataState.data = undefined;
    metadataState.error = new Error("metadata failed");
    metadataState.isError = true;
    catalogState.data = {
      pages: [
        {
          projectID: "project-1",
          offset: 0,
          workspaces: [{ id: "workspace-1", isDefault: false, name: "One", rootPath: "/one" }],
          nextOffset: null,
        },
      ],
      pageParams: [0],
    };
    render(<ProjectEditRoute projectId="project-1" />);
    expect(screen.getByRole("button", { name: "retry-states.error" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "projectEdit.attachWorkspace" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "default-workspace-1" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "unlink-workspace-1" })).toBeEnabled();
    expect(screen.getByText("workspace-1")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "retry-states.error" }));
    expect(metadataState.refetch).toHaveBeenCalledOnce();
    expect(catalogState.refetch).not.toHaveBeenCalled();
  });

  it("hydrates metadata drafts when the pending Project read completes", () => {
    metadataState.data = undefined;
    metadataState.isPending = true;
    const view = render(<ProjectEditRoute projectId="project-1" />);

    metadataState.data = project;
    metadataState.isPending = false;
    view.rerender(<ProjectEditRoute projectId="project-1" />);

    expect(screen.getByRole("textbox", { name: "project-name" })).toHaveValue("Kent");
    expect(screen.getByRole("textbox", { name: "project-key" })).toHaveValue("KNT");
  });

  it("preserves edited metadata drafts across later query refreshes", () => {
    const view = render(<ProjectEditRoute projectId="project-1" />);
    fireEvent.change(screen.getByRole("textbox", { name: "project-name" }), {
      target: { value: "Kent Desktop" },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "project-key" }), {
      target: { value: "KTD" },
    });

    metadataState.data = { ...project, displayName: "Kent refreshed", projectKey: "NEW" };
    view.rerender(<ProjectEditRoute projectId="project-1" />);

    expect(screen.getByRole("textbox", { name: "project-name" })).toHaveValue("Kent Desktop");
    expect(screen.getByRole("textbox", { name: "project-key" })).toHaveValue("KTD");
  });

  it("treats catalog missing Project as Back", () => {
    catalogState.error = new RpcError({ code: -32014, message: "gone", method: "project.workspace.list" });
    catalogState.isError = true;
    const navigator = {
      back: vi.fn(() => "accepted" as const),
      close: vi.fn(() => "accepted" as const),
      push: vi.fn(() => "accepted" as const),
      replace: vi.fn(() => "accepted" as const),
      registerAvailability: vi.fn(() => () => undefined),
      registerCapture: vi.fn(() => () => undefined),
    };
    render(<ProjectEditRoute navigator={navigator} projectId="project-1" />);
    expect(sidebarBackWhen).toHaveBeenCalledWith(true, navigator);
  });

  it("retains edge-failed rows and overlapping occurrence identity", () => {
    catalogState.data = {
      pages: [
        {
          projectID: "project-1",
          offset: 0,
          workspaces: [{ id: "same", isDefault: true, name: "Same", rootPath: "/same" }],
          nextOffset: 100,
        },
        {
          projectID: "project-1",
          offset: 100,
          workspaces: [{ id: "same", isDefault: false, name: "Same", rootPath: "/same" }],
          nextOffset: null,
        },
      ],
      pageParams: [0, 100],
    };
    catalogState.error = new Error("edge");
    catalogState.hasNextPage = true;
    catalogState.isFetchNextPageError = true;
    render(<ProjectEditRoute projectId="project-1" />);
    expect(screen.getAllByText("same")).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "retry" }));
    expect(catalogState.fetchNextPage).toHaveBeenCalledOnce();
  });

  it("always sends Attach and reports already-attached without changing retained rows", async () => {
    catalogState.data = {
      pages: [
        {
          projectID: "project-1",
          offset: 0,
          workspaces: [{ id: "retained", isDefault: true, name: "Retained", rootPath: "/retained" }],
          nextOffset: 100,
        },
      ],
      pageParams: [0],
    };
    selectDirectory.mockResolvedValue({ path: "/buried" });
    attachMutation.mutateAsync.mockResolvedValue({
      binding: { projectID: "project-1", workspaceID: "buried" },
      outcome: "already_attached",
    });
    render(<ProjectEditRoute projectId="project-1" />);

    fireEvent.click(screen.getByRole("button", { name: "projectEdit.attachWorkspace" }));
    await vi.waitFor(() => {
      expect(attachMutation.mutateAsync).toHaveBeenCalledWith("/buried");
    });

    expect(statusPush).toHaveBeenCalledWith(
      expect.objectContaining({
        body: "projectEdit.workspaceAlreadyLinked",
        tone: "success",
      }),
    );
  });
});
