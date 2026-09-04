import { fireEvent, render, screen } from "@testing-library/react";
import { useCallback } from "react";

import { appI18n, initializeI18n } from "@/i18n";
import type { SessionChatTarget } from "@/app-facade";
import type { ProjectTasksViewMemory } from "./projectTasksViewMemory";
import { HomeProjectContent } from "./HomeProjectContent";

type ProjectQueryFixture = Readonly<{
  data: Readonly<{ displayName: string; projectKey: string }> | undefined;
  error: Error | null;
  isPending: boolean;
}>;

const fixture = vi.hoisted(
  (): {
    projectQuery: ProjectQueryFixture;
    sessions: readonly {
      id: string;
      category: "main" | "subagent";
      name: string | null;
      firstPromptPreview: string | null;
      updatedAt: number;
    }[];
    sessionTargets: SessionChatTarget[];
  } => ({
    projectQuery: {
      data: { displayName: "Kent", projectKey: "KNT" },
      error: null,
      isPending: false,
    },
    sessions: [],
    sessionTargets: [],
  }),
);

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal()),
  useQueryClient: () => ({ invalidateQueries: vi.fn(), resetQueries: vi.fn() }),
  useQuery: () => fixture.projectQuery,
  useInfiniteQuery: () => ({
    data: { pages: [{ sessions: fixture.sessions }] },
    error: null,
    fetchNextPage: vi.fn(),
    hasNextPage: false,
    isError: false,
    isFetchingNextPage: false,
    isPending: false,
    refetch: vi.fn(),
  }),
}));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppNavigation: () => ({
    openSessionChat: async (target: SessionChatTarget) => {
      fixture.sessionTargets.push(target);
    },
    selectHomeProject: vi.fn(),
  }),
  useAppServices: () => ({ api: {} }),
  useOwnedSidebarRoots: () => ({ open: vi.fn() }),
}));

vi.mock("./ProjectTasksSurface", () => ({
  ProjectTasksSurface: ({ viewMemory }: Readonly<{ viewMemory: ProjectTasksViewMemory }>) => {
    const registerGrid = useCallback(
      (element: HTMLDivElement | null) => {
        if (element === null) return;
        const memory = viewMemory.read();
        element.scrollTop = memory.verticalOffsetPx;
        element.scrollLeft = memory.horizontalOffsetPx;
      },
      [viewMemory],
    );
    return (
      <div
        aria-label={appI18n.t("home.prototype.projectTasksGrid")}
        onScroll={(event) => {
          viewMemory.setScrollOffsets(event.currentTarget.scrollTop, event.currentTarget.scrollLeft);
        }}
        ref={registerGrid}
        role="grid"
      />
    );
  },
}));

beforeAll(async () => initializeI18n());

beforeEach(() => {
  fixture.projectQuery = {
    data: { displayName: "Kent", projectKey: "KNT" },
    error: null,
    isPending: false,
  };
  fixture.sessions = [];
  fixture.sessionTargets = [];
});

it("renders the Task surface while selected Project metadata is pending", () => {
  fixture.projectQuery = {
    data: undefined,
    error: null,
    isPending: true,
  };

  render(<HomeProjectContent projectID="project-1" sessionsVisible={false} sidebarMode="shift" />);

  expect(
    screen.getByRole("grid", { name: appI18n.t("home.prototype.projectTasksGrid") }),
  ).toBeInTheDocument();
});

it("restores Task-grid pixels after visiting another Project tab", () => {
  render(<HomeProjectContent projectID="project-1" sessionsVisible sidebarMode="shift" />);
  const grid = screen.getByRole("grid", { name: appI18n.t("home.prototype.projectTasksGrid") });
  grid.scrollTop = 500;
  grid.scrollLeft = 0;
  fireEvent.scroll(grid);

  fireEvent.click(screen.getByRole("tab", { name: appI18n.t("home.prototype.sessions") }));
  expect(
    screen.queryByRole("grid", { name: appI18n.t("home.prototype.projectTasksGrid") }),
  ).not.toBeInTheDocument();

  fireEvent.click(screen.getByRole("tab", { name: appI18n.t("home.prototype.tasks") }));
  const restoredGrid = screen.getByRole("grid", { name: appI18n.t("home.prototype.projectTasksGrid") });
  expect(restoredGrid.scrollTop).toBe(500);
  expect(restoredGrid.scrollLeft).toBe(0);
});

it("renders Tasks directly when Desktop Sessions are unavailable", () => {
  render(<HomeProjectContent projectID="project-1" sessionsVisible={false} sidebarMode="shift" />);

  expect(screen.queryByRole("tab")).not.toBeInTheDocument();
  expect(
    screen.getByRole("grid", { name: appI18n.t("home.prototype.projectTasksGrid") }),
  ).toBeInTheDocument();
});

it.each([
  ["main", "sessions"],
  ["subagent", "subagents"],
] as const)("opens a %s catalog row through Session Chat navigation", async (category, tabLabel) => {
  fixture.sessions = [
    {
      category,
      firstPromptPreview: "Review the change",
      id: `${category}-session`,
      name: "Review chat",
      updatedAt: 1,
    },
  ];

  render(<HomeProjectContent projectID="project-1" sessionsVisible sidebarMode="shift" />);
  fireEvent.click(screen.getByRole("tab", { name: appI18n.t(`home.prototype.${tabLabel}`) }));
  fireEvent.click(await screen.findByTestId("home-list-card-button"));

  expect(fixture.sessionTargets).toEqual([
    {
      catalogOrigin: { category },
      projectID: "project-1",
      sessionID: `${category}-session`,
    },
  ]);
});
