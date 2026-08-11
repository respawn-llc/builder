import { render, screen } from "@testing-library/react";

import { AppRoot } from "@/app/AppRoot";
import { removeBrowserStorage } from "@/app-facade";
import { createTestServices, startupRoutes } from "@/test-support/app-services";

it("starts on the Projects tab", async () => {
  window.history.replaceState(null, "", "/");
  removeBrowserStorage("local", "desktop.lastProjectRoute");
  removeBrowserStorage("session", "desktop.routeRestoreChecked");
  const services = createTestServices(startupRoutes);
  render(<AppRoot services={services} />);

  expect(await screen.findByRole("tab", { name: "Projects" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  expect(screen.getByRole("tab", { name: "Workflows" })).toHaveAttribute(
    "aria-selected",
    "false",
  );
});
