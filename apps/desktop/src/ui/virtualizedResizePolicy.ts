export function shouldAdjustScrollForVirtualizedResize(
  nonAdjustingItemKey: string | undefined,
  itemKey: string,
): boolean {
  return nonAdjustingItemKey !== itemKey;
}
