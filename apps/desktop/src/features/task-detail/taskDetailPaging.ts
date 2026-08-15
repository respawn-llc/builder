import type { DetailTab } from "./TaskDetailTabs";
import type { useTaskActivity, useTaskComments } from "./useTaskDetailData";
import type { VirtualizedPixelOffsetRequest } from "@/ui";
export const selectedFeed = <Comments, Activity>(
  tab: DetailTab,
  comments: Comments,
  activity: Activity,
): Comments | Activity => (tab === "comments" ? comments : activity);
export const feedPixelOffsetRequest = (
  attentionPending: boolean,
  feedPending: boolean,
  request: VirtualizedPixelOffsetRequest | undefined,
) => (attentionPending || feedPending ? undefined : request);
type TaskDetailPagingInput = Readonly<{
  activity: ReturnType<typeof useTaskActivity>;
  comments: ReturnType<typeof useTaskComments>;
  detailID: string;
  selectedTab: DetailTab;
}>;
export function taskDetailPaging({ activity, comments, detailID, selectedTab }: TaskDetailPagingInput) {
  const data = selectedFeed(selectedTab, comments, activity);
  const firstOffset = data.data?.pages.at(0)?.offset;
  const nextOffset = data.data?.pages.at(-1)?.nextOffset;
  const loadKey = (direction: "previous" | "next", offset: number | null | undefined) =>
    offset === undefined || offset === null
      ? undefined
      : `${detailID}:${selectedTab}:${direction}:${offset.toString()}:${data.dataUpdatedAt.toString()}`;
  return {
    error: data.error,
    hasPreviousPage: data.hasPreviousPage,
    isFetchingPreviousPage: data.isFetchingPreviousPage,
    isFetchPreviousPageError: data.isFetchPreviousPageError,
    previousLoadKey: loadKey("previous", firstOffset),
    loadPrevious: () => void data.fetchPreviousPage(),
    hasNextPage: data.hasNextPage,
    isFetchingNextPage: data.isFetchingNextPage,
    isFetchNextPageError: data.isFetchNextPageError,
    nextLoadKey: loadKey("next", nextOffset),
    loadNext: () => void data.fetchNextPage(),
  };
}
