import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { expect, it, vi } from "vitest";

import type { TaskDetailSessionChatEntry } from "@/features/task-detail";
import { appI18n, initializeI18n } from "@/i18n";
import { StandaloneTaskRoute } from "./StandaloneTaskRoute";

type TaskDetailSurfaceTestProps = Readonly<{
  openSessionChat?: TaskDetailSessionChatEntry;
}>;

const fixture = vi.hoisted(() => ({
  navigation: {
    openHome: vi.fn(async () => undefined),
    openSessionChat: vi.fn(async () => undefined),
  },
  taskDetailProps: vi.fn<(props: TaskDetailSurfaceTestProps) => void>(),
}));

vi.mock("@/shared/feature-flags", () => ({ desktopChatEnabled: false }));
vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  SidebarRootOwner: ({ children }: Readonly<{ children: ReactNode }>) => <>{children}</>,
  useAppNavigation: () => fixture.navigation,
  useOwnedSidebarRoots: () => ({ open: vi.fn() }),
}));
vi.mock("./TaskDetailSurface", () => ({
  TaskDetailSurface: (props: TaskDetailSurfaceTestProps) => {
    fixture.taskDetailProps(props);
    return <div />;
  },
}));
vi.mock("./taskDetailDismissal", () => ({
  useExactTaskDetailDeleteDismissal: () => async () => ({ kind: "accepted" }),
}));

beforeAll(async () => initializeI18n());
beforeEach(() => fixture.taskDetailProps.mockClear());

it("omits Open Chat from the production main-window Task Detail host", () => {
  render(
    <I18nextProvider i18n={appI18n}>
      <StandaloneTaskRoute taskId="task-1" />
    </I18nextProvider>,
  );

  expect(fixture.taskDetailProps.mock.lastCall?.[0].openSessionChat).toBeUndefined();
});
