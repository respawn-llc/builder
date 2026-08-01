import type { BoardNodeCardsSort } from "@/api";

export function boardNodeCardsSortEqual(left: BoardNodeCardsSort, right: BoardNodeCardsSort): boolean {
  return left.field === right.field && left.direction === right.direction;
}
