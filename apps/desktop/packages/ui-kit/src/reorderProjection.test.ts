import { describe, expect, it } from "vitest";

import { projectVerticalReorder } from "./reorderProjection";

describe("projectVerticalReorder", () => {
  it("keeps activation in the source slot until a destination is crossed", () => {
    expect(projectVerticalReorder(["first", "second", "third"], "second", "second")).toBeNull();
  });

  it("maps one adjacent destination to the committed order", () => {
    expect(projectVerticalReorder(["first", "second", "third"], "second", "third")).toEqual([
      "first",
      "third",
      "second",
    ]);
  });
});
