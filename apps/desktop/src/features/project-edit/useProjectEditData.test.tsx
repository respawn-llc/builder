import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";

import { queryKeys } from "@/app-facade";
import { createBrowserNativeBridge } from "@/test-support/native-bridge";
import {
  useProjectDefaultWorkspaceSave,
  useProjectWorkspaceAttach,
  useProjectWorkspaceChangedEvents,
  useProjectWorkspaceUnlink,
} from "./useProjectEditData";

const api = vi.hoisted(() => ({
  attachWorkspace: vi.fn(async (): Promise<{ binding: object; outcome: "attached" }> => ({
    binding: {},
    outcome: "attached",
  })),
  setDefaultWorkspace: vi.fn(async () => ({ project: {} })),
  unlinkWorkspace: vi.fn(async () => ({ blockers: [], project: null })),
}));
const changed = vi.hoisted((): { handler: ((event: { projectID: string }) => void) | undefined } => ({
  handler: undefined,
}));
vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppServices: () => ({ api, logger: { append: vi.fn(async () => undefined) } }),
}));
const wrapper =
  (client: QueryClient) =>
  ({ children }: Readonly<{ children: ReactNode }>) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );

describe("Project Settings Workspace refresh ownership", () => {
  beforeEach(() => {
    changed.handler = undefined;
  });
  it.each([
    ["default", useProjectDefaultWorkspaceSave, "workspace-1"],
    ["attach", useProjectWorkspaceAttach, "/workspace"],
    ["unlink", useProjectWorkspaceUnlink, "workspace-1"],
  ] as const)("%s mutation leaves the canonical catalog untouched", async (_name, hook, input) => {
    const client = new QueryClient();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const view = renderHook(() => hook("project-1"), { wrapper: wrapper(client) });
    await act(async () => {
      await view.result.current.mutateAsync(input);
    });
    expect(invalidate).not.toHaveBeenCalledWith(
      expect.objectContaining({
        queryKey: queryKeys.projectWorkspaceCatalog("project-1"),
      }),
    );
  });

  it("native Workspace changes leave the canonical catalog untouched", async () => {
    const client = new QueryClient();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const nativeBridge = createBrowserNativeBridge();
    vi.spyOn(nativeBridge.projectWorkspace, "onChanged").mockImplementation(async (handler) => {
      changed.handler = handler;
      return () => undefined;
    });
    renderHook(
      () => {
        useProjectWorkspaceChangedEvents(nativeBridge, "project-1");
      },
      {
        wrapper: wrapper(client),
      },
    );
    await vi.waitFor(() => {
      expect(changed.handler).toBeDefined();
    });
    changed.handler?.({ projectID: "project-1" });
    await vi.waitFor(() => {
      expect(invalidate).toHaveBeenCalled();
    });
    expect(invalidate).not.toHaveBeenCalledWith(
      expect.objectContaining({
        queryKey: queryKeys.projectWorkspaceCatalog("project-1"),
      }),
    );
  });
});
