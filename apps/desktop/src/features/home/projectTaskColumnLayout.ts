import { useLayoutEffect, useRef, useState, type CSSProperties, type RefObject } from "react";

import { projectTaskGroups, type ProjectTaskListData } from "./projectTaskListData";

export type ProjectTaskColumnLayout = Readonly<{
  dependenciesPx: number;
  idCharacters: number;
  labelsPx: number;
  workflowCharacters: number;
}>;

type ProjectTaskVisibleColumns = Readonly<{
  dependencies: boolean;
  labels: boolean;
  title: boolean;
  workflow: boolean;
}>;

const initialProjectTaskColumnLayout: ProjectTaskColumnLayout = {
  dependenciesPx: 18,
  idCharacters: 2,
  labelsPx: 48,
  workflowCharacters: 8,
};

export function useProjectTaskColumnLayout(data: ProjectTaskListData): ProjectTaskColumnLayout {
  const measured = measureProjectTaskColumns(data);
  const [layout, setLayout] = useState(initialProjectTaskColumnLayout);
  const expanded = expandProjectTaskColumnLayout(layout, measured);
  if (!projectTaskColumnLayoutsEqual(layout, expanded)) {
    setLayout(expanded);
    return expanded;
  }
  return layout;
}

function expandProjectTaskColumnLayout(
  current: ProjectTaskColumnLayout,
  measured: ProjectTaskColumnLayout,
): ProjectTaskColumnLayout {
  return {
    dependenciesPx: Math.max(current.dependenciesPx, measured.dependenciesPx),
    idCharacters: Math.max(current.idCharacters, measured.idCharacters),
    labelsPx: Math.max(current.labelsPx, measured.labelsPx),
    workflowCharacters: Math.max(current.workflowCharacters, measured.workflowCharacters),
  };
}

function measureProjectTaskColumns(data: ProjectTaskListData): ProjectTaskColumnLayout {
  return projectTaskGroups
    .flatMap((group) => data[group].tasks)
    .reduce<ProjectTaskColumnLayout>(
      (layout, task) => ({
        dependenciesPx: Math.max(layout.dependenciesPx, dependencyChipWidthPx(task.dependencyProgress)),
        idCharacters: Math.max(layout.idCharacters, Math.min(18, task.shortID.length)),
        labelsPx: Math.max(layout.labelsPx, taskLabelsWidthPx(task.labels.map((label) => label.name))),
        workflowCharacters: Math.max(layout.workflowCharacters, Math.min(24, task.workflowName?.length ?? 0)),
      }),
      initialProjectTaskColumnLayout,
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

function taskLabelsWidthPx(names: readonly string[]): number {
  if (names.length === 0) {
    return initialProjectTaskColumnLayout.labelsPx;
  }
  const contentWidth = names.reduce((width, name) => width + 14 + name.length * 7, 0);
  return Math.min(320, contentWidth + Math.max(0, names.length - 1) * 4);
}

function projectTaskColumnLayoutsEqual(
  left: ProjectTaskColumnLayout,
  right: ProjectTaskColumnLayout,
): boolean {
  return (
    left.dependenciesPx === right.dependenciesPx &&
    left.idCharacters === right.idCharacters &&
    left.labelsPx === right.labelsPx &&
    left.workflowCharacters === right.workflowCharacters
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
    visible.labels ? `${layout.labelsPx.toString()}px` : null,
    visible.workflow ? `${layout.workflowCharacters.toString()}ch` : null,
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
    return { dependencies: true, labels: true, title: true, workflow: true };
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

  const optionalWidths = [
    layout.dependenciesPx,
    layout.labelsPx,
    layout.workflowCharacters * characterWidthPx,
  ];
  const workflow = requiredWidth(optionalWidths) <= widthPx;
  const withoutWorkflow = optionalWidths.slice(0, 2);
  const labels = workflow || requiredWidth(withoutWorkflow) <= widthPx;
  const withoutLabels = [layout.dependenciesPx];
  const dependencies = labels || requiredWidth(withoutLabels) <= widthPx;
  const title = dependencies || requiredWidth([]) <= widthPx;
  return { dependencies, labels, title, workflow };
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
