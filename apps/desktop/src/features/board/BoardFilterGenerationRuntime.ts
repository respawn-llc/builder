import { createContext, useContext } from "react";

import type { BoardNodeCardsSort } from "@/api";
import type {
  BoardFilterGenerationController,
  BoardFilterGenerationSnapshot,
} from "./BoardFilterGenerationController";
import type { BoardGenerationQueryRegistry } from "./BoardGenerationQueryRegistry";
import type { BoardGenerationRequestAdapter } from "./BoardGenerationRequestAdapter";

export type BoardFilterGenerationRuntime = Readonly<{
  controller: BoardFilterGenerationController;
  queryRegistry: BoardGenerationQueryRegistry;
  requestAdapter: BoardGenerationRequestAdapter;
  queriesEnabled: boolean;
  snapshot: BoardFilterGenerationSnapshot;
  sort: BoardNodeCardsSort;
  setSort: (sort: BoardNodeCardsSort) => void;
}>;

export const BoardFilterGenerationContext = createContext<BoardFilterGenerationRuntime | null>(null);

export function useBoardFilterGeneration(): BoardFilterGenerationRuntime {
  const value = useContext(BoardFilterGenerationContext);
  if (value === null) {
    throw new Error("BoardFilterGenerationProvider is required");
  }
  return value;
}
