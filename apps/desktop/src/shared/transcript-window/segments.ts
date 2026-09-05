import { chatTranscriptCommittedRowSchema, ContractError } from "@/api";
import { replaceEqualDeep } from "@tanstack/react-query";

import { committedItem, locatorKey } from "./renderSlots";
import type { CommittedRow, ResidentSegments, Segment, TranscriptPageRequest } from "./types";

export function compareRows(left: CommittedRow, right: CommittedRow): number {
  return (
    left.Locator.event_sequence - right.Locator.event_sequence ||
    left.Locator.row_ordinal - right.Locator.row_ordinal
  );
}

function validateBoundary(hasMore: boolean, cursor: number | null): void {
  if (hasMore !== (cursor !== null) || (cursor !== null && (!Number.isInteger(cursor) || cursor <= 0))) {
    throw new ContractError("Transcript boundary must carry a positive cursor exactly when history exists.");
  }
}

export function validateSegment(segment: Segment): void {
  validateBoundary(segment.hasMoreAbove, segment.olderCursor);
  validateBoundary(segment.hasMoreBelow, segment.newerCursor);
  let previous: CommittedRow | null = null;
  for (const row of segment.entries) {
    const parsed = chatTranscriptCommittedRowSchema.safeParse(row);
    if (!parsed.success) throw new ContractError(parsed.error.message);
    committedItem(row);
    if (previous !== null && compareRows(previous, row) >= 0) {
      throw new ContractError("Transcript segment locators must be unique and in committed order.");
    }
    previous = row;
  }
}

export function validateTail(segment: Segment): void {
  validateSegment(segment);
  if (segment.hasMoreBelow || segment.newerCursor !== null) {
    throw new ContractError("Transcript tail must have a closed newer boundary.");
  }
}

export function validateAdjacent(segment: Segment, request: TranscriptPageRequest): void {
  validateSegment(segment);
  const adjacent =
    request.direction === "older"
      ? segment.hasMoreBelow && segment.newerCursor === request.cursor
      : segment.hasMoreAbove && segment.olderCursor === request.cursor;
  if (!adjacent) throw new ContractError("Transcript page does not adjoin the requested edge cursor.");
}

function authoritativePayload(row: CommittedRow): CommittedRow {
  if (row.ReasoningTrace === null) return row;
  // Provisional correlation is not part of an authoritative committed Reasoning payload.
  return { ...row, ReasoningTrace: { ...row.ReasoningTrace, ProvisionalIdentity: null } };
}

/** Recurrent rows reference the resident source payload, including rows about to be evicted. */
export function shareSegment(
  segment: Segment,
  resident: ResidentSegments,
  pool: readonly CommittedRow[] = [],
): Segment {
  const sources = new Map(
    [...pool, ...resident.flatMap((shell) => shell.entries)].map((row) => [locatorKey(row), row] as const),
  );
  const entries = segment.entries.map((row) => {
    const source = sources.get(locatorKey(row));
    if (source === undefined || source === row) return row;
    const previous = authoritativePayload(source);
    if (replaceEqualDeep(previous, authoritativePayload(row)) !== previous) {
      throw new ContractError("Recurrent transcript locator carries an incompatible committed payload.");
    }
    return source;
  });
  return { ...segment, entries };
}

/** Merge at most two sorted source arrays; overlap has already been validated and shared. */
export function residentRows(segments: ResidentSegments): readonly CommittedRow[] {
  const left = segments[0]?.entries ?? [];
  const right = segments[1]?.entries ?? [];
  return mergeRows(left, right);
}

export function mergeRows(
  left: readonly CommittedRow[],
  right: readonly CommittedRow[],
): readonly CommittedRow[] {
  const rows: CommittedRow[] = [];
  let leftIndex = 0;
  let rightIndex = 0;
  while (leftIndex < left.length || rightIndex < right.length) {
    const first = left[leftIndex];
    const second = right[rightIndex];
    if (first === undefined) {
      return rows.concat(right.slice(rightIndex));
    }
    if (second === undefined) {
      return rows.concat(left.slice(leftIndex));
    }
    const order = compareRows(first, second);
    rows.push(order <= 0 ? first : second);
    if (order <= 0) leftIndex++;
    if (order >= 0) rightIndex++;
  }
  return rows;
}

export function withLivePool(segments: ResidentSegments, pool: readonly CommittedRow[]): ResidentSegments {
  const tail = segments.at(-1);
  if (tail === undefined || tail.hasMoreBelow) return segments;
  const merged = { ...tail, entries: mergeRows(tail.entries, pool) };
  return segments.length === 2 ? [segments[0], merged] : [merged];
}

export function rowBatch(entries: readonly CommittedRow[]): Segment {
  return { entries, olderCursor: null, hasMoreAbove: false, newerCursor: null, hasMoreBelow: false };
}
