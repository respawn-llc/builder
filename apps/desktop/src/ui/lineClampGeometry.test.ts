import { lineCountForAssignedHeight } from "./lineClampGeometry";

describe("lineCountForAssignedHeight", () => {
  it("uses every complete wrapped line that fits the assigned height", () => {
    expect(lineCountForAssignedHeight({ assignedHeight: 60, lineHeight: 20 })).toBe(3);
    expect(lineCountForAssignedHeight({ assignedHeight: 59, lineHeight: 20 })).toBe(2);
  });

  it("keeps one line when the assigned region is shorter than its line height", () => {
    expect(lineCountForAssignedHeight({ assignedHeight: 12, lineHeight: 20 })).toBe(1);
  });

  it("waits for measurable finite geometry", () => {
    expect(lineCountForAssignedHeight({ assignedHeight: 0, lineHeight: 20 })).toBeNull();
    expect(lineCountForAssignedHeight({ assignedHeight: 60, lineHeight: 0 })).toBeNull();
    expect(
      lineCountForAssignedHeight({ assignedHeight: Number.POSITIVE_INFINITY, lineHeight: 20 }),
    ).toBeNull();
  });
});
