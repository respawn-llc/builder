import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";

import { appI18n, initializeI18n } from "@/i18n";
import { ProjectTaskStatusLegend } from "./ProjectTaskStatusLegend";

beforeAll(async () => initializeI18n());

describe("ProjectTaskStatusLegend", () => {
  it("shows every canonical Task status under its Project-list group", async () => {
    const user = userEvent.setup();
    render(
      <I18nextProvider i18n={appI18n}>
        <ProjectTaskStatusLegend />
      </I18nextProvider>,
    );

    await user.hover(screen.getByTestId("project-task-status-legend-trigger"));
    const tooltip = await screen.findByRole("tooltip");
    expect(tooltip).toHaveTextContent("Active");
    expect(tooltip).toHaveTextContent("Waiting for question — Needs an answer");
    expect(tooltip).toHaveTextContent("Waiting for approval — Needs approval");
    expect(tooltip).toHaveTextContent("Interrupted — Stopped and available to resume");
    expect(tooltip).toHaveTextContent("Running — Executing now");
    expect(tooltip).toHaveTextContent("Queued — Waiting to execute");
    expect(tooltip).toHaveTextContent("Active — In progress");
    expect(tooltip).toHaveTextContent("Backlog");
    expect(tooltip).toHaveTextContent("Backlog — Not started");
    expect(tooltip).toHaveTextContent("Done");
    expect(tooltip).toHaveTextContent("Done — Completed");
  });
});
