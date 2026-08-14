import { fireEvent, render, screen } from "@testing-library/react";
import { isValidElement, type ReactNode } from "react";

import { ProjectEditRoute } from "./ProjectEditRoute";

const headerAction = vi.hoisted(() => vi.fn<(action: ReactNode) => void>());
const catalogQuery = vi.hoisted(() => ({
  current: {
    data: {
      pageParams: [null],
      pages: [
        {
          defaultWorkspaceID: "workspace-1",
          nextPageToken: null,
          projectID: "project-1",
          workspaces: [],
        },
      ],
    } as
      | {
          pageParams: null[];
          pages: {
            defaultWorkspaceID: string;
            nextPageToken: null;
            projectID: string;
            workspaces: never[];
          }[];
        }
      | undefined,
    error: null as unknown,
    fetchNextPage: vi.fn(async () => undefined),
    fetchPreviousPage: vi.fn(async () => undefined),
    hasNextPage: false,
    hasPreviousPage: false,
    isError: false,
    isFetchNextPageError: false,
    isFetchPreviousPageError: false,
    isFetchingNextPage: false,
    isFetchingPreviousPage: false,
    isPending: false,
    refetch: vi.fn(async () => undefined),
  },
}));
const project = vi.hoisted(() => ({
  displayName: "Kent",
  projectID: "project-1",
  projectKey: "KNT",
}));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppServices: () => ({
    api: {},
    nativeBridge: {
      capabilities: { dialogWindows: false },
      directories: { selectDirectory: vi.fn(async () => null) },
      projectWorkspace: {
        onChanged: vi.fn(async () => () => undefined),
        onUnlinkRequested: vi.fn(async () => () => undefined),
      },
    },
  }),
  useConnectionSnapshot: () => ({ phase: "connected" }),
  useNativeDialogFallback: () => ({ fallback: null, open: vi.fn(async () => undefined) }),
  usePublishSidebarHeaderAction: headerAction,
  useSidebarHeaderOffset: () => 0,
  useStatusController: () => ({ push: vi.fn() }),
  useTextFieldSubmitShortcut: () => vi.fn(),
  useWindowChromeTitle: () => undefined,
}));

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal()),
  useInfiniteQuery: () => catalogQuery.current,
}));

vi.mock("./useProjectEditData", () => {
  const mutation = () => ({ isPending: false, mutateAsync: vi.fn(async () => undefined) });
  return {
    useProjectDefaultWorkspaceSave: mutation,
    useProjectEdit: () => ({
      data: project,
      error: null,
      isError: false,
      isPending: false,
      refetch: vi.fn(async () => undefined),
    }),
    useProjectSave: mutation,
    useProjectWorkspaceAttach: mutation,
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
  WorkspaceRow: () => null,
  WorkspaceUnlinkFallbackDialog: () => null,
  workspaceUnlinkDialogWidth: 400,
}));

vi.mock("@/ui", () => ({
  Button: ({ children, ...props }: Readonly<{ children: ReactNode; [key: string]: unknown }>) => (
    <button {...props}>{children}</button>
  ),
  ErrorState: ({ body }: Readonly<{ body: string }>) => <p>{body}</p>,
  HelpHint: () => null,
  LoadingState: () => null,
  VirtualizedInfiniteList: ({ empty, header }: Readonly<{ empty: ReactNode; header: ReactNode }>) => (
    <>
      {header}
      {empty}
    </>
  ),
}));

function publishedHeader(): ReactNode {
  const action = headerAction.mock.lastCall?.[0];
  if (!isValidElement(action)) throw new Error("Expected Project Edit header actions.");
  return action;
}

describe("ProjectEditRoute sidebar header composition", () => {
  beforeEach(() => {
    headerAction.mockClear();
    catalogQuery.current = {
      ...catalogQuery.current,
      data: {
        pageParams: [null],
        pages: [
          {
            defaultWorkspaceID: "workspace-1",
            nextPageToken: null,
            projectID: "project-1",
            workspaces: [],
          },
        ],
      },
      error: null,
      isError: false,
      isFetchNextPageError: false,
      isPending: false,
    };
  });

  it("keeps Delete visible for a clean Project", () => {
    render(<ProjectEditRoute headerAccessory={<button>Delete</button>} projectId="project-1" />);
    render(publishedHeader());

    expect(screen.getByRole("button", { name: "Delete" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "projectEdit.saveName" })).not.toBeInTheDocument();
  });

  it("publishes Save and Delete together for a dirty Project", () => {
    render(<ProjectEditRoute headerAccessory={<button>Delete</button>} projectId="project-1" />);
    fireEvent.change(screen.getByRole("textbox", { name: "project-name" }), {
      target: { value: "Kent Desktop" },
    });
    render(publishedHeader());

    expect(screen.getByRole("button", { name: "projectEdit.saveName" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete" })).toBeInTheDocument();
  });

  it("keeps Project metadata editable when the Workspace catalog fails", () => {
    catalogQuery.current = {
      ...catalogQuery.current,
      data: undefined,
      error: new Error("Workspace catalog unavailable"),
      isError: true,
      isFetchNextPageError: true,
      isPending: false,
    };

    render(<ProjectEditRoute projectId="project-1" />);

    expect(screen.getByRole("textbox", { name: "project-name" })).toHaveValue("Kent");
    expect(screen.getByRole("textbox", { name: "project-key" })).toHaveValue("KNT");
    expect(screen.getByRole("button", { name: "projectEdit.attachWorkspace" })).toBeEnabled();
    expect(screen.getByText("Workspace catalog unavailable")).toBeInTheDocument();
  });
});
