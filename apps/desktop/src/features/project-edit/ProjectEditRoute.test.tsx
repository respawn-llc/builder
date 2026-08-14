import { fireEvent, render, screen } from "@testing-library/react";
import { isValidElement, type ReactNode } from "react";

import { ProjectEditRoute } from "./ProjectEditRoute";

const headerAction = vi.hoisted(() => vi.fn<(action: ReactNode) => void>());
const project = vi.hoisted(() => ({
  defaultWorkspaceID: "workspace-1",
  displayName: "Kent",
  nextPageToken: "",
  projectID: "project-1",
  projectKey: "KNT",
  workspaces: [],
}));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppServices: () => ({
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

vi.mock("./useProjectEditData", () => {
  const mutation = () => ({ isPending: false, mutateAsync: vi.fn(async () => undefined) });
  return {
    useProjectDefaultWorkspaceSave: mutation,
    useProjectEdit: () => ({
      data: { pages: [project] },
      fetchNextPage: vi.fn(async () => undefined),
      hasNextPage: false,
      isError: false,
      isFetchingNextPage: false,
      isPending: false,
    }),
    useProjectSave: mutation,
    useProjectWorkspaceAttach: mutation,
    useProjectWorkspaceChangedEvents: () => undefined,
    useProjectWorkspaceUnlink: mutation,
    useProjectWorkspaceUnlinkRequests: () => undefined,
  };
});

vi.mock("./ProjectEditParts", () => ({
  ProjectKeyField: ({ keyDraft, onKeyChange }: Readonly<{ keyDraft: string; onKeyChange(value: string): void }>) => (
    <input aria-label="project-key" onChange={(event) => { onKeyChange(event.target.value); }} value={keyDraft} />
  ),
  ProjectNameField: ({ nameDraft, onNameChange }: Readonly<{ nameDraft: string; onNameChange(value: string): void }>) => (
    <input aria-label="project-name" onChange={(event) => { onNameChange(event.target.value); }} value={nameDraft} />
  ),
  WorkspaceRow: () => null,
  WorkspaceUnlinkFallbackDialog: () => null,
  workspaceUnlinkDialogWidth: 400,
}));

vi.mock("@/ui", () => ({
  Button: ({ children, ...props }: Readonly<{ children: ReactNode; [key: string]: unknown }>) => <button {...props}>{children}</button>,
  ErrorState: () => null,
  HelpHint: () => null,
  LoadingState: () => null,
  VirtualizedInfiniteList: ({ header }: Readonly<{ header: ReactNode }>) => <>{header}</>,
}));

function publishedHeader(): ReactNode {
  const action = headerAction.mock.lastCall?.[0];
  if (!isValidElement(action)) throw new Error("Expected Project Edit header actions.");
  return action;
}

describe("ProjectEditRoute sidebar header composition", () => {
  beforeEach(() => {
    headerAction.mockClear();
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
});
