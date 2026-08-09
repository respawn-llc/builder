import type { DetailTab } from "./TaskDetailTabs";
import type { useTaskActivity, useTaskComments } from "./useTaskDetailData";

export function taskDetailPaging({
  activity,
  comments,
  detailID,
  selectedTab,
}: Readonly<{
  activity: ReturnType<typeof useTaskActivity>;
  comments: ReturnType<typeof useTaskComments>;
  detailID: string;
  selectedTab: DetailTab;
}>): Readonly<{
  error: unknown;
  hasPreviousPage: boolean;
  isFetchingPreviousPage: boolean;
  isFetchPreviousPageError: boolean;
  previousLoadKey: string | undefined;
  loadPrevious: () => void;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  isFetchNextPageError: boolean;
  nextLoadKey: string | undefined;
  loadNext: () => void;
}> {
  if (selectedTab === "comments") {
    return feedPaging({
      data: comments,
      detailID,
      feed: "comments",
    });
  }
  return feedPaging({
    data: activity,
    detailID,
    feed: "activity",
  });
}

function feedPaging<TFeed extends "activity" | "comments">({
  data,
  detailID,
  feed,
}: Readonly<{
  data: TFeed extends "comments" ? ReturnType<typeof useTaskComments> : ReturnType<typeof useTaskActivity>;
  detailID: string;
  feed: TFeed;
}>): Readonly<{
  error: unknown;
  hasPreviousPage: boolean;
  isFetchingPreviousPage: boolean;
  isFetchPreviousPageError: boolean;
  previousLoadKey: string | undefined;
  loadPrevious: () => void;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  isFetchNextPageError: boolean;
  nextLoadKey: string | undefined;
  loadNext: () => void;
}> {
  const firstOffset = data.data?.pages.at(0)?.offset;
  const nextOffset = data.data?.pages.at(-1)?.nextOffset;
  const previousLoadKey =
    firstOffset === undefined
      ? undefined
      : `${detailID}:${feed}:previous:${firstOffset.toString()}:${data.dataUpdatedAt.toString()}`;
  const nextLoadKey =
    nextOffset === undefined || nextOffset === null
      ? undefined
      : `${detailID}:${feed}:next:${nextOffset.toString()}:${data.dataUpdatedAt.toString()}`;
  return {
    error: data.error,
    hasPreviousPage: data.hasPreviousPage,
    isFetchingPreviousPage: data.isFetchingPreviousPage,
    isFetchPreviousPageError: data.isFetchPreviousPageError,
    previousLoadKey,
    loadPrevious: () => {
      void data.fetchPreviousPage();
    },
    hasNextPage: data.hasNextPage,
    isFetchingNextPage: data.isFetchingNextPage,
    isFetchNextPageError: data.isFetchNextPageError,
    nextLoadKey,
    loadNext: () => {
      void data.fetchNextPage();
    },
  };
}
