import labelComparisonV1 from "./labelComparisonV1.generated.json";

const labelCaseFoldV1: Readonly<Record<string, string>> = labelComparisonV1.fold_by_code_point;

export const LABEL_COMPARISON_VERSION = labelComparisonV1.version;

export function foldLabelText(value: string): string {
  let folded = "";
  for (const character of value.normalize("NFC")) {
    const codePoint = character.codePointAt(0);
    if (codePoint === undefined) {
      throw new Error("label comparison received an empty Unicode scalar");
    }
    folded += labelCaseFoldV1[String(codePoint)] ?? character;
  }
  return folded.normalize("NFC");
}

export function compareLabelNames(left: string, right: string): number {
  const foldedLeft = foldLabelText(left);
  const foldedRight = foldLabelText(right);
  if (foldedLeft < foldedRight) {
    return -1;
  }
  if (foldedLeft > foldedRight) {
    return 1;
  }
  return 0;
}

export function labelNamesEqual(left: string, right: string): boolean {
  return compareLabelNames(left, right) === 0;
}

export function labelNameContains(name: string, query: string): boolean {
  return foldLabelText(name).includes(foldLabelText(query));
}
