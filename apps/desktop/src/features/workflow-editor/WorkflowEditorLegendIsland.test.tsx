import { render, screen } from "@testing-library/react";

import { initializeI18n } from "../../i18n/setup";
import { WorkflowEditorLegendIsland } from "./WorkflowEditorLegendIsland";

void initializeI18n();

describe("WorkflowEditorLegendIsland", () => {
  it("includes script nodes in the legend", () => {
    render(<WorkflowEditorLegendIsland positionStrategy="absolute" />);

    expect(screen.getByText("Script node")).toBeInTheDocument();
  });
});
