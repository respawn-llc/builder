import { useLayoutEffect, useMemo, useRef } from "react";

import type { TaskListItem } from "@/api";
import type { ProjectTaskRenderedColumnWidths } from "./projectTaskColumnLayout";
import { projectTaskGroups, type ProjectTaskListData } from "./projectTaskListData";
import { projectTaskLabelItems } from "./projectTaskLabelItems";

type ProjectTaskMeasurementEntry = Readonly<{
  key: string;
  task: TaskListItem;
}>;

export function ProjectTaskColumnMeasurements({
  data,
  onMeasure,
}: Readonly<{
  data: ProjectTaskListData;
  onMeasure: (widths: ProjectTaskRenderedColumnWidths) => void;
}>) {
  const entries = useMemo(
    () =>
      projectTaskGroups.flatMap((group) =>
        data[group].tasks.map((task): ProjectTaskMeasurementEntry => ({
          key: `${group}-${task.id}`,
          task,
        })),
      ),
    [data],
  );
  const labelRows = useRef(new Map<string, HTMLSpanElement>());
  const workflowNames = useRef(new Map<string, HTMLSpanElement>());

  useLayoutEffect(() => {
    let active = true;
    const measure = () => {
      if (!active) return;
      onMeasure({
        labelsPx: maximumMeasuredWidth(labelRows.current.values()),
        workflowPx: maximumMeasuredWidth(workflowNames.current.values()),
      });
    };
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    for (const element of labelRows.current.values()) {
      observer?.observe(element);
    }
    for (const element of workflowNames.current.values()) {
      observer?.observe(element);
    }
    measure();
    return () => {
      active = false;
      observer?.disconnect();
    };
  }, [entries, onMeasure]);

  return (
    <div
      aria-hidden="true"
      className="pointer-events-none invisible fixed left-0 top-0 -z-10 grid w-max"
      inert
    >
      {entries.map((entry) => (
        <TaskColumnMeasurement
          key={entry.key}
          labelRef={(element) => {
            retainMeasurementElement(labelRows.current, entry.key, element);
          }}
          task={entry.task}
          workflowRef={(element) => {
            retainMeasurementElement(workflowNames.current, entry.key, element);
          }}
        />
      ))}
    </div>
  );
}

function TaskColumnMeasurement({
  labelRef,
  task,
  workflowRef,
}: Readonly<{
  labelRef: (element: HTMLSpanElement | null) => void;
  task: TaskListItem;
  workflowRef: (element: HTMLSpanElement | null) => void;
}>) {
  return (
    <span className="flex w-max items-center">
      <span
        className="inline-flex w-max max-w-[320px] items-center gap-[var(--space-1)] overflow-hidden"
        ref={labelRef}
      >
        {projectTaskLabelItems(task.labels).map((item) => (
          <span className="shrink-0" key={item.id}>
            {item.content}
          </span>
        ))}
      </span>
      <span
        className="inline-block w-max max-w-[24ch] overflow-hidden whitespace-nowrap text-xs"
        ref={workflowRef}
      >
        {task.workflowName}
      </span>
    </span>
  );
}

function retainMeasurementElement(
  elements: Map<string, HTMLSpanElement>,
  key: string,
  element: HTMLSpanElement | null,
): void {
  if (element === null) {
    elements.delete(key);
    return;
  }
  elements.set(key, element);
}

function maximumMeasuredWidth(elements: Iterable<HTMLSpanElement>): number {
  let width = 0;
  for (const element of elements) {
    width = Math.max(width, Math.ceil(element.getBoundingClientRect().width));
  }
  return width;
}
