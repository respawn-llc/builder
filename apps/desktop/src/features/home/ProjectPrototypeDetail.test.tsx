import { fireEvent, render, screen } from "@testing-library/react";

import { initializeI18n } from "@/i18n";
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
  ProjectTasksSurface: () => (
    <div aria-label="Retained tasks" data-testid="retained-tasks-grid" role="region" />
  ),
}));

beforeAll(async () => initializeI18n());

it("retains the mounted Tasks grid and its pixels across Project tab changes", () => {
  render(<ProjectPrototypeDetail projectID="project-1" sidebarMode="shift" />);
  const grid = screen.getByTestId("retained-tasks-grid");
  grid.scrollTop = 500;
  grid.scrollLeft = 120;

  fireEvent.click(screen.getByRole("tab", { name: "Sessions" }));
  expect(screen.getByTestId("retained-tasks-grid")).toBe(grid);
  expect(screen.queryByRole("region", { name: "Retained tasks" })).not.toBeInTheDocument();
  expect(screen.getByRole("region", { hidden: true, name: "Retained tasks" })).toBe(grid);

  fireEvent.click(screen.getByRole("tab", { name: "Tasks" }));
  expect(screen.getByTestId("retained-tasks-grid")).toBe(grid);
  expect(screen.getByRole("region", { name: "Retained tasks" })).toBe(grid);
  expect(grid.scrollTop).toBe(500);
  expect(grid.scrollLeft).toBe(120);
});
