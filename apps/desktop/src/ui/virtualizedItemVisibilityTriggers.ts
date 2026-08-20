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
  const triggerStatesRef = useRef(new Map<string, Readonly<{ generation: string; handled: boolean }>>());
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
    const currentItemKeys = new Set(triggers.map((trigger) => trigger.itemKey));
    for (const itemKey of triggerStatesRef.current.keys()) {
      if (!currentItemKeys.has(itemKey)) {
        triggerStatesRef.current.delete(itemKey);
      }
    }
    for (const trigger of triggers) {
      const previousState = triggerStatesRef.current.get(trigger.itemKey);
      const state =
        previousState?.generation === trigger.requestGeneration
          ? previousState
          : { generation: trigger.requestGeneration, handled: false };
      triggerStatesRef.current.set(trigger.itemKey, state);
      if (trigger.enabled && !trigger.fetching && visibleItemKeys.has(trigger.itemKey) && !state.handled) {
        triggerStatesRef.current.set(trigger.itemKey, {
          generation: trigger.requestGeneration,
          handled: true,
        });
        trigger.onVisible();
      }
    }
  }, [triggers, visibleItemKeys]);
}
