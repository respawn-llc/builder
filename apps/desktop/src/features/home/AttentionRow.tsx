import { ChevronRight, MessageCircle } from "lucide-react";
import { memo } from "react";
import { useTranslation } from "react-i18next";

import type { AttentionItem } from "@/api";
import {
  formatRelativeTime,
  taskDetailInitialFocusFromAttentionItem,
  useAppNavigation,
  type SidebarMode,
  type SidebarRootController,
} from "@/app-facade";
import { desktopChatEnabled } from "@/shared/feature-flags";
import { cx, islandSurfaceClassName } from "@/ui";
import { attentionChatTarget } from "./attentionChatTarget";

export const AttentionRow = memo(function AttentionRow({
  item,
  openSidebar,
  sidebarMode,
}: Readonly<{
  item: AttentionItem;
  openSidebar: SidebarRootController["open"];
  sidebarMode: SidebarMode;
}>) {
  const { t } = useTranslation();
  const navigation = useAppNavigation();
  const message =
    item.message ??
    (item.kind === "approval"
      ? t("app.attention.approvalFallback")
      : t("app.attention.interruptedCurrentNodeFallback"));
  const chatTarget = desktopChatEnabled ? attentionChatTarget(item) : null;
  if (!desktopChatEnabled) {
    return (
      <ProductionAttentionRow
        item={item}
        message={message}
        openSidebar={openSidebar}
        sidebarMode={sidebarMode}
      />
    );
  }
  if (chatTarget === null) {
    return (
      <button
        className={cx(
          "grid w-full min-w-0 gap-[var(--space-2)] rounded-[var(--radius-l)] p-[var(--space-3)] text-left text-[var(--color-on-island)]",
          islandSurfaceClassName(1),
        )}
        data-testid="attention-row"
        onClick={() => {
          openTaskDetail(item, openSidebar, sidebarMode);
        }}
        type="button"
      >
        <AttentionHeader item={item} />
        <AttentionBody item={item} message={message} />
      </button>
    );
  }
  return (
    <article
      className={cx(
        "grid w-full min-w-0 rounded-[var(--radius-l)] text-left text-[var(--color-on-island)]",
        islandSurfaceClassName(1),
      )}
      data-testid="attention-row"
    >
      <button
        className="flex min-w-0 items-center gap-[var(--space-2)] p-[var(--space-3)] text-left"
        data-testid="attention-chat-header"
        onClick={() => {
          void navigation.openSessionChat(chatTarget);
        }}
        type="button"
      >
        <AttentionHeader item={item} showChat />
      </button>
      <button
        className="grid min-w-0 gap-[var(--space-2)] p-[var(--space-3)] pt-0 text-left"
        data-testid="attention-task-detail-body"
        onClick={() => {
          openTaskDetail(item, openSidebar, sidebarMode);
        }}
        type="button"
      >
        <AttentionBody item={item} message={message} />
      </button>
    </article>
  );
}, attentionRowPropsEqual);

function ProductionAttentionRow({
  item,
  message,
  openSidebar,
  sidebarMode,
}: Readonly<{
  item: AttentionItem;
  message: string;
  openSidebar: SidebarRootController["open"];
  sidebarMode: SidebarMode;
}>) {
  return (
    <button
      className={cx(
        "grid w-full min-w-0 gap-[var(--space-2)] rounded-[var(--radius-l)] p-[var(--space-3)] text-left text-[var(--color-on-island)]",
        islandSurfaceClassName(1),
      )}
      data-testid="attention-row"
      onClick={() => {
        openTaskDetail(item, openSidebar, sidebarMode);
      }}
      type="button"
    >
      <div
        className="flex min-w-0 flex-wrap items-center gap-[var(--space-2)]"
        data-testid="attention-row-meta"
      >
        {item.taskShortID.length > 0 ? (
          <span className="min-w-0 truncate font-mono text-sm text-[var(--color-muted)]">
            {item.taskShortID}
          </span>
        ) : null}
      </div>
      {item.taskTitle.length > 0 ? <strong className="min-w-0 truncate">{item.taskTitle}</strong> : null}
      <span className="min-w-0 line-clamp-5 text-sm break-words">{message}</span>
      <span className="text-sm text-[var(--color-muted)]">{formatRelativeTime(item.occurredAt)}</span>
    </button>
  );
}

function AttentionHeader({ item, showChat = false }: Readonly<{ item: AttentionItem; showChat?: boolean }>) {
  return (
    <div className="flex min-w-0 flex-1 items-center gap-[var(--space-2)]">
      {item.taskShortID.length > 0 ? (
        <span className="min-w-0 shrink-0 truncate font-mono text-sm text-[var(--color-muted)]">
          {item.taskShortID}
        </span>
      ) : null}
      {item.taskTitle.length > 0 ? (
        <strong className="min-w-0 flex-1 truncate">{item.taskTitle}</strong>
      ) : null}
      {showChat ? (
        <>
          <MessageCircle aria-hidden="true" className="shrink-0" size={16} strokeWidth={1.5} />
          <ChevronRight aria-hidden="true" className="shrink-0" size={16} strokeWidth={1.5} />
        </>
      ) : null}
    </div>
  );
}

function AttentionBody({ item, message }: Readonly<{ item: AttentionItem; message: string }>) {
  return (
    <>
      <span className="min-w-0 line-clamp-5 break-words text-sm text-[var(--color-muted)]">{message}</span>
      <span className="text-sm text-[var(--color-muted)]">{formatRelativeTime(item.occurredAt)}</span>
    </>
  );
}

function openTaskDetail(
  item: AttentionItem,
  openSidebar: SidebarRootController["open"],
  sidebarMode: SidebarMode,
): void {
  openSidebar({
    kind: "taskDetail",
    initialFocus: taskDetailInitialFocusFromAttentionItem(item),
    inboxNav: true,
    mode: sidebarMode,
    onMutated: undefined,
    taskID: item.taskID,
  });
}

function attentionRowPropsEqual(
  previous: Readonly<{
    item: AttentionItem;
    openSidebar: SidebarRootController["open"];
    sidebarMode: SidebarMode;
  }>,
  next: Readonly<{
    item: AttentionItem;
    openSidebar: SidebarRootController["open"];
    sidebarMode: SidebarMode;
  }>,
): boolean {
  return (
    previous.openSidebar === next.openSidebar &&
    previous.sidebarMode === next.sidebarMode &&
    attentionItemsEqual(previous.item, next.item)
  );
}

function attentionItemsEqual(previous: AttentionItem, next: AttentionItem): boolean {
  return (
    previous.id === next.id &&
    previous.kind === next.kind &&
    previous.taskID === next.taskID &&
    previous.taskShortID === next.taskShortID &&
    previous.taskTitle === next.taskTitle &&
    previous.message === next.message &&
    previous.occurredAt === next.occurredAt &&
    attentionChatTargetsEqual(attentionChatTarget(previous), attentionChatTarget(next))
  );
}

function attentionChatTargetsEqual(
  previous: ReturnType<typeof attentionChatTarget>,
  next: ReturnType<typeof attentionChatTarget>,
): boolean {
  return previous?.projectID === next?.projectID && previous?.sessionID === next?.sessionID;
}
