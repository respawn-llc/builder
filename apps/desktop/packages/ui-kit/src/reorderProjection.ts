import type { UniqueIdentifier } from "@dnd-kit/core";

export function projectVerticalReorder<ID extends UniqueIdentifier>(
  ids: readonly ID[],
  activeID: UniqueIdentifier,
  overID: UniqueIdentifier | undefined,
): readonly ID[] | null {
  if (overID === undefined || activeID === overID) {
    return null;
  }
  const indexes = indexesByID(ids);
  const activeIndex = indexes.get(activeID);
  const overIndex = indexes.get(overID);
  if (activeIndex === undefined || overIndex === undefined) {
    return null;
  }
  const orderedIDs = [...ids];
  const [moved] = orderedIDs.splice(activeIndex, 1);
  if (moved === undefined) {
    return null;
  }
  orderedIDs.splice(overIndex, 0, moved);
  return orderedIDs;
}

export function indexesByID(ids: readonly UniqueIdentifier[]): ReadonlyMap<UniqueIdentifier, number> {
  return new Map(ids.map((id, index) => [id, index]));
}
