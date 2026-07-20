import type { LabelMembershipRefreshEffect } from "@/shared/labels";

export type BoardMembershipRefreshHandler = (effect: LabelMembershipRefreshEffect) => Promise<void>;

export interface BoardMembershipRefreshRef {
  current: BoardMembershipRefreshHandler;
}

export async function ignoreBoardMembershipRefresh(): Promise<void> {
  return Promise.resolve();
}
