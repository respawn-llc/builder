export function lineCountForAssignedHeight({
  assignedHeight,
  lineHeight,
}: Readonly<{
  assignedHeight: number;
  lineHeight: number;
}>): number | null {
  if (
    !Number.isFinite(assignedHeight) ||
    !Number.isFinite(lineHeight) ||
    assignedHeight <= 0 ||
    lineHeight <= 0
  ) {
    return null;
  }
  return Math.max(1, Math.floor(assignedHeight / lineHeight));
}
