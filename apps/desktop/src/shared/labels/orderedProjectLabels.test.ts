import { selectOrderedProjectLabels } from "./orderedProjectLabels";

const alphaID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const betaID = "942495c2-5958-4959-8445-94046ad74fbd";

describe("ordered Project Label projection", () => {
  it("uses catalog order instead of assignment mutation order", () => {
    expect(
      selectOrderedProjectLabels(
        [
          { id: alphaID, name: "Alpha" },
          { id: betaID, name: "Beta" },
        ],
        [betaID, alphaID],
      ),
    ).toEqual([
      { id: alphaID, name: "Alpha" },
      { id: betaID, name: "Beta" },
    ]);
  });
});
