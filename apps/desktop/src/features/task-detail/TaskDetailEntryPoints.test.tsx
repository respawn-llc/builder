import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";

import type { TaskDetailSessionChatEntry } from "@/features/task-detail";
import { appI18n, initializeI18n } from "@/i18n";
import { StandaloneTaskRoute } from "./StandaloneTaskRoute";
import { TaskDetailWindowRoute } from "./TaskDetailWindowRoute";

type TaskDetailSurfaceTestProps = Readonly<{
  openSessionChat?: TaskDetailSessionChatEntry;
}>;

const fixture = vi.hoisted(() => ({
  closeCurrent: vi.fn(async () => undefined),
  navigation: {
    openHome: vi.fn(async () => undefined),
    openSessionChat: vi.fn(async () => undefined),
  },
  taskDetailProps: vi.fn<(props: TaskDetailSurfaceTestProps) => void>(),
}));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  SidebarRootOwner: ({ children }: Readonly<{ children: ReactNode }>) => <>{children}</>,
  useAppNavigation: () => fixture.navigation,
  useAppServices: () => ({
    nativeBridge: { window: { closeCurrent: fixture.closeCurrent } },
  }),
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

beforeEach(() => {
  fixture.closeCurrent.mockClear();
  fixture.navigation.openHome.mockClear();
  fixture.navigation.openSessionChat.mockClear();
  fixture.taskDetailProps.mockClear();
});

beforeAll(async () => initializeI18n());

it("supplies direct Session Chat navigation from standalone main-window Task Detail", async () => {
  render(
    <I18nextProvider i18n={appI18n}>
      <StandaloneTaskRoute taskId="task-1" />
    </I18nextProvider>,
  );

  const props = fixture.taskDetailProps.mock.lastCall?.[0];
  if (props?.openSessionChat === undefined) throw new Error("Expected standalone Chat entry.");

  await props.openSessionChat({ projectID: "project-1", sessionID: "session-1" });
  expect(fixture.navigation.openSessionChat).toHaveBeenCalledWith({
    projectID: "project-1",
    sessionID: "session-1",
  });
});

it("omits Session Chat navigation from native Task Detail", () => {
  render(<TaskDetailWindowRoute taskID="task-1" />);

  const props = fixture.taskDetailProps.mock.lastCall?.[0];
  expect(props?.openSessionChat).toBeUndefined();
});
