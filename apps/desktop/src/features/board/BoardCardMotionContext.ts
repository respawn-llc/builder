import { createContext, useContext, type CSSProperties } from "react";

import type { BoardCardInstance } from "./BoardCardInstance";

export type BoardCardMotionContextValue = Readonly<{
  cardStyle: (cardID: string) => CSSProperties | undefined;
  cardClassName: (cardID: string) => string | undefined;
  registerCard: (instance: BoardCardInstance, element: HTMLElement | null) => void;
}>;

export const BoardCardMotionContext = createContext<BoardCardMotionContextValue>({
  cardClassName: () => undefined,
  cardStyle: () => undefined,
  registerCard: () => undefined,
});

export function useBoardCardMotion(): BoardCardMotionContextValue {
  return useContext(BoardCardMotionContext);
}
