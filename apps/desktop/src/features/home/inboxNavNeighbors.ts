export type InboxNavNeighbors = Readonly<{
  previousTaskID: string | null;
  nextTaskID: string | null;
  anchorIndex: number;
}>;

/**
 * Collapses the live Inbox task IDs into their ordered, de-duplicated navigation
 * sequence. A task may raise multiple attention rows but occupies one position.
 */
export function orderedInboxTaskIDs(taskIDs: readonly string[]): readonly string[] {
  const seen = new Set<string>();
  const ordered: string[] = [];
  for (const taskID of taskIDs) {
    if (seen.has(taskID)) {
      continue;
    }
    seen.add(taskID);
    ordered.push(taskID);
  }
  return ordered;
}

/**
 * Resolves the Previous/Next targets for the currently open inbox task against
 * the live list. When the open task is still present, neighbors are its direct
 * siblings. When it has just been resolved and dropped out of the inbox, the
 * caller-supplied `lastAnchorIndex` (the open task's last known position) is used
 * so Next lands on whatever item now occupies that slot — i.e. the successor that
 * shifted up into the resolved task's place.
 */
export function inboxNavNeighbors(
  taskIDs: readonly string[],
  currentTaskID: string,
  lastAnchorIndex: number,
): InboxNavNeighbors {
  const found = taskIDs.indexOf(currentTaskID);
  if (found >= 0) {
    return {
      previousTaskID: at(taskIDs, found - 1),
      nextTaskID: at(taskIDs, found + 1),
      anchorIndex: found,
    };
  }
  const anchor = Math.min(Math.max(lastAnchorIndex, 0), taskIDs.length);
  return {
    previousTaskID: at(taskIDs, anchor - 1),
    nextTaskID: at(taskIDs, anchor),
    anchorIndex: anchor,
  };
}

function at(taskIDs: readonly string[], index: number): string | null {
  if (index < 0 || index >= taskIDs.length) {
    return null;
  }
  return taskIDs[index] ?? null;
}
