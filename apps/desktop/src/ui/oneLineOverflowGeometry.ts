export type OneLineOverflowLayout = Readonly<{
  hiddenCount: number;
  visibleCount: number;
}>;

export function oneLineOverflowLayout({
  availableWidth,
  gap,
  itemWidths,
  overflowWidth,
}: Readonly<{
  availableWidth: number;
  gap: number;
  itemWidths: readonly number[];
  overflowWidth(hiddenCount: number): number;
}>): OneLineOverflowLayout {
  if (itemWidths.length === 0) {
    return {
      hiddenCount: 0,
      visibleCount: 0,
    };
  }
  const completeWidth = itemWidths.reduce((total, width) => total + width, 0) + gap * (itemWidths.length - 1);
  if (completeWidth <= availableWidth) {
    return {
      hiddenCount: 0,
      visibleCount: itemWidths.length,
    };
  }
  let visibleWidth = itemWidths.reduce((total, width) => total + width, 0);
  for (let visibleCount = itemWidths.length - 1; visibleCount >= 0; visibleCount -= 1) {
    visibleWidth -= itemWidths[visibleCount] ?? 0;
    const hiddenCount = itemWidths.length - visibleCount;
    const requiredWidth =
      visibleWidth + overflowWidth(hiddenCount) + (visibleCount === 0 ? 0 : gap * visibleCount);
    if (requiredWidth <= availableWidth) {
      return {
        hiddenCount,
        visibleCount,
      };
    }
  }
  return {
    hiddenCount: itemWidths.length,
    visibleCount: 0,
  };
}
