export function virtualizedInitialScrollIndex<TItem>({
  getItemKey,
  headerCount,
  initialScrollKey,
  items,
}: Readonly<{
  getItemKey: (item: TItem) => string;
  headerCount: number;
  initialScrollKey: string | undefined;
  items: readonly TItem[];
}>): number | null {
  if (initialScrollKey === undefined) {
    return null;
  }
  const itemIndex = items.findIndex((item) => getItemKey(item) === initialScrollKey);
  return itemIndex < 0 ? null : headerCount + itemIndex;
}

export function resolveVirtualizedInitialScroll<TItem>({
  getItemKey,
  headerCount,
  initialScrollKey,
  initialScrollRequestKey,
  items,
  lastRequestKey,
}: Readonly<{
  getItemKey: (item: TItem) => string;
  headerCount: number;
  initialScrollKey: string | undefined;
  initialScrollRequestKey: string | undefined;
  items: readonly TItem[];
  lastRequestKey: string | null;
}>): Readonly<{ requestKey: string; scrollIndex: number }> | null {
  if (initialScrollKey === undefined) {
    return null;
  }
  const requestKey = initialScrollRequestKey ?? initialScrollKey;
  if (lastRequestKey === requestKey) {
    return null;
  }
  const scrollIndex = virtualizedInitialScrollIndex({
    getItemKey,
    headerCount,
    initialScrollKey,
    items,
  });
  return scrollIndex === null ? null : { requestKey, scrollIndex };
}
