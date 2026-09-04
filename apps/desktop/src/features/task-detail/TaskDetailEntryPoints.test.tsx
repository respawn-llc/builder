import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement } from "react";
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterContextProvider,
} from "@tanstack/react-router";

import { SidebarRootContext } from "@/app-facade";
import { createBrowserNativeBridge } from "@/test-support/native-bridge";
import { createTaskDetailTestServices, taskDetailResponse } from "@/test-support/task-detail";
import { createTestSidebarController } from "@/test-support/sidebar";
import { TestAppProviders } from "@/test-support/app-services";
import { appI18n, initializeI18n } from "@/i18n";
import { StandaloneTaskRoute } from "./StandaloneTaskRoute";
import { TaskDetailWindowRoute } from "./TaskDetailWindowRoute";

const fixture = vi.hoisted(() => ({
  featureFlags: { desktopChatEnabled: true },
  copyText: vi.fn(async () => undefined),
  navigation: {
    openHome: vi.fn(async () => undefined),
    openSessionChat: vi.fn(async () => undefined),
  },
}));

vi.mock("@/shared/feature-flags", () => fixture.featureFlags);
vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppNavigation: () => fixture.navigation,
}));
vi.mock("./taskDetailDismissal", () => ({
  useExactTaskDetailDeleteDismissal: () => async () => ({ kind: "accepted" }),
}));

beforeAll(async () => initializeI18n());
beforeEach(() => {
  fixture.featureFlags.desktopChatEnabled = true;
  fixture.copyText.mockClear();
  fixture.navigation.openHome.mockClear();
  fixture.navigation.openSessionChat.mockClear();
});

function renderTaskDetailHost(child: ReactElement) {
  const browserBridge = createBrowserNativeBridge();
  const nativeBridge = {
    ...browserBridge,
    capabilities: {
      ...browserBridge.capabilities,
      clipboard: { ...browserBridge.capabilities.clipboard, writeText: true },
    },
    clipboard: { ...browserBridge.clipboard, writeText: fixture.copyText },
  };
  const services = createTaskDetailTestServices(taskDetailResponse, { nativeBridge });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/tasks/task-1"] }),
    routeTree: createRootRoute(),
  });
  return render(
    <RouterContextProvider router={router}>
      <SidebarRootContext.Provider value={createTestSidebarController()}>
        <TestAppProviders services={services}>{child}</TestAppProviders>
      </SidebarRootContext.Provider>
    </RouterContextProvider>,
  );
}

it.each([
  ["development", true, true],
  ["production", false, false],
] as const)("renders the %s main-window Task Detail actions", async (_name, enabled, hasChat) => {
  fixture.featureFlags.desktopChatEnabled = enabled;
  renderTaskDetailHost(<StandaloneTaskRoute taskId="task-1" />);

  const flow = await screen.findByTestId("task-detail-action-flow");
  const openInCli = within(flow).getByRole("button", {
    name: appI18n.t("task.openInCli", { name: "Review chat" }),
  });
  expect(openInCli).toBeInTheDocument();
  fireEvent.click(openInCli);
  await waitFor(() => {
    expect(fixture.copyText).toHaveBeenCalledWith("kent --session=session-1");
  });

  const openChat = within(flow).queryByRole("button", {
    name: appI18n.t("task.openChat", { name: "Review chat" }),
  });
  expect(openChat !== null).toBe(hasChat);
  if (openChat !== null) {
    fireEvent.click(openChat);
    await waitFor(() => {
      expect(fixture.navigation.openSessionChat).toHaveBeenCalledWith({
        projectID: "project-1",
        sessionID: "session-1",
      });
    });
  }
});

it("keeps native Task Detail on its CLI action without a Chat action", async () => {
  renderTaskDetailHost(<TaskDetailWindowRoute taskID="task-1" />);

  const flow = await screen.findByTestId("task-detail-action-flow");
  expect(
    within(flow).getByRole("button", {
      name: appI18n.t("task.openInCli", { name: "Review chat" }),
    }),
  ).toBeInTheDocument();
  expect(
    within(flow).queryByRole("button", {
      name: appI18n.t("task.openChat", { name: "Review chat" }),
    }),
  ).not.toBeInTheDocument();
});
