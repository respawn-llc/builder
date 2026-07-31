import type { ProjectLabel } from "@/api";

export function selectOrderedProjectLabels(
  catalog: readonly ProjectLabel[] | undefined,
  assignedLabelIDs: readonly string[],
): readonly ProjectLabel[] {
  if (catalog === undefined || assignedLabelIDs.length === 0) {
    return [];
  }
  const assigned = new Set(assignedLabelIDs);
  return catalog.filter((label) => assigned.has(label.id));
}
