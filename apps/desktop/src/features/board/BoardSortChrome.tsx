import type { BoardNodeCardsSort } from "@/api";
import { SortChrome } from "@/ui";
import { useBoardQuery } from "./BoardQueryRuntime";

export const boardSortFieldOptions = [
  { value: "updated", labelKey: "board.sort.fields.updated" },
  { value: "created", labelKey: "board.sort.fields.created" },
  { value: "labels", labelKey: "board.sort.fields.labels" },
  { value: "short_id", labelKey: "board.sort.fields.short_id" },
] as const satisfies readonly Readonly<{
  value: BoardNodeCardsSort["field"];
  labelKey: string;
}>[];

export function BoardSortChrome() {
  const { setSort, sort } = useBoardQuery();
  return <SortChrome fieldOptions={boardSortFieldOptions} onSortChange={setSort} sort={sort} />;
}
