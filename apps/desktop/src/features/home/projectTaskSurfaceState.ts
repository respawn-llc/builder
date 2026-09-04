import type { ReactNode } from "react";

import type { VirtualizedInfiniteListBoundaryState } from "@/ui";
import {
  projectTaskGroups,
  type ProjectTaskGroupDisclosure,
  type ProjectTaskListData,
} from "./projectTaskListData";

export function projectTaskWorkflowStrip(
  boundary: VirtualizedInfiniteListBoundaryState | undefined,
  workflowCount: number,
  strip: ReactNode,
): ReactNode {
  return boundary === undefined && workflowCount === 0 ? null : strip;
}

export function projectTaskWorkflowInitialState(
  established: boolean,
  failed: boolean,
  loading: boolean,
): Readonly<{ failed: boolean; loading: boolean }> {
  return {
    failed: !established && failed,
    loading: !established && loading && !failed,
  };
}

export function projectTaskScrollRestorationReady(
  data: ProjectTaskListData,
  disclosure: ProjectTaskGroupDisclosure,
): boolean {
  const counts = data.counts.data?.counts;
  return (
    counts !== undefined &&
    projectTaskGroups.every(
      (group) => !disclosure[group] || counts[group] === 0 || data[group].pages.length > 0,
    )
  );
}
