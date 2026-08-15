import { act, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { installResizeObserverGeometry } from "@/test-support/resize-observer";
import { TaskDetailBodyIslands } from "./TaskDetailBodyIslands";

describe.each([
  { descriptionHeightPx: 100, expectedHeightPx: 100, metadataHeightPx: 50, state: "collapsed" },
  { descriptionHeightPx: 1000, expectedHeightPx: 1000, metadataHeightPx: 50, state: "expanded" },
  { descriptionHeightPx: 100, expectedHeightPx: 500, metadataHeightPx: 500, state: "collapsed" },
  { descriptionHeightPx: 1000, expectedHeightPx: 1000, metadataHeightPx: 500, state: "expanded" },
])(
  "$state Description at $descriptionHeightPx px with Metadata at $metadataHeightPx px",
  ({ descriptionHeightPx, expectedHeightPx, metadataHeightPx }) => {
    it(`renders both islands at ${expectedHeightPx.toString()} px`, () => {
      const geometry = installResizeObserverGeometry();
      try {
        render(<TaskDetailBodyIslands description={<div>Description</div>} metadata={<div>Metadata</div>} />);
        const row = screen.getByTestId("task-detail-body-split");
        const description = screen.getByTestId("task-detail-description-slot");
        const metadata = screen.getByTestId("task-detail-metadata-slot");
        geometry.setGeometry(row, { rect: rect(900, 0) });
        geometry.setGeometry(description, { rect: rect(580, descriptionHeightPx) });
        geometry.setGeometry(metadata, { rect: rect(312, metadataHeightPx) });

        act(() => {
          geometry.notify();
        });

        expect(description).toHaveStyle({ height: `${expectedHeightPx.toString()}px` });
        expect(metadata).toHaveStyle({ height: `${expectedHeightPx.toString()}px` });
      } finally {
        geometry.restore();
      }
    });
  },
);

it("leaves stacked islands at their intrinsic heights below the side-by-side breakpoint", () => {
  const geometry = installResizeObserverGeometry();
  try {
    render(<TaskDetailBodyIslands description={<div>Description</div>} metadata={<div>Metadata</div>} />);
    const row = screen.getByTestId("task-detail-body-split");
    const description = screen.getByTestId("task-detail-description-slot");
    const metadata = screen.getByTestId("task-detail-metadata-slot");
    geometry.setGeometry(row, { rect: rect(719, 0) });
    geometry.setGeometry(description, { rect: rect(719, 100) });
    geometry.setGeometry(metadata, { rect: rect(719, 500) });

    act(() => {
      geometry.notify();
    });

    expect(row).not.toHaveAttribute("style");
    expect(description).not.toHaveAttribute("style");
    expect(metadata).not.toHaveAttribute("style");
  } finally {
    geometry.restore();
  }
});

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
