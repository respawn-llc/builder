import labelComparisonV1 from "./labelComparisonV1.generated.json";
import {
  LABEL_COMPARISON_VERSION,
  compareLabelNames,
  foldLabelText,
  labelNameContains,
} from "./labelComparison";

describe("label comparison", () => {
  it("matches the shared versioned case-fold corpus", () => {
    const comparisonFixture = labelComparisonV1.fixture;
    expect(LABEL_COMPARISON_VERSION).toBe(comparisonFixture.version);

    for (const test of comparisonFixture.fold) {
      expect(foldLabelText(test.input)).toBe(test.expected);
    }
    for (const test of comparisonFixture.contains) {
      expect(labelNameContains(test.value, test.query)).toBe(test.expected);
    }
    for (const test of comparisonFixture.order) {
      expect(Math.sign(compareLabelNames(test.left, test.right))).toBe(test.expected);
    }
  });
});
