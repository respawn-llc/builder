import { describe, expect, it } from "vitest";

import { projectVerticalReorder } from "./reorderProjection";

describe("projectVerticalReorder", () => {
  it("keeps activation in the source slot until a destination is crossed", () => {
    expect(projectVerticalReorder(["first", "second", "third"], "second", "second")).toEqual({
      insertionIndex: undefined,
      orderedIDs: null,
    });
  });

  it("projects one adjacent destination for both layout and commit", () => {
    expect(projectVerticalReorder(["first", "second", "third"], "second", "third")).toEqual({
      insertionIndex: 3,
      orderedIDs: ["first", "third", "second"],
    });
  });
});
