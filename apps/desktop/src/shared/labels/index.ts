import type { ProjectLabel, ProjectLabelCatalog } from "@/api";

export function orderedAssignedLabels(
  catalog: ProjectLabelCatalog | null,
  assignedLabelIDs: readonly string[],
): readonly ProjectLabel[] {
  if (catalog === null) {
    return [];
  }
  const assigned = new Set(assignedLabelIDs);
  return catalog.labels.filter((label) => assigned.has(label.id));
}

export {
  createLabelFilterState,
  reconcileLabelFilterState,
  reduceLabelFilterState,
  type LabelFilterAction,
  type LabelFilterState,
} from "./labelFilterState";
export {
  readPersistedLabelFilterState,
  writePersistedLabelFilterState,
  type LabelFilterPersistenceReadResult,
} from "./labelFilterPersistence";
export { ProjectLabelsProvider } from "./ProjectLabelsProvider";
export {
  useProjectLabelCatalog,
  useProjectLabelCatalogMutations,
  useProjectLabelEffects,
  useProjectLabelFilter,
} from "./projectLabelHooks";
export {
  LabelFilterStorageNamespaceError,
  type LabelFilterPersistenceStatus,
  type ProjectLabelFilterController,
} from "./projectLabelFilter";
export {
  patchExistingTaskLabelAssignment,
  patchExistingTaskLabelProjections,
  pruneDeletedLabelFromExistingCaches,
  removeDeletedTaskFromExistingCaches,
} from "./taskLabelCache";
export { type TaskLabelAssignmentData, type TaskLabelAssignmentFailure } from "./taskLabelAssignmentData";
export { TaskLabelAssignmentProvider } from "./TaskLabelAssignmentProvider";
export { TaskLabelAssignmentFeedback } from "./TaskLabelAssignmentFeedback";
export { useTaskLabelAssignment } from "./taskLabelAssignmentContext";
export { LabelChooser, type LabelChooserInvocation, type LabelChooserProps } from "./LabelChooser";
export { createProjectLabelEffects, type ProjectLabelEffects } from "./labelEventEffects";
