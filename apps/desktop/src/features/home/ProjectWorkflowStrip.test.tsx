import { render, screen } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { vi } from "vitest";

import { appI18n, initializeI18n } from "@/i18n";
import { ProjectWorkflowStrip } from "./ProjectWorkflowStrip";

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppNavigation: () => ({ openProject: vi.fn() }),
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
    expect(buttons.map((button) => button.getAttribute("aria-label") ?? button.textContent)).toEqual([
      appI18n.t("board.sort.chip"),
      appI18n.t("workflowLibrary.linkWorkflow"),
      "Delivery",
      "Support",
    ]);
  });

  it("keeps the fixed controls visible before an initial loading boundary", () => {
    renderStrip({
      initialBoundary: { label: appI18n.t("states.loading"), state: "loading" },
      workflows: [],
    });

    expect(
      screen.getAllByRole("button").map((button) => button.getAttribute("aria-label") ?? button.textContent),
    ).toEqual([appI18n.t("board.sort.chip"), appI18n.t("workflowLibrary.linkWorkflow")]);
    expect(screen.getByRole("status", { name: appI18n.t("states.loading") })).toBeInTheDocument();
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
