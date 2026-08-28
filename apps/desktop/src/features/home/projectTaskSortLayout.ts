export type ProjectTaskSortProjection = Readonly<{
  error: boolean;
  isSortReplacement: boolean;
}>;

export type ProjectTaskSortLayoutState = Readonly<{
  previousProjection: readonly ProjectTaskSortProjection[] | null;
  pulse: boolean;
  transition: boolean;
}>;

export type ProjectTaskSortLayoutAction =
  | Readonly<{ kind: "projection-committed"; projection: readonly ProjectTaskSortProjection[] }>
  | Readonly<{ kind: "sort-selected" }>;

export const emptyProjectTaskSortLayoutState: ProjectTaskSortLayoutState = {
  previousProjection: null,
  pulse: false,
  transition: false,
};

export function projectTaskSortLayoutReducer(
  state: ProjectTaskSortLayoutState,
  action: ProjectTaskSortLayoutAction,
): ProjectTaskSortLayoutState {
  if (action.kind === "sort-selected") {
    return { ...state, pulse: true, transition: true };
  }
  return {
    previousProjection: action.projection,
    pulse: false,
    transition: state.transition && action.projection.some((group) => group.isSortReplacement),
  };
}

export function projectTaskSortProjection(
  active: ProjectTaskSortProjection,
  backlog: ProjectTaskSortProjection,
  done: ProjectTaskSortProjection,
): readonly ProjectTaskSortProjection[] {
  return [active, backlog, done];
}

export function previousSortProjectionChanged(
  previous: readonly ProjectTaskSortProjection[] | null,
  current: readonly ProjectTaskSortProjection[],
): boolean {
  if (previous === null) return false;
  return current.some((group, index) => {
    const previousGroup = previous[index];
    if (previousGroup === undefined) {
      throw new Error("Sort projection groups are not aligned.");
    }
    return (
      group.isSortReplacement !== previousGroup.isSortReplacement ||
      (group.error && !previousGroup.error) ||
      (group.isSortReplacement && !group.error && previousGroup.error)
    );
  });
}
