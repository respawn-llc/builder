import { act, render, screen } from "@testing-library/react";

import { installResizeObserverGeometry } from "@/test-support/resize-observer";
import { TaskDetailBodyIslands } from "./TaskDetailBodyIslands";

it("keeps Description and Metadata at the larger intrinsic height in both directions", () => {
  const geometry = installResizeObserverGeometry();
  try {
    const view = renderBodyIslands("short description", "tall metadata");
    const row = screen.getByTestId("task-detail-body-split");
    const description = screen.getByTestId("task-detail-description-slot");
    const metadata = screen.getByTestId("task-detail-metadata-slot");

    geometry.setGeometry(row, { rect: rect(900, 0) });
    geometry.setGeometry(description, { rect: rect(580, 240) });
    geometry.setGeometry(metadata, { rect: rect(312, 480) });
    act(() => {
      geometry.notify();
    });

    expect(description).toHaveStyle({ height: "480px" });
    expect(metadata).toHaveStyle({ height: "480px" });

    geometry.setGeometry(description, { rect: rect(580, 720) });
    geometry.setGeometry(metadata, { rect: rect(312, 480) });
    view.rerender(
      <TaskDetailBodyIslands
        description={<div>tall description</div>}
        metadata={<div>short metadata</div>}
      />,
    );

    expect(description).toHaveStyle({ height: "720px" });
    expect(metadata).toHaveStyle({ height: "720px" });
  } finally {
    geometry.restore();
  }
});

function renderBodyIslands(description: string, metadata: string) {
  return render(
    <TaskDetailBodyIslands description={<div>{description}</div>} metadata={<div>{metadata}</div>} />,
  );
}

function rect(width: number, height: number): DOMRect {
  return {
    bottom: height,
    height,
    left: 0,
    right: width,
    top: 0,
    width,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  };
}
