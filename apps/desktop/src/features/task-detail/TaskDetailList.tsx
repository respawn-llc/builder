import { useMemo, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { ActivityItem, TaskComment, TaskDetail } from "../../api";
import { errorMessage } from "../../api/errors";
import type { TaskDetailInitialFocus } from "../../app/sidebarContext";
import { taskDetailInitialFocusRequestKey } from "../../app/taskDetailInitialFocus";
import { useSidebarHeaderOffset } from "../../app/sidebarHeaderOffset";
import { ErrorState, LoadingState, VirtualizedInfiniteList } from "../../ui";
import { ActivityRow, CommentComposer, CommentRow } from "./TaskDetailActivity";
import type { DescriptionPresentationState } from "./TaskDetailDescriptionPresentation";
import { TaskInbox } from "./TaskDetailInbox";
import {
  DescriptionIsland,
  PropertiesIsland,
  TaskHeaderIsland,
  type TaskDraft,
} from "./TaskDetailRows";
import { TaskTabs, type DetailTab } from "./TaskDetailTabs";
import type { QuestionSelectionState } from "./TaskDetailQuestionState";
import type { useTaskActivity, useTaskComments, useTaskMutations } from "./useTaskDetailData";

type TaskDetailListItem =
  | Readonly<{ kind: "header" }>
  | Readonly<{ kind: "body" }>
  | Readonly<{ kind: "inbox" }>
  | Readonly<{ kind: "tabs" }>
  | Readonly<{ kind: "comment-composer" }>
  | Readonly<{ kind: "comments-loading" }>
  | Readonly<{ kind: "comments-error"; error: unknown }>
  | Readonly<{ kind: "comments-empty" }>
  | Readonly<{ kind: "comment"; comment: TaskComment }>
  | Readonly<{ kind: "activity-loading" }>
  | Readonly<{ kind: "activity-error"; error: unknown }>
  | Readonly<{ kind: "activity-empty" }>
  | Readonly<{ kind: "activity"; item: ActivityItem }>;

export function TaskDetailList({
  activity,
  comments,
  detail,
  disabled,
  draft,
  descriptionPresentation,
  editingComment,
  initialFocus,
  mutations,
  newCommentBody,
  onDraftChange,
  onDescriptionPresentationChange,
  onNewCommentBodyChange,
  onEditingCommentChange,
  onQuestionSelectionChange,
  onSaveDraft,
  openLink,
  questionSelections,
  selectedTab,
  setTab,
  updateError,
  updatePending,
}: Readonly<{
  activity: ReturnType<typeof useTaskActivity>;
  comments: ReturnType<typeof useTaskComments>;
  detail: TaskDetail;
  disabled: boolean;
  draft: TaskDraft;
  descriptionPresentation: DescriptionPresentationState;
  editingComment: Readonly<{ id: string; body: string }> | null;
  initialFocus?: TaskDetailInitialFocus | undefined;
  mutations: ReturnType<typeof useTaskMutations>;
  newCommentBody: string;
  onDraftChange: (draft: TaskDraft) => void;
  onDescriptionPresentationChange: (presentation: DescriptionPresentationState) => void;
  onNewCommentBodyChange: (body: string) => void;
  onEditingCommentChange: (editing: Readonly<{ id: string; body: string }> | null) => void;
  onQuestionSelectionChange: (askID: string, selection: QuestionSelectionState) => void;
  onSaveDraft: (draft?: TaskDraft) => Promise<void>;
  openLink: (url: string) => void;
  questionSelections: ReadonlyMap<string, QuestionSelectionState>;
  selectedTab: DetailTab;
  setTab: (tab: DetailTab) => void;
  updateError: unknown;
  updatePending: boolean;
}>) {
  const { t } = useTranslation();
  const headerOffset = useSidebarHeaderOffset();
  const activityItems = useMemo(() => activity.data?.pages.flatMap((page) => page.items) ?? [], [activity.data]);
  const commentItems = useMemo(() => comments.data?.pages.flatMap((page) => page.comments) ?? [], [comments.data]);
  const listItems = useMemo(
    () =>
      taskDetailListItems({
        activityItems,
        activityPending: activity.isPending,
        activityError: activity.error,
        commentItems,
        commentsPending: comments.isPending,
        commentsError: comments.error,
        detail,
        tab: selectedTab,
      }),
    [
      activity.error,
      activity.isPending,
      activityItems,
      commentItems,
      comments.error,
      comments.isPending,
      detail,
      selectedTab,
    ],
  );
  const paging = taskDetailPaging({ activity, comments, detailID: detail.id, selectedTab });

  return (
    <VirtualizedInfiniteList
      ariaLabel={t("task.title")}
      className="task-detail-island-stack h-full min-h-0 overflow-auto hide-scrollbar p-[var(--space-3)]"
      estimateSize={() => 160}
      getItemKey={taskDetailListItemKey}
      hasNextPage={paging.hasNextPage}
      initialScrollKey={initialFocus !== undefined ? "inbox" : undefined}
      initialScrollRequestKey={
        initialFocus !== undefined
          ? taskDetailInitialFocusRequestKey(detail.id, initialFocus)
          : undefined
      }
      isFetchingNextPage={paging.isFetchingNextPage}
      items={listItems}
      loadingLabel={t("app.loadingMore")}
      loadMoreKey={paging.loadMoreKey}
      nonAdjustingResizeItemKey="body"
      onLoadMore={paging.loadMore}
      paddingStart={headerOffset}
      rowSpacing="compact"
      renderItem={(item) => (
        <TaskDetailListRow
          activityCount={activityItems.length}
          commentCount={detail.comments.length}
          detail={detail}
          disabled={disabled}
          draft={draft}
          descriptionPresentation={descriptionPresentation}
          editingComment={editingComment}
          errorTitle={t("states.error")}
          initialFocus={initialFocus}
          item={item}
          loadingTitle={t("states.loading")}
          mutations={mutations}
          newCommentBody={newCommentBody}
          noActivityTitle={t("task.noActivityTitle")}
          noCommentsTitle={t("task.noCommentsTitle")}
          onDraftChange={onDraftChange}
          onDescriptionPresentationChange={onDescriptionPresentationChange}
          onNewCommentBodyChange={onNewCommentBodyChange}
          onEditingCommentChange={onEditingCommentChange}
          onQuestionSelectionChange={onQuestionSelectionChange}
          onSaveDraft={onSaveDraft}
          openLink={openLink}
          questionSelections={questionSelections}
          selectedTab={selectedTab}
          setTab={setTab}
          updateError={updateError}
          updatePending={updatePending}
        />
      )}
      testId="task-detail-island-stack"
    />
  );
}

type TaskDetailListRowProps = Readonly<{
  activityCount: number;
  commentCount: number;
  detail: TaskDetail;
  disabled: boolean;
  draft: TaskDraft;
  descriptionPresentation: DescriptionPresentationState;
  editingComment: Readonly<{ id: string; body: string }> | null;
  errorTitle: string;
  initialFocus?: TaskDetailInitialFocus | undefined;
  item: TaskDetailListItem;
  loadingTitle: string;
  mutations: ReturnType<typeof useTaskMutations>;
  newCommentBody: string;
  noActivityTitle: string;
  noCommentsTitle: string;
  onDraftChange: (draft: TaskDraft) => void;
  onDescriptionPresentationChange: (presentation: DescriptionPresentationState) => void;
  onNewCommentBodyChange: (body: string) => void;
  onEditingCommentChange: (editing: Readonly<{ id: string; body: string }> | null) => void;
  onQuestionSelectionChange: (askID: string, selection: QuestionSelectionState) => void;
  onSaveDraft: (draft?: TaskDraft) => Promise<void>;
  openLink: (url: string) => void;
  questionSelections: ReadonlyMap<string, QuestionSelectionState>;
  selectedTab: DetailTab;
  setTab: (tab: DetailTab) => void;
  updateError: unknown;
  updatePending: boolean;
}>;

const rowRenderers: Record<TaskDetailListItem["kind"], (props: TaskDetailListRowProps) => ReactNode> = {
  header: HeaderRow,
  body: BodyRow,
  inbox: InboxRow,
  tabs: TabsRow,
  "comment-composer": CommentComposerRow,
  "comments-loading": LoadingRow,
  "comments-error": ErrorRow,
  "comments-empty": CommentsEmptyRow,
  comment: CommentItemRow,
  "activity-loading": LoadingRow,
  "activity-error": ErrorRow,
  "activity-empty": ActivityEmptyRow,
  activity: ActivityItemRow,
};

function TaskDetailListRow(props: TaskDetailListRowProps): ReactNode {
  const row = rowRenderers[props.item.kind](props);
  if (row === null || !isFeedItem(props.item)) {
    return row;
  }
  return (
    <div className="task-detail-feed-row" data-task-detail-feed-tab={props.selectedTab}>
      {row}
    </div>
  );
}

function HeaderRow({
  detail,
  disabled,
  draft,
  onDraftChange,
  onSaveDraft,
  updatePending,
}: TaskDetailListRowProps): ReactNode {
  return (
    <TaskHeaderIsland
      detail={detail}
      disabled={disabled || updatePending}
      draft={draft}
      onDraftChange={onDraftChange}
      onSave={onSaveDraft}
    />
  );
}

function BodyRow({
  detail,
  disabled,
  draft,
  mutations,
  onDraftChange,
  onDescriptionPresentationChange,
  descriptionPresentation,
  updateError,
  updatePending,
}: TaskDetailListRowProps): ReactNode {
  return (
    <div
      className="task-detail-body-split grid items-stretch gap-[var(--space-2)]"
      data-testid="task-detail-body-split"
    >
      <DescriptionIsland
        disabled={disabled || updatePending}
        draft={draft}
        error={updateError}
        onDraftChange={onDraftChange}
        onPresentationChange={onDescriptionPresentationChange}
        presentation={descriptionPresentation}
      />
      <PropertiesIsland
        detail={detail}
        disabled={disabled}
        mutations={mutations}
      />
    </div>
  );
}

function InboxRow({
  detail,
  disabled,
  initialFocus,
  mutations,
  onQuestionSelectionChange,
  questionSelections,
}: TaskDetailListRowProps): ReactNode {
  return (
    <TaskInbox
      currentVersion={detail.workflowVersion}
      detail={detail}
      disabled={disabled}
      initialFocus={initialFocus}
      mutations={mutations}
      onQuestionSelectionChange={onQuestionSelectionChange}
      questionSelections={questionSelections}
    />
  );
}

function TabsRow({
  activityCount,
  commentCount,
  selectedTab,
  setTab,
}: TaskDetailListRowProps): ReactNode {
  return (
    <TaskTabs
      activityCount={activityCount}
      commentCount={commentCount}
      selected={selectedTab}
      onSelect={setTab}
    />
  );
}

function CommentComposerRow({
  disabled,
  editingComment,
  mutations,
  newCommentBody,
  onNewCommentBodyChange,
  onEditingCommentChange,
}: TaskDetailListRowProps): ReactNode {
  return (
    <CommentComposer
      body={newCommentBody}
      disabled={disabled}
      editing={editingComment}
      mutations={mutations}
      onBodyChange={onNewCommentBodyChange}
      onEditingChange={onEditingCommentChange}
    />
  );
}

function LoadingRow({ loadingTitle }: TaskDetailListRowProps): ReactNode {
  return <LoadingState appearanceDelayMs={0} fullPage={false} reveal={false} title={loadingTitle} />;
}

function ErrorRow({ errorTitle, item }: TaskDetailListRowProps): ReactNode {
  const error = item.kind === "comments-error" || item.kind === "activity-error" ? item.error : undefined;
  return <ErrorState body={errorMessage(error)} reveal={false} title={errorTitle} />;
}

function CommentsEmptyRow({ noCommentsTitle }: TaskDetailListRowProps): ReactNode {
  return <p className="m-0 text-[var(--color-muted)]">{noCommentsTitle}</p>;
}

function CommentItemRow({
  disabled,
  editingComment,
  item,
  mutations,
  onEditingCommentChange,
  openLink,
}: TaskDetailListRowProps): ReactNode {
  const comment = item.kind === "comment" ? item.comment : undefined;
  return comment === undefined ? null : (
    <CommentRow
      comment={comment}
      disabled={disabled}
      editing={editingComment?.id === comment.id}
      mutations={mutations}
      onEdit={(nextComment) => {
        onEditingCommentChange({ id: nextComment.id, body: nextComment.body });
      }}
      openLink={openLink}
    />
  );
}

function ActivityEmptyRow({ noActivityTitle }: TaskDetailListRowProps): ReactNode {
  return <p className="m-0 text-[var(--color-muted)]">{noActivityTitle}</p>;
}

function ActivityItemRow({ item }: TaskDetailListRowProps): ReactNode {
  const activity = item.kind === "activity" ? item.item : undefined;
  return activity === undefined ? null : (
    <div className="grid justify-items-center">
      <ActivityRow item={activity} />
    </div>
  );
}

function taskDetailListItems({
  activityError,
  activityItems,
  activityPending,
  commentItems,
  commentsError,
  commentsPending,
  detail,
  tab,
}: Readonly<{
  activityError: unknown;
  activityItems: readonly ActivityItem[];
  activityPending: boolean;
  commentItems: readonly TaskComment[];
  commentsError: unknown;
  commentsPending: boolean;
  detail: TaskDetail;
  tab: DetailTab;
}>): readonly TaskDetailListItem[] {
  const staticItems: TaskDetailListItem[] = [{ kind: "header" }, { kind: "body" }];
  if (detail.attention.length > 0) {
    staticItems.push({ kind: "inbox" });
  }
  staticItems.push({ kind: "tabs" });
  if (tab === "comments") {
    return [
      ...staticItems,
      { kind: "comment-composer" },
      ...commentStatusItems({ commentsError, commentsPending, commentItems }),
      ...commentItems.map((comment) => ({ kind: "comment", comment }) satisfies TaskDetailListItem),
    ];
  }
  return [
    ...staticItems,
    ...activityStatusItems({ activityError, activityPending, activityItems }),
    ...activityItems.map((item) => ({ kind: "activity", item }) satisfies TaskDetailListItem),
  ];
}

function commentStatusItems({
  commentItems,
  commentsError,
  commentsPending,
}: Readonly<{
  commentItems: readonly TaskComment[];
  commentsError: unknown;
  commentsPending: boolean;
}>): readonly TaskDetailListItem[] {
  // Once rows are loaded, keep them visible. A failed/pending later page must
  // not collapse already-loaded comments into a single status row; the
  // infinite-list footer surfaces ongoing pagination state instead.
  if (commentItems.length > 0) {
    return [];
  }
  if (commentsPending) {
    return [{ kind: "comments-loading" }];
  }
  if (commentsError != null) {
    return [{ kind: "comments-error", error: commentsError }];
  }
  return [{ kind: "comments-empty" }];
}

function activityStatusItems({
  activityError,
  activityItems,
  activityPending,
}: Readonly<{
  activityError: unknown;
  activityItems: readonly ActivityItem[];
  activityPending: boolean;
}>): readonly TaskDetailListItem[] {
  // Keep already-loaded activity rows visible across later page fetches; only
  // show a full status row when nothing has loaded yet.
  if (activityItems.length > 0) {
    return [];
  }
  if (activityPending) {
    return [{ kind: "activity-loading" }];
  }
  if (activityError != null) {
    return [{ kind: "activity-error", error: activityError }];
  }
  return [{ kind: "activity-empty" }];
}

function taskDetailListItemKey(item: TaskDetailListItem): string {
  if (item.kind === "comment") {
    return `comment:${item.comment.id}`;
  }
  if (item.kind === "activity") {
    return `activity:${item.item.id}`;
  }
  return item.kind;
}

function isFeedItem(item: TaskDetailListItem): boolean {
  return item.kind !== "header" && item.kind !== "body" && item.kind !== "inbox" && item.kind !== "tabs";
}

function taskDetailPaging({
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
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  loadMoreKey: string;
  loadMore: () => void;
}> {
  if (selectedTab === "comments") {
    const nextPageToken = comments.data?.pages.at(-1)?.nextPageToken ?? "";
    return {
      hasNextPage: comments.hasNextPage,
      isFetchingNextPage: comments.isFetchingNextPage,
      loadMoreKey: `${detailID}:comments:${nextPageToken}:${comments.dataUpdatedAt.toString()}`,
      loadMore: () => {
        void comments.fetchNextPage();
      },
    };
  }
  const nextPageToken = activity.data?.pages.at(-1)?.nextPageToken ?? "";
  return {
    hasNextPage: activity.hasNextPage,
    isFetchingNextPage: activity.isFetchingNextPage,
    loadMoreKey: `${detailID}:activity:${nextPageToken}:${activity.dataUpdatedAt.toString()}`,
    loadMore: () => {
      void activity.fetchNextPage();
    },
  };
}
