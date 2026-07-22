import { act, render, screen } from "@testing-library/react";

import { installOneLineOverflowGeometry, visibleOneLineOverflowText } from "@/test-support/one-line-overflow";
import { OneLineOverflowRow } from "./index";

describe("OneLineOverflowRow", () => {
  it("recomputes complete visible items across narrow and wide widths", () => {
    const geometry = installOneLineOverflowGeometry({
      availableWidth: 96,
      gap: 4,
      itemWidth: 32,
      overflowWidth: 24,
    });
    try {
      render(
        <OneLineOverflowRow
          ariaLabel="Labels"
          items={[
            { id: "one", content: <span>One</span> },
            { id: "two", content: <span>Two</span> },
            { id: "three", content: <span>Three</span> },
            { id: "four", content: <span>Four</span> },
          ]}
          renderOverflow={(hiddenCount) => <span>+{hiddenCount}</span>}
        />,
      );
      act(() => {
        geometry.notify();
      });

      const row = screen.getByRole("group", { name: "Labels" });
      expect(visibleOneLineOverflowText(row)).toBe("OneTwo+2");

      geometry.setAvailableWidth(140);
      act(() => {
        geometry.notify();
      });

      expect(visibleOneLineOverflowText(row)).toBe("OneTwoThreeFour");
    } finally {
      geometry.restore();
    }
  });
});
