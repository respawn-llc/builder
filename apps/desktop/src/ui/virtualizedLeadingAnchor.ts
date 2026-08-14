export type VirtualizedLeadingAnchor = Readonly<{
  anchorKey: string;
  occurrenceKey: string;
  inRowOffset: number;
}>;

export type VirtualizedAnchorEntry = Readonly<{
  anchorKey: string;
  occurrenceKey: string;
}>;

export function recoverableLeadingAnchor(
  anchor: VirtualizedLeadingAnchor | null,
  appliedPixelOffsetKey: string | null,
  requestKey: string | undefined,
): VirtualizedLeadingAnchor | null {
  return appliedPixelOffsetKey === requestKey ? null : anchor;
}

function findAnchorEntryIndex(
  entries: readonly VirtualizedAnchorEntry[],
  anchor: VirtualizedLeadingAnchor,
  includeOccurrence: boolean,
): number {
  return entries.findIndex(
    (entry) =>
      entry.anchorKey === anchor.anchorKey &&
      (!includeOccurrence || entry.occurrenceKey === anchor.occurrenceKey),
  );
}

export function resolveAnchorEntryIndex(
  entries: readonly VirtualizedAnchorEntry[],
  anchor: VirtualizedLeadingAnchor,
): number {
  const occurrenceIndex = findAnchorEntryIndex(entries, anchor, true);
  return occurrenceIndex >= 0 ? occurrenceIndex : findAnchorEntryIndex(entries, anchor, false);
}
