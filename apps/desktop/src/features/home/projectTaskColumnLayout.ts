import { useCallback, useLayoutEffect, useRef, useState, type CSSProperties, type RefObject } from "react";

import { projectTaskGroups, type ProjectTaskListData } from "./projectTaskListData";

export type ProjectTaskColumnLayout = Readonly<{
  dependenciesPx: number;
  idCharacters: number;
  labelsPx: number;
  workflowPx: number;
}>;

export type ProjectTaskRenderedColumnWidths = Readonly<{
  labelsPx: number;
  workflowPx: number;
}>;

type ProjectTaskStructuralColumnWidths = Readonly<{
  dependenciesPx: number;
  idCharacters: number;
}>;

type ProjectTaskVisibleColumns = Readonly<{
  dependencies: boolean;
  labelsPx: number | null;
  title: boolean;
  workflow: boolean;
}>;

const initialProjectTaskColumnLayout: ProjectTaskColumnLayout = {
  dependenciesPx: 18,
  idCharacters: 2,
  labelsPx: 48,
  workflowPx: 64,
};

export function useProjectTaskColumnLayout(data: ProjectTaskListData): Readonly<{
  layout: ProjectTaskColumnLayout;
  retainRenderedWidths: (widths: ProjectTaskRenderedColumnWidths) => void;
}> {
  const measured = measureProjectTaskColumns(data);
  const [layout, setLayout] = useState(initialProjectTaskColumnLayout);
  const retainRenderedWidths = useCallback(
    (widths: ProjectTaskRenderedColumnWidths) => {
      setLayout((current) => {
        const expanded = {
          ...current,
          labelsPx: Math.max(current.labelsPx, widths.labelsPx),
          workflowPx: Math.max(current.workflowPx, widths.workflowPx),
        };
        return projectTaskColumnLayoutsEqual(current, expanded) ? current : expanded;
      });
    },
    [setLayout],
  );
  const expanded = expandProjectTaskColumnLayout(layout, measured);
  if (!projectTaskColumnLayoutsEqual(layout, expanded)) {
    setLayout(expanded);
    return { layout: expanded, retainRenderedWidths };
  }
  return { layout, retainRenderedWidths };
}

function expandProjectTaskColumnLayout(
  current: ProjectTaskColumnLayout,
  measured: ProjectTaskStructuralColumnWidths,
): ProjectTaskColumnLayout {
  return {
    ...current,
    dependenciesPx: Math.max(current.dependenciesPx, measured.dependenciesPx),
    idCharacters: Math.max(current.idCharacters, measured.idCharacters),
  };
}

function measureProjectTaskColumns(data: ProjectTaskListData): ProjectTaskStructuralColumnWidths {
  return projectTaskGroups
    .flatMap((group) => data[group].tasks)
    .reduce<ProjectTaskStructuralColumnWidths>(
      (layout, task) => ({
        dependenciesPx: Math.max(layout.dependenciesPx, dependencyChipWidthPx(task.dependencyProgress)),
        idCharacters: Math.max(layout.idCharacters, Math.min(18, task.shortID.length)),
      }),
      {
        dependenciesPx: initialProjectTaskColumnLayout.dependenciesPx,
        idCharacters: initialProjectTaskColumnLayout.idCharacters,
      },
    );
}

function dependencyChipWidthPx(
  progress: Readonly<{ satisfiedCount: number; totalCount: number }> | null,
): number {
  if (progress === null) {
    return initialProjectTaskColumnLayout.dependenciesPx;
  }
  const textLength = `${progress.satisfiedCount.toString()}/${progress.totalCount.toString()}`.length;
  return Math.min(76, 29 + textLength * 7);
}

function projectTaskColumnLayoutsEqual(
  left: ProjectTaskColumnLayout,
  right: ProjectTaskColumnLayout,
): boolean {
  return (
    left.dependenciesPx === right.dependenciesPx &&
    left.idCharacters === right.idCharacters &&
    left.labelsPx === right.labelsPx &&
    left.workflowPx === right.workflowPx
  );
}

type ProjectTaskColumnStyle = CSSProperties &
  Readonly<{
    "--project-task-grid-columns": string;
  }>;

export function projectTaskColumnStyle(
  layout: ProjectTaskColumnLayout,
  visible: ProjectTaskVisibleColumns,
): ProjectTaskColumnStyle {
  const columns = [
    "16px",
    `${layout.idCharacters.toString()}ch`,
    visible.title ? "minmax(7ch, 1fr)" : null,
    visible.dependencies ? `${layout.dependenciesPx.toString()}px` : null,
    visible.labelsPx === null ? null : `${visible.labelsPx.toString()}px`,
    visible.workflow ? `${layout.workflowPx.toString()}px` : null,
  ].filter((column): column is string => column !== null);
  return {
    "--project-task-grid-columns": columns.join(" "),
  };
}

export function resolveProjectTaskVisibleColumns(
  widthPx: number | null,
  layout: ProjectTaskColumnLayout,
): ProjectTaskVisibleColumns {
  if (widthPx === null) {
    return { dependencies: true, labelsPx: layout.labelsPx, title: true, workflow: true };
  }
  const characterWidthPx = 8;
  const horizontalPaddingPx = 24;
  const gapPx = 12;
  const scrollbarGutterPx = 16;
  const titleMinimumPx = 7 * characterWidthPx;
  const idWidthPx = layout.idCharacters * characterWidthPx;
  const requiredWidth = (optionalWidths: readonly number[]) =>
    horizontalPaddingPx +
    scrollbarGutterPx +
    16 +
    idWidthPx +
    titleMinimumPx +
    optionalWidths.reduce((total, columnWidth) => total + columnWidth, 0) +
    gapPx * (2 + optionalWidths.length);

  const optionalWidths = [layout.dependenciesPx, layout.labelsPx, layout.workflowPx];
  const workflow = requiredWidth(optionalWidths) <= widthPx;
  const labelsAvailablePx = widthPx - requiredWidth([layout.dependenciesPx, 0]);
  let labelsPx: number | null = null;
  if (workflow) {
    labelsPx = layout.labelsPx;
  } else if (labelsAvailablePx >= initialProjectTaskColumnLayout.labelsPx) {
    labelsPx = Math.min(layout.labelsPx, labelsAvailablePx);
  }
  const withoutLabels = [layout.dependenciesPx];
  const dependencies = labelsPx !== null || requiredWidth(withoutLabels) <= widthPx;
  const title = dependencies || requiredWidth([]) <= widthPx;
  return { dependencies, labelsPx, title, workflow };
}

export function useProjectTaskListWidth(): Readonly<{
  containerRef: RefObject<HTMLDivElement | null>;
  widthPx: number | null;
}> {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [widthPx, setWidthPx] = useState<number | null>(null);
  useLayoutEffect(() => {
    const container = containerRef.current;
    if (container === null) {
      return;
    }
    const measure = () => {
      setWidthPx(container.getBoundingClientRect().width);
    };
    measure();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(container);
    return () => {
      observer?.disconnect();
    };
  }, []);
  return { containerRef, widthPx };
}
