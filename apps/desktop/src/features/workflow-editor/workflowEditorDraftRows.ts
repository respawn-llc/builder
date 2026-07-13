import { z } from "zod";

import type { WorkflowParameter } from "../../api";

const workflowParameterRowIDSchema = z.string();

export function draftParameterRowID(parameter: WorkflowParameter): string | undefined {
  if (!("rowID" in parameter)) {
    return undefined;
  }
  const rowID = workflowParameterRowIDSchema.safeParse(parameter.rowID);
  return rowID.success ? rowID.data : undefined;
}

export function reorderDraftRows<T extends Readonly<{ rowID?: string }>>(
  rows: readonly T[],
  activeRowID: string,
  overRowID: string,
): readonly T[] {
  const activeIndex = rows.findIndex((row) => row.rowID === activeRowID);
  const overIndex = rows.findIndex((row) => row.rowID === overRowID);
  if (activeIndex < 0 || overIndex < 0 || activeIndex === overIndex) {
    return rows;
  }
  const next = [...rows];
  const [item] = next.splice(activeIndex, 1);
  if (item === undefined) {
    return rows;
  }
  next.splice(overIndex, 0, item);
  return next;
}
