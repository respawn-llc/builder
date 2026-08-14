import { fireEvent, render, screen } from "@testing-library/react";
import { useCallback } from "react";

import { appI18n, initializeI18n } from "@/i18n";
import type { ProjectTasksViewMemory } from "./projectTasksViewMemory";
import { ProjectPrototypeDetail } from "./ProjectPrototypeDetail";

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal()),
  useInfiniteQuery: () => ({
    data: { pages: [] },
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
  useAppServices: () => ({ api: {} }),
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

it("restores Task-grid pixels after visiting another Project tab", () => {
  render(<ProjectPrototypeDetail projectID="project-1" sidebarMode="shift" />);
  const grid = screen.getByRole("grid", { name: appI18n.t("home.prototype.projectTasksGrid") });
  grid.scrollTop = 500;
  grid.scrollLeft = 120;
  fireEvent.scroll(grid);

  fireEvent.click(screen.getByRole("tab", { name: appI18n.t("home.prototype.sessions") }));
  expect(screen.queryByRole("grid", { name: appI18n.t("home.prototype.projectTasksGrid") })).not.toBeInTheDocument();

  fireEvent.click(screen.getByRole("tab", { name: appI18n.t("home.prototype.tasks") }));
  const restoredGrid = screen.getByRole("grid", { name: appI18n.t("home.prototype.projectTasksGrid") });
  expect(restoredGrid.scrollTop).toBe(500);
  expect(restoredGrid.scrollLeft).toBe(120);
});
