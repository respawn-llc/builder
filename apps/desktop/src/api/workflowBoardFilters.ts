import {
  canonicalTaskLabelFilter,
  taskLabelFiltersEqual,
  type CanonicalTaskLabelFilter,
  type TaskLabelFilter,
} from "./workflowLabels";

export type BoardDependencyFilter = boolean | null;

export type BoardFilter = Readonly<{
  labelFilter: CanonicalTaskLabelFilter;
  dependencyFilter: BoardDependencyFilter;
}>;

export type BoardFilterInput = BoardFilter | TaskLabelFilter;

export function canonicalBoardFilter(
  input:
    | BoardFilterInput
    | Readonly<{
        labelFilter: TaskLabelFilter;
        dependencyFilter: BoardDependencyFilter;
      }>,
): BoardFilter {
  if ("kind" in input) {
    return { labelFilter: canonicalTaskLabelFilter(input), dependencyFilter: null };
  }
  return {
    labelFilter: canonicalTaskLabelFilter(input.labelFilter),
    dependencyFilter: input.dependencyFilter,
  };
}

export function boardFiltersEqual(left: BoardFilter, right: BoardFilter): boolean {
  return (
    left.dependencyFilter === right.dependencyFilter &&
    taskLabelFiltersEqual(left.labelFilter, right.labelFilter)
  );
}

export function boardFilterWithLabelFilter(filter: BoardFilter, labelFilter: TaskLabelFilter): BoardFilter {
  return canonicalBoardFilter({ labelFilter, dependencyFilter: filter.dependencyFilter });
}

export function boardFilterWithDependencyFilter(
  filter: BoardFilter,
  dependencyFilter: BoardDependencyFilter,
): BoardFilter {
  return { ...filter, dependencyFilter };
}
