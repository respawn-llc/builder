import { useMemo, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { ActivityItem, AttentionItem, TaskComment, TaskDetail, TaskDependencyDirection } from "@/api";
import { errorMessage } from "@/api";
import type { TaskDetailInitialFocus } from "@/app-facade";
import { taskDetailInitialFocusRequestKey } from "@/app-facade";
import { useSidebarHeaderOffset } from "@/app-facade";
import type { TaskDependencyPair } from "@/shared/task-dependencies";
import {
  autoLoadAvailable,
  directionalBoundary,
  ErrorState,
  InfiniteListBoundary,
  LoadingState,
  VirtualizedInfiniteList,
  type VirtualizedInfiniteListBoundaryState,
  type VirtualizedPixelOffsetRequest,
} from "@/ui";
import { ActivityRow, CommentComposer, CommentRow } from "./TaskDetailActivity";
import type { DescriptionPresentationState } from "./TaskDetailDescriptionPresentation";
import { TaskInbox } from "./TaskDetailInbox";
import { DescriptionIsland, PropertiesIsland, TaskHeaderIsland, type TaskDraft } from "./TaskDetailRows";
import { TaskTabs, type DetailTab } from "./TaskDetailTabs";
import { TaskDependenciesArea } from "./TaskDependenciesArea";
import type { QuestionSelectionState } from "./TaskDetailQuestionState";
import { feedPixelOffsetRequest, selectedFeed, taskDetailPaging } from "./taskDetailPaging";
import type {
  useTaskActivity,
  useTaskAttention,
  useTaskComments,
  useTaskMutations,
} from "./useTaskDetailData";

type TaskDetailListItem =
  | Readonly<{ kind: "header" }>
  | Readonly<{ kind: "body" }>
  | Readonly<{ kind: "dependencies" }>
  | Readonly<{ kind: "inbox" }>
  | Readonly<{ kind: "tabs" }>
  | Readonly<{ kind: "comment-composer" }>
  | Readonly<{ kind: "comments-loading" }>
  | Readonly<{ kind: "comments-error"; error: unknown }>
  | Readonly<{ kind: "comments-empty" }>
  | Readonly<{ kind: "comment"; comment: TaskComment; presentationKey: string }>
  | Readonly<{ kind: "activity-loading" }>
  | Readonly<{ kind: "activity-error"; error: unknown }>
  | Readonly<{ kind: "activity-empty" }>
  | Readonly<{ kind: "activity"; item: ActivityItem; presentationKey: string }>;

type TaskDetailFeedRow<T> = Readonly<{
  item: T;
  presentationKey: string;
}>;

export function TaskDetailList({
  activity,
  attention,
  comments,
  detail,
  disabled,
  draft,
  descriptionPresentation,
  editingComment,
  focusRequestKey,
  initialFocus,
  mutations,
  newCommentBody,
  onDraftChange,
  onDescriptionPresentationChange,
  onAddDependency,
  onRemoveDependency,
  onSelectDependencyTask,
  onNewCommentBodyChange,
  onEditingCommentChange,
  onQuestionSelectionChange,
  onScrollElementChange,
  onSaveDraft,
  pixelOffsetRequest,
  questionSelections,
  relationshipNavigationAvailable,
  selectedTab,
  setTab,
  updateError,
  updatePending,
}: Readonly<{
  activity: ReturnType<typeof useTaskActivity>;
  attention: ReturnType<typeof useTaskAttention>;
  comments: ReturnType<typeof useTaskComments>;
  detail: TaskDetail;
  disabled: boolean;
  draft: TaskDraft;
  descriptionPresentation: DescriptionPresentationState;
  editingComment: Readonly<{ id: string; body: string }> | null;
  focusRequestKey?: string | undefined;
  initialFocus?: TaskDetailInitialFocus | undefined;
  mutations: ReturnType<typeof useTaskMutations>;
  newCommentBody: string;
  onDraftChange: (draft: TaskDraft) => void;
  onDescriptionPresentationChange: (presentation: DescriptionPresentationState) => void;
  onAddDependency: (direction: TaskDependencyDirection) => void;
  onRemoveDependency: (pair: TaskDependencyPair) => void;
  onSelectDependencyTask: (taskID: string) => void;
  onNewCommentBodyChange: (body: string) => void;
  onEditingCommentChange: (editing: Readonly<{ id: string; body: string }> | null) => void;
  onQuestionSelectionChange: (askID: string, selection: QuestionSelectionState) => void;
  onScrollElementChange: (element: HTMLDivElement | null) => void;
  onSaveDraft: (draft?: TaskDraft) => Promise<void>;
  pixelOffsetRequest?: VirtualizedPixelOffsetRequest | undefined;
  questionSelections: ReadonlyMap<string, QuestionSelectionState>;
  relationshipNavigationAvailable: boolean;
  selectedTab: DetailTab;
  setTab: (tab: DetailTab) => void;
  updateError: unknown;
  updatePending: boolean;
}>) {
  const { t } = useTranslation();
  const headerOffset = useSidebarHeaderOffset();
  const draftDirty = draft.title !== detail.title || draft.body !== detail.body;
  const canSaveDraft = draftDirty && !disabled && !updatePending && draft.title.trim().length > 0;
  const activityItems = useMemo(
    () =>
      withPresentationKeys(
        activity.data?.pages.flatMap((page) => page.items) ?? [],
        "activity",
        (item) => item.id,
      ),
    [activity.data],
  );
  const commentItems = useMemo(
    () =>
      withPresentationKeys(
        comments.data?.pages.flatMap((page) => page.items) ?? [],
        "comment",
        (comment) => comment.id,
      ),
    [comments.data],
  );
  const attentionItems = useMemo(() => attention.data?.items ?? [], [attention.data]);
  const listItems = useMemo(
    () =>
      taskDetailListItems({
        activityItems,
        activityPending: activity.isPending,
        activityError: activity.error,
        attentionFailed: attention.isError,
        attentionItems,
        attentionPending: attention.isPending,
        commentItems,
        commentsPending: comments.isPending,
        commentsError: comments.error,
        detail,
        initialFocus,
        tab: selectedTab,
      }),
    [
      activity.error,
      activity.isPending,
      activityItems,
      attention.isError,
      attention.isPending,
      attentionItems,
      commentItems,
      comments.error,
      comments.isPending,
      detail,
      initialFocus,
      selectedTab,
    ],
  );
  const initialScrollKey =
    initialFocus?.kind === "dependencies" ? "dependencies" : initialFocus === undefined ? undefined : "inbox";
  const pinnedItemKeys = useMemo(() => {
    const keys = new Set<string>();
    if (attention.isPending || (initialFocus !== undefined && initialFocus.kind !== "dependencies")) {
      keys.add("inbox");
    }
    if (initialFocus?.kind === "dependencies") {
      keys.add("dependencies");
    }
    return keys.size === 0 ? undefined : keys;
  }, [attention.isPending, initialFocus]);
  const paging = taskDetailPaging({ activity, comments, detailID: detail.id, selectedTab });
  const previousBoundary = directionalBoundary({
    failed: paging.isFetchPreviousPageError,
    loading: paging.isFetchingPreviousPage,
    loadingLabel: t("app.loadingMore"),
    message: errorMessage(paging.error),
    onRetry: paging.loadPrevious,
    retryLabel: t("app.retry"),
  });
  const nextBoundary = directionalBoundary({
    failed: paging.isFetchNextPageError,
    loading: paging.isFetchingNextPage,
    loadingLabel: t("app.loadingMore"),
    message: errorMessage(paging.error),
    onRetry: paging.loadNext,
    retryLabel: t("app.retry"),
  });
  const firstFeedItemKey = selectedFeed(selectedTab, commentItems, activityItems)[0]?.presentationKey;
  const feedOffsetRequest = feedPixelOffsetRequest(
    attention.isPending,
    selectedFeed(selectedTab, comments.isPending, activity.isPending),
    pixelOffsetRequest,
  );

  return (
    <VirtualizedInfiniteList
      ariaLabel={t("task.title")}
      className="task-detail-island-stack h-full min-h-0 overflow-auto hide-scrollbar p-[var(--space-3)]"
      estimateSize={() => 160}
      getItemKey={taskDetailListItemKey}
      hasNextPage={autoLoadAvailable(paging.hasNextPage, nextBoundary)}
      hasPreviousPage={autoLoadAvailable(paging.hasPreviousPage, previousBoundary)}
      initialScrollKey={initialScrollKey}
      initialScrollRequestKey={
        focusRequestKey ??
        (initialFocus === undefined ? undefined : taskDetailInitialFocusRequestKey(detail.id, initialFocus))
      }
      isFetchingNextPage={paging.isFetchingNextPage}
      isFetchingPreviousPage={paging.isFetchingPreviousPage}
      items={listItems}
      loadingLabel={t("app.loadingMore")}
      loadMoreKey={paging.nextLoadKey}
      nonAdjustingResizeItemKey="body"
      onLoadMore={paging.loadNext}
      onLoadPrevious={paging.loadPrevious}
      onScrollElementChange={onScrollElementChange}
      paddingStart={headerOffset}
      pinnedItemKeys={pinnedItemKeys}
      pixelOffsetRequest={feedOffsetRequest}
      rowSpacing="compact"
      nextBoundary={nextBoundary}
      previousLoadItemKey={firstFeedItemKey}
      previousLoadKey={paging.previousLoadKey}
      renderItem={(item) => (
        <TaskDetailListRow
          activityCount={activityItems.length}
          attentionItems={attentionItems}
          attentionPending={attention.isPending}
          commentCount={commentItems.length}
          canSaveDraft={canSaveDraft}
          draftDirty={draftDirty}
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
          onAddDependency={onAddDependency}
          onRemoveDependency={onRemoveDependency}
          onSelectDependencyTask={onSelectDependencyTask}
          onNewCommentBodyChange={onNewCommentBodyChange}
          onEditingCommentChange={onEditingCommentChange}
          onQuestionSelectionChange={onQuestionSelectionChange}
          onSaveDraft={onSaveDraft}
          questionSelections={questionSelections}
          relationshipNavigationAvailable={relationshipNavigationAvailable}
          selectedTab={selectedTab}
          setTab={setTab}
          previousBoundary={
            (item.kind === "comment" || item.kind === "activity") && item.presentationKey === firstFeedItemKey
              ? previousBoundary
              : undefined
          }
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
  attentionItems: readonly AttentionItem[];
  attentionPending: boolean;
  canSaveDraft: boolean;
  commentCount: number;
  detail: TaskDetail;
  disabled: boolean;
  draft: TaskDraft;
  draftDirty: boolean;
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
  onAddDependency: (direction: TaskDependencyDirection) => void;
  onRemoveDependency: (pair: TaskDependencyPair) => void;
  onSelectDependencyTask: (taskID: string) => void;
  onNewCommentBodyChange: (body: string) => void;
  onEditingCommentChange: (editing: Readonly<{ id: string; body: string }> | null) => void;
  onQuestionSelectionChange: (askID: string, selection: QuestionSelectionState) => void;
  onSaveDraft: (draft?: TaskDraft) => Promise<void>;
  questionSelections: ReadonlyMap<string, QuestionSelectionState>;
  relationshipNavigationAvailable: boolean;
  selectedTab: DetailTab;
  setTab: (tab: DetailTab) => void;
  previousBoundary?: VirtualizedInfiniteListBoundaryState | undefined;
  updateError: unknown;
  updatePending: boolean;
}>;

const rowRenderers: Record<TaskDetailListItem["kind"], (props: TaskDetailListRowProps) => ReactNode> = {
  header: HeaderRow,
  body: BodyRow,
  dependencies: DependenciesRow,
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
      {props.previousBoundary === undefined ? null : (
        <InfiniteListBoundary direction="previous" state={props.previousBoundary} />
      )}
      {row}
    </div>
  );
}

function HeaderRow({
  canSaveDraft,
  detail,
  disabled,
  draft,
  onDraftChange,
  onSaveDraft,
  updatePending,
}: TaskDetailListRowProps): ReactNode {
  return (
    <TaskHeaderIsland
      canSaveDraft={canSaveDraft}
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
  draftDirty,
  mutations,
  onDraftChange,
  onDescriptionPresentationChange,
  onSaveDraft,
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
        disabled={disabled}
        draft={draft}
        draftDirty={draftDirty}
        error={updateError}
        onDraftChange={onDraftChange}
        onPresentationChange={onDescriptionPresentationChange}
        onSave={onSaveDraft}
        presentation={descriptionPresentation}
        submitting={updatePending}
      />
      <PropertiesIsland detail={detail} disabled={disabled} mutations={mutations} />
    </div>
  );
}

function DependenciesRow({
  detail,
  disabled,
  mutations,
  onAddDependency,
  onRemoveDependency,
  onSelectDependencyTask,
  relationshipNavigationAvailable,
  updatePending,
}: TaskDetailListRowProps): ReactNode {
  return (
    <TaskDependenciesArea
      dependencies={detail.dependencies}
      disabled={disabled}
      navigationDisabled={
        disabled ||
        !relationshipNavigationAvailable ||
        updatePending ||
        mutations.addComment.isPending ||
        mutations.replaceComment.isPending
      }
      onAdd={onAddDependency}
      onRemove={onRemoveDependency}
      onSelectTask={onSelectDependencyTask}
      taskID={detail.id}
    />
  );
}

function InboxRow({
  attentionItems,
  attentionPending,
  detail,
  disabled,
  initialFocus,
  mutations,
  onQuestionSelectionChange,
  questionSelections,
}: TaskDetailListRowProps): ReactNode {
  if (attentionPending) {
    return <LoadingState appearanceDelayMs={0} fullPage={false} reveal={false} title={undefined} />;
  }
  return (
    <TaskInbox
      attentionItems={attentionItems}
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

function TabsRow({ activityCount, commentCount, selectedTab, setTab }: TaskDetailListRowProps): ReactNode {
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

function withPresentationKeys<T>(
  items: readonly T[],
  prefix: string,
  identity: (item: T) => string,
): readonly TaskDetailFeedRow<T>[] {
  const ordinals = new Map<string, number>();
  return items.map((item) => {
    const itemIdentity = identity(item);
    const ordinal = ordinals.get(itemIdentity) ?? 0;
    ordinals.set(itemIdentity, ordinal + 1);
    return {
      item,
      presentationKey: `${prefix}:${itemIdentity}:${ordinal.toString()}`,
    };
  });
}

function taskDetailListItems({
  activityError,
  activityItems,
  activityPending,
  attentionFailed,
  attentionItems,
  attentionPending,
  commentItems,
  commentsError,
  commentsPending,
  detail,
  initialFocus,
  tab,
}: Readonly<{
  activityError: unknown;
  activityItems: readonly TaskDetailFeedRow<ActivityItem>[];
  activityPending: boolean;
  attentionFailed: boolean;
  attentionItems: readonly AttentionItem[];
  attentionPending: boolean;
  commentItems: readonly TaskDetailFeedRow<TaskComment>[];
  commentsError: unknown;
  commentsPending: boolean;
  detail: TaskDetail;
  initialFocus?: TaskDetailInitialFocus | undefined;
  tab: DetailTab;
}>): readonly TaskDetailListItem[] {
  const staticItems: TaskDetailListItem[] = [{ kind: "header" }, { kind: "body" }, { kind: "dependencies" }];
  if (
    !attentionFailed &&
    (detail.attentionCount > 0 ||
      attentionItems.length > 0 ||
      (attentionPending && initialFocus !== undefined && initialFocus.kind !== "dependencies"))
  ) {
    staticItems.push({ kind: "inbox" });
  }
  staticItems.push({ kind: "tabs" });
  if (tab === "comments") {
    return [
      ...staticItems,
      { kind: "comment-composer" },
      ...commentStatusItems({ commentsError, commentsPending, commentItems }),
      ...commentItems.map(
        ({ item: comment, presentationKey }) =>
          ({ kind: "comment", comment, presentationKey }) satisfies TaskDetailListItem,
      ),
    ];
  }
  return [
    ...staticItems,
    ...activityStatusItems({ activityError, activityPending, activityItems }),
    ...activityItems.map(
      ({ item, presentationKey }) =>
        ({ kind: "activity", item, presentationKey }) satisfies TaskDetailListItem,
    ),
  ];
}

function commentStatusItems({
  commentItems,
  commentsError,
  commentsPending,
}: Readonly<{
  commentItems: readonly TaskDetailFeedRow<TaskComment>[];
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
  activityItems: readonly TaskDetailFeedRow<ActivityItem>[];
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
    return item.presentationKey;
  }
  if (item.kind === "activity") {
    return item.presentationKey;
  }
  return item.kind;
}

function isFeedItem(item: TaskDetailListItem): boolean {
  return (
    item.kind !== "header" &&
    item.kind !== "body" &&
    item.kind !== "dependencies" &&
    item.kind !== "inbox" &&
    item.kind !== "tabs"
  );
}
