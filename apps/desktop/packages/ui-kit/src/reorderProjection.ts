import type { UniqueIdentifier } from "@dnd-kit/core";

export type VerticalReorderProjection<ID extends UniqueIdentifier> = Readonly<{
  insertionIndex: number | undefined;
  orderedIDs: readonly ID[] | null;
}>;

export function projectVerticalReorder<ID extends UniqueIdentifier>(
  ids: readonly ID[],
  activeID: UniqueIdentifier,
  overID: UniqueIdentifier | undefined,
): VerticalReorderProjection<ID> {
  if (overID === undefined || activeID === overID) {
    return { insertionIndex: undefined, orderedIDs: null };
  }
  const indexes = indexesByID(ids);
  const activeIndex = indexes.get(activeID);
  const overIndex = indexes.get(overID);
  if (activeIndex === undefined || overIndex === undefined) {
    return { insertionIndex: undefined, orderedIDs: null };
  }
  const orderedIDs = [...ids];
  const [moved] = orderedIDs.splice(activeIndex, 1);
  if (moved === undefined) {
    return { insertionIndex: undefined, orderedIDs: null };
  }
  orderedIDs.splice(overIndex, 0, moved);
  return {
    insertionIndex: activeIndex < overIndex ? overIndex + 1 : overIndex,
    orderedIDs,
  };
}

export function indexesByID(ids: readonly UniqueIdentifier[]): ReadonlyMap<UniqueIdentifier, number> {
  return new Map(ids.map((id, index) => [id, index]));
}
