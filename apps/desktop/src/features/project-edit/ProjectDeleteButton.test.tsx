import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState, type ReactNode } from "react";
import { createTestSidebarNavigator } from "@/test-support/sidebar";
import { ProjectDeleteButton } from "./ProjectDeleteButton";
type CompletionInput = Readonly<{
  navigateHome?: (() => Promise<void>) | undefined;
}>;
type FallbackConfig = Readonly<{
  renderFallback: (payload: Readonly<{ projectID: string }>, close: () => void) => ReactNode;
}>;

const fixture = vi.hoisted(() => ({
  complete: vi.fn<(input: CompletionInput) => Promise<void>>(),
  mutateAsync: vi.fn(async () => ({ blockers: [], deleted: true })),
  openHome: vi.fn(async () => undefined),
  push: vi.fn(),
}));

vi.mock("./useProjectEditData", () => ({
  useProjectDelete: () => ({ isPending: false, mutateAsync: fixture.mutateAsync }),
}));

vi.mock("@/app-facade", () => ({
  completeProjectDeletion: fixture.complete,
  useAppNavigation: () => ({ openHome: fixture.openHome }),
  useAppServices: () => ({
    nativeBridge: {
      capabilities: { dialogWindows: false },
      dialogs: { openWindow: vi.fn(async () => undefined) },
    },
  }),
  useConnectionSnapshot: () => ({ phase: "connected" }),
  useNativeDialogFallback: function useNativeDialogFallback(config: FallbackConfig) {
    const [payload, setPayload] = useState<Readonly<{ projectID: string }> | undefined>();
    return {
      fallback:
        payload === undefined
          ? null
          : config.renderFallback(payload, () => {
              setPayload(undefined);
            }),
      open: async (next: Readonly<{ projectID: string }>) => {
        setPayload(next);
      },
    };
  },
  useStatusController: () => ({ push: fixture.push }),
}));

describe("ProjectDeleteButton scoped completion", () => {
  beforeEach(() => {
    fixture.complete.mockReset();
    fixture.complete.mockImplementation(async (input) => {
      if (input.navigateHome !== undefined) await input.navigateHome();
    });
    fixture.mutateAsync.mockClear();
    fixture.openHome.mockClear();
    fixture.push.mockClear();
  });

  it.each([
    ["accepted", 1],
    ["stale", 0],
  ] as const)("navigates Home only when scoped close is %s", async (outcome, homeCalls) => {
    const navigator = createTestSidebarNavigator({
      close: vi.fn(() => outcome),
    });
    const queryClient = new QueryClient();
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <ProjectDeleteButton navigator={navigator} projectID="project-1" />
      </QueryClientProvider>,
    );

    await user.click(screen.getByRole("button", { name: "projectEdit.deleteProject" }));
    await user.click(await screen.findByRole("button", { name: "projectEdit.deleteConfirm" }));
    await waitFor(() => {
      expect(fixture.complete).toHaveBeenCalledOnce();
    });

    expect(navigator.close).toHaveBeenCalledOnce();
    expect(fixture.openHome).toHaveBeenCalledTimes(homeCalls);
    expect(fixture.complete).toHaveBeenCalledWith(
      expect.objectContaining({
        navigateHome: outcome === "accepted" ? fixture.openHome : undefined,
        projectID: "project-1",
      }),
    );
    queryClient.clear();
  });
});
