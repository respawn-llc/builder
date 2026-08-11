import { createContext, useContext } from "react";

import type { BoardFilter, BoardNodeCardsSort } from "@/api";

export type BoardQueryState = Readonly<{
  filter: BoardFilter;
  queriesEnabled: boolean;
  setDependencyFilter: (filter: boolean | null) => void;
  setSort: (sort: BoardNodeCardsSort) => void;
  sort: BoardNodeCardsSort;
}>;

export const BoardQueryContext = createContext<BoardQueryState | null>(null);

export function useBoardQuery(): BoardQueryState {
  const value = useContext(BoardQueryContext);
  if (value === null) {
    throw new Error("BoardQueryProvider is required");
  }
  return value;
}
