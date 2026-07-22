import { oneLineOverflowLayout } from "./oneLineOverflowGeometry";

describe("one-line overflow geometry", () => {
  it("keeps every complete item when the row fits", () => {
    expect(
      oneLineOverflowLayout({
        availableWidth: 104,
        gap: 4,
        itemWidths: [32, 32, 32],
        overflowWidth: () => 24,
      }),
    ).toEqual({
      hiddenCount: 0,
      visibleCount: 3,
    });
  });

  it("reserves the final fitting position for the overflow item", () => {
    expect(
      oneLineOverflowLayout({
        availableWidth: 104,
        gap: 4,
        itemWidths: [32, 32, 32, 32],
        overflowWidth: () => 24,
      }),
    ).toEqual({
      hiddenCount: 2,
      visibleCount: 2,
    });
  });

  it("falls back to only the overflow item at the narrow boundary", () => {
    expect(
      oneLineOverflowLayout({
        availableWidth: 20,
        gap: 4,
        itemWidths: [32, 32, 32],
        overflowWidth: () => 24,
      }),
    ).toEqual({
      hiddenCount: 3,
      visibleCount: 0,
    });
  });
});
