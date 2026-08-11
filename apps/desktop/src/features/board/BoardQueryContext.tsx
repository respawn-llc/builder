import { useMemo, useState, type ReactNode } from "react";

import {
  canonicalBoardFilter,
  defaultBoardNodeCardsSort,
  type BoardNodeCardsSort,
  type TaskLabelFilter,
} from "@/api";
import { BoardQueryContext } from "./BoardQueryRuntime";

export function BoardQueryProvider({
  children,
  labelFilter,
  queriesEnabled = true,
}: Readonly<{
  children: ReactNode;
  labelFilter: TaskLabelFilter;
  queriesEnabled?: boolean;
}>) {
  const [dependencyFilter, setDependencyFilter] = useState<boolean | null>(null);
  const [sort, setSort] = useState<BoardNodeCardsSort>(defaultBoardNodeCardsSort);
  const filter = useMemo(
    () => canonicalBoardFilter({ dependencyFilter, labelFilter }),
    [dependencyFilter, labelFilter],
  );
  const value = useMemo(
    () => ({ filter, queriesEnabled, setDependencyFilter, setSort, sort }),
    [filter, queriesEnabled, sort],
  );
  return <BoardQueryContext.Provider value={value}>{children}</BoardQueryContext.Provider>;
}
