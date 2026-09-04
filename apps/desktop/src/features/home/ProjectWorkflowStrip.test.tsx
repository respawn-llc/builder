import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { vi } from "vitest";

import { appI18n, initializeI18n } from "@/i18n";
import { ProjectWorkflowStrip } from "./ProjectWorkflowStrip";

const openProject = vi.hoisted(() => vi.fn());

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppNavigation: () => ({ openProject }),
}));

beforeAll(async () => initializeI18n());

describe("ProjectWorkflowStrip", () => {
  it("places Sort and Link Workflow before the retained Workflow items", () => {
    renderStrip({
      workflows: [
        { description: "", id: "workflow-1", isProjectDefault: false, name: "Delivery" },
        { description: "", id: "workflow-2", isProjectDefault: false, name: "Support" },
      ],
    });

    const buttons = screen.getAllByRole("button");
    expect(buttons).toHaveLength(4);
    expect(buttons[0]).toHaveAttribute("aria-haspopup", "dialog");
    expect(buttons[1]).not.toHaveAttribute("title");
    for (const button of buttons.slice(2)) {
      expect(button).toHaveAttribute("title");
    }
  });

  it("keeps the fixed controls visible before an initial loading boundary", () => {
    renderStrip({
      initialBoundary: { label: "loading", state: "loading" },
      workflows: [],
    });

    const buttons = screen.getAllByRole("button");
    expect(buttons).toHaveLength(2);
    expect(buttons[0]).toHaveAttribute("aria-haspopup", "dialog");
    expect(buttons[1]).not.toHaveAttribute("title");
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("opens the selected Project Workflow board", () => {
    renderStrip({
      workflows: [
        { description: "Delivery workflow", id: "workflow-1", isProjectDefault: false, name: "Delivery" },
      ],
    });

    fireEvent.click(screen.getByRole("button", { name: "Delivery" }));

    expect(openProject).toHaveBeenCalledWith("project-1", "workflow-1");
  });

  it("opens the selected Project Workflow board from keyboard activation", async () => {
    const user = userEvent.setup();
    renderStrip({
      workflows: [
        { description: "Delivery workflow", id: "workflow-1", isProjectDefault: false, name: "Delivery" },
      ],
    });

    const button = screen.getByRole("button", { name: "Delivery" });
    button.focus();
    await user.keyboard("{Enter}");
    expect(openProject).toHaveBeenCalledWith("project-1", "workflow-1");

    openProject.mockClear();
    button.focus();
    await user.keyboard(" ");
    expect(openProject).toHaveBeenCalledWith("project-1", "workflow-1");
  });
});

function renderStrip({
  initialBoundary,
  workflows,
}: Readonly<{
  initialBoundary?: { label: string; state: "loading" };
  workflows: readonly Readonly<{
    description: string;
    id: string;
    isProjectDefault: boolean;
    name: string;
  }>[];
}>) {
  return render(
    <I18nextProvider i18n={appI18n}>
      <ProjectWorkflowStrip
        hasNextPage={false}
        hasPreviousPage={false}
        initialBoundary={initialBoundary}
        isFetchingNextPage={false}
        isFetchingPreviousPage={false}
        nextBoundary={undefined}
        onLinkWorkflow={vi.fn()}
        onLoadNext={vi.fn()}
        onLoadPrevious={vi.fn()}
        onSortChange={vi.fn()}
        previousBoundary={undefined}
        projectID="project-1"
        sort={{ direction: "desc", field: "updated" }}
        workflows={workflows}
      />
    </I18nextProvider>,
  );
}
