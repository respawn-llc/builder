import { useEffect, useMemo, useRef } from "react";

export type VirtualizedItemVisibilityTrigger = Readonly<{
  itemKey: string;
  requestGeneration: string;
  enabled: boolean;
  fetching: boolean;
  onVisible: () => void;
}>;

export function useVirtualizedItemVisibilityTriggers<TItem>({
  getItemKey,
  itemStartIndex,
  items,
  triggers = [],
  visibleIndexes,
}: Readonly<{
  getItemKey: (item: TItem) => string;
  itemStartIndex: number;
  items: readonly TItem[];
  triggers?: readonly VirtualizedItemVisibilityTrigger[] | undefined;
  visibleIndexes: readonly number[];
}>): void {
  const handledGenerationsRef = useRef(new Set<string>());
  const visibleItemKeys = useMemo(
    () =>
      new Set(
        visibleIndexes.flatMap((index) => {
          const item = items[index - itemStartIndex];
          return item === undefined ? [] : [getItemKey(item)];
        }),
      ),
    [getItemKey, itemStartIndex, items, visibleIndexes],
  );
  useEffect(() => {
    for (const trigger of triggers) {
      const identity = `${trigger.itemKey}\u0000${trigger.requestGeneration}`;
      if (
        trigger.enabled &&
        !trigger.fetching &&
        visibleItemKeys.has(trigger.itemKey) &&
        !handledGenerationsRef.current.has(identity)
      ) {
        handledGenerationsRef.current.add(identity);
        trigger.onVisible();
      }
    }
  }, [triggers, visibleItemKeys]);
}
