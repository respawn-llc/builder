import { defaultRangeExtractor, type Range } from "@tanstack/react-virtual";

export function pinnedVirtualRangeExtractor(range: Range, pinnedIndexes: ReadonlySet<number>): number[] {
  const indexes = new Set(defaultRangeExtractor(range));
  for (const index of pinnedIndexes) {
    if (index >= 0 && index < range.count) {
      indexes.add(index);
    }
  }
  return [...indexes].sort((left, right) => left - right);
}
