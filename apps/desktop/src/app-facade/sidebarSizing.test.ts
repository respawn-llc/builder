import {
  clampSidebarWidth,
  defaultSidebarSizePreference,
  initialSidebarWidthForViewport,
  resolveSidebarWidth,
  sidebarResizeBoundsForShellWidth,
} from "./sidebarSizing";

describe("sidebar sizing", () => {
  it("uses the content desired width clamped by the global max and effective minimum", () => {
    expect(initialSidebarWidthForViewport(800)).toBe(550);
    expect(initialSidebarWidthForViewport(1600)).toBe(550);
    expect(initialSidebarWidthForViewport(3000)).toBe(550);
    expect(initialSidebarWidthForViewport(800, { desiredWidthPx: 900, minWidthPx: 620 })).toBe(680);
    expect(initialSidebarWidthForViewport(700, { desiredWidthPx: 900, minWidthPx: 620 })).toBe(595);
  });

  it("uses 85 percent of shell width as resize max before applying minimums", () => {
    expect(sidebarResizeBoundsForShellWidth(320)).toEqual({
      maxWidthPx: 272,
      minWidthPx: 272,
      shellWidthPx: 320,
    });
    expect(sidebarResizeBoundsForShellWidth(760)).toEqual({
      maxWidthPx: 646,
      minWidthPx: 350,
      shellWidthPx: 760,
    });
    expect(sidebarResizeBoundsForShellWidth(1200, { desiredWidthPx: 900, minWidthPx: 620 })).toEqual({
      maxWidthPx: 1020,
      minWidthPx: 620,
      shellWidthPx: 1200,
    });
  });

  it("clamps resized widths to min and max bounds", () => {
    expect(clampSidebarWidth(100, 1020)).toBe(350);
    expect(clampSidebarWidth(100, 1020, 620)).toBe(620);
    expect(clampSidebarWidth(560.4, 1020)).toBe(560);
    expect(clampSidebarWidth(1200, 1020)).toBe(1020);
    expect(
      resolveSidebarWidth(100, sidebarResizeBoundsForShellWidth(320, defaultSidebarSizePreference)).px,
    ).toBe(272);
  });
});
