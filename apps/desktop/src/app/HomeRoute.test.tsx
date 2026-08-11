import { render, screen, within } from "@testing-library/react";

import { AppRoot } from "@/app/AppRoot";
import { removeBrowserStorage } from "@/app-facade";
import { createTestServices, startupRoutes } from "@/test-support/app-services";
import { getTabs } from "@/test-support/tabs";

it("starts on the Projects tab", async () => {
  window.history.replaceState(null, "", "/");
  removeBrowserStorage("local", "desktop.lastProjectRoute");
  removeBrowserStorage("session", "desktop.routeRestoreChecked");
  const services = createTestServices(startupRoutes);
  render(<AppRoot services={services} />);

  const controls = await screen.findByTestId("home-primary-controls");
  const projectsTab = within(screen.getByTestId("home-primary-projects-tab-island")).getByRole("tab");
  const workflowsTab = within(screen.getByTestId("home-primary-workflows-tab-island")).getByRole("tab");
  expect(getTabs(controls)).toEqual([projectsTab, workflowsTab]);
  expect(projectsTab).toHaveAttribute("aria-selected", "true");
  expect(workflowsTab).toHaveAttribute("aria-selected", "false");
});
