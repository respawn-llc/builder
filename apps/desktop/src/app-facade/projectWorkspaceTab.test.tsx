import { fireEvent, render, screen } from "@testing-library/react";

import { ProjectWorkspaceTabProvider, useProjectWorkspaceTab } from "./projectWorkspaceTab";

function TabProbe({ projectID }: Readonly<{ projectID: string }>) {
  const { selectedTab, selectTab } = useProjectWorkspaceTab();
  return (
    <>
      <output aria-label="project">{projectID}</output>
      <output aria-label="selected-tab">{selectedTab}</output>
      <button
        data-testid="select-workflows"
        onClick={() => {
          selectTab("workflows");
        }}
        type="button"
      >
        Select
      </button>
    </>
  );
}

it("defaults to Sessions, carries Workflows between Projects, and resets after remount", () => {
  const view = render(
    <ProjectWorkspaceTabProvider>
      <TabProbe projectID="project-1" />
    </ProjectWorkspaceTabProvider>,
  );

  expect(screen.getByLabelText("selected-tab")).toHaveTextContent("sessions");
  fireEvent.click(screen.getByTestId("select-workflows"));
  view.rerender(
    <ProjectWorkspaceTabProvider>
      <TabProbe projectID="project-2" />
    </ProjectWorkspaceTabProvider>,
  );
  expect(screen.getByLabelText("project")).toHaveTextContent("project-2");
  expect(screen.getByLabelText("selected-tab")).toHaveTextContent("workflows");

  view.unmount();
  render(
    <ProjectWorkspaceTabProvider>
      <TabProbe projectID="project-2" />
    </ProjectWorkspaceTabProvider>,
  );
  expect(screen.getByLabelText("selected-tab")).toHaveTextContent("sessions");
});
