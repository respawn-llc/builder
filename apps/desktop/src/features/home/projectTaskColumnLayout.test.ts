import { resolveProjectTaskVisibleColumns, type ProjectTaskColumnLayout } from "./projectTaskColumnLayout";

const layout: ProjectTaskColumnLayout = {
  dependenciesPx: 48,
  idCharacters: 8,
  labelsPx: 320,
  workflowPx: 96,
};

describe("Project Task responsive columns", () => {
  it("shrinks Labels after hiding Workflow and before hiding Labels", () => {
    const visible = resolveProjectTaskVisibleColumns(500, layout);

    expect(visible).toEqual({
      dependencies: true,
      labelsPx: 228,
      title: true,
      workflow: false,
    });
  });

  it("keeps the minimum usable Labels track until the remaining width no longer fits it", () => {
    expect(resolveProjectTaskVisibleColumns(320, layout)).toMatchObject({
      dependencies: true,
      labelsPx: 48,
      title: true,
      workflow: false,
    });
    expect(resolveProjectTaskVisibleColumns(319, layout)).toMatchObject({
      dependencies: true,
      labelsPx: null,
      title: true,
      workflow: false,
    });
  });
});
