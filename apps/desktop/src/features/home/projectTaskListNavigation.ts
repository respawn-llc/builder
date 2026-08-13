import {
  projectTaskGroups,
  type ProjectTaskGroup,
  type ProjectTaskGroupDisclosure,
  type ProjectTaskGroupAnchors,
  type ProjectTaskListData,
} from "./projectTaskListData";

export function firstExpandedProjectTaskGroup(
  counts: Readonly<Record<ProjectTaskGroup, number>> | undefined,
  disclosure: ProjectTaskGroupDisclosure,
): ProjectTaskGroup | null {
  if (counts === undefined) return null;
  return projectTaskGroups.find((group) => counts[group] > 0 && disclosure[group]) ?? null;
}

export function projectTaskTopNavigationRequiresRequest(
  counts: Readonly<Record<ProjectTaskGroup, number>> | undefined,
  disclosure: ProjectTaskGroupDisclosure,
  anchors: ProjectTaskGroupAnchors,
): boolean {
  const group = firstExpandedProjectTaskGroup(counts, disclosure);
  return group !== null && anchors[group] !== 0;
}

export function topNavigationScrollRequest(
  request: Readonly<{ group: ProjectTaskGroup; key: string }> | null,
  data: ProjectTaskListData,
  disclosure: ProjectTaskGroupDisclosure,
) {
  if (
    request === null ||
    !disclosure[request.group] ||
    !data[request.group].pageParams.includes(0) ||
    data[request.group].isPlaceholderData
  ) {
    return undefined;
  }
  return { key: request.key, target: "top" as const };
}
