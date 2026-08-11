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
export {
  LABEL_COMPARISON_VERSION,
  compareLabelNames,
  foldLabelText,
  labelNameContains,
  labelNamesEqual,
} from "./labelComparison";
export { createProjectCatalogAuthority, type ProjectCatalogAuthority } from "./projectCatalogAuthority";
export { ProjectLabelsProvider } from "./ProjectLabelsProvider";
export {
  useProjectCatalogAuthority,
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
  createTaskLabelAssignmentController,
  type TaskLabelAssignmentController,
  type TaskLabelAssignmentFailure,
  type TaskLabelReconciliationFailure,
  type TaskLabelAssignmentSnapshot,
  type TaskLabelUpdateInput,
} from "./taskLabelAssignmentController";
export {
  patchExistingTaskLabelAssignment,
  patchExistingTaskLabelProjections,
  pruneDeletedLabelFromExistingCaches,
  removeDeletedTaskFromExistingCaches,
} from "./taskLabelCache";
export { type TaskLabelAssignmentData } from "./taskLabelAssignmentData";
export { TaskLabelAssignmentProvider } from "./TaskLabelAssignmentProvider";
export { useTaskLabelAssignment } from "./taskLabelAssignmentContext";
export { LabelChooser, type LabelChooserInvocation, type LabelChooserProps } from "./LabelChooser";
export { createProjectLabelEffects, type ProjectLabelEffects } from "./labelEventEffects";
