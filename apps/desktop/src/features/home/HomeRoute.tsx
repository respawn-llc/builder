import { memo, useCallback, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import type { AttentionItem } from "@/api";
import { errorMessage } from "@/api";
import { basename, formatRelativeTime, projectKeyFromName } from "@/app-facade";
import { useAppNavigation } from "@/app-facade";
import { queryKeys } from "@/app-facade";
import {
  SidebarRootOwner,
  useOwnedSidebarRoots,
  type SidebarMode,
  type SidebarRootController,
} from "@/app-facade";
import { taskDetailInitialFocusFromAttentionItem } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useNativeDialogFallback } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import { useConnectionSnapshot } from "@/app-facade";
import { desktopChatEnabled } from "@/shared/feature-flags";
import {
  ErrorState,
  homeListCardListMaxWidthClassName,
  islandSurfaceClassName,
  LoadingState,
  VirtualizedInfiniteList,
} from "@/ui";
import { cx } from "@/ui";
import { HomePrimaryPane, type HomePrimaryTab } from "./HomePrimaryPane";
import { HomePrototypeSidebar } from "./HomePrototypeSidebar";
import { OverlappingCrossfade } from "./OverlappingCrossfade";
import { ProjectCreateDialog, type ProjectDraft } from "./ProjectCreateForm";
import { ProjectPrototypeDetail } from "./ProjectPrototypeDetail";
import { useHomeSidebarMode } from "./useHomeSidebarMode";
import {
  useGlobalAttentionPages,
  useGlobalAttentionEvents,
  useProjectCreation,
  useProjectCreationEvents,
  useProjectPages,
} from "./useHomeData";

const LOCAL_UNBOUND_PLAN_KIND = "local_unbound";
export function HomeRoute() {
  return (
    <SidebarRootOwner>
      <HomeRouteContent />
    </SidebarRootOwner>
  );
}

function HomeRouteContent() {
  const { t } = useTranslation();
  const { api, nativeBridge } = useAppServices();
  const { push } = useStatusController();
  const connection = useConnectionSnapshot();
  const sidebarMode = useHomeSidebarMode();
  const navigation = useAppNavigation();
  const { open } = useOwnedSidebarRoots();
  const queryClient = useQueryClient();
  const creation = useProjectCreation();
  const projects = useProjectPages();
  const attentionSubscriptionReady = useGlobalAttentionEvents();
  const attention = useGlobalAttentionPages(attentionSubscriptionReady);
  const [primaryTab, setPrimaryTab] = useState<HomePrimaryTab>("projects");
  const [prototypeCategory, setPrototypeCategory] = useState<HomePrimaryTab>("projects");
  const [selectedProjectID, setSelectedProjectID] = useState<string | null>(null);
  const projectItems = projects.data?.pages.flatMap((page) => page.projects) ?? [];
  const attentionItems = attention.data?.pages.flatMap((page) => page.items) ?? [];
  const disabled = connection.phase !== "connected";
  const projectCreationDialog = useNativeDialogFallback<ProjectDraft>({
    errorNoticeID: "project-create-window-error",
    errorTitle: t("home.projectCreateWindowError"),
    nativeAvailable: nativeBridge.capabilities.projectCreationWindow,
    openNative: async (nextDraft) => {
      await nativeBridge.projectCreation.openWindow(nextDraft);
    },
    renderFallback: (nextDraft, close) => (
      <ProjectCreateDialog
        creationError={creation.error}
        draft={nextDraft}
        isCreating={creation.isPending}
        onClose={close}
        onSubmitDraft={(values) => void submitDraft(values, close)}
      />
    ),
  });

  async function chooseWorkspace(): Promise<void> {
    try {
      const selected = await nativeBridge.directories.selectDirectory({ title: t("home.chooseWorkspace") });
      if (selected === null) {
        return;
      }
      await openProjectCreationDestination(selected.path);
    } catch (error) {
      push({
        id: "project-create-picker-error",
        tone: "danger",
        title: t("home.workspacePickerError"),
        body: errorMessage(error),
      });
    }
  }

  async function openProjectCreationDestination(workspacePath: string): Promise<void> {
    try {
      const plan = await api.planWorkspace(workspacePath);
      if (plan.binding !== null) {
        void navigation.openProject(plan.binding.projectID);
        return;
      }
      if (plan.kind !== LOCAL_UNBOUND_PLAN_KIND) {
        push({
          id: "project-create-selection-required",
          tone: "info",
          title: t("home.workspaceSelectionRequired"),
          body: t("home.workspaceSelectionRequiredBody"),
        });
        return;
      }
      const name = basename(plan.canonicalRoot);
      const nextDraft = { name, key: projectKeyFromName(name), workspaceRoot: plan.canonicalRoot };
      await projectCreationDialog.open(nextDraft);
    } catch (error) {
      push({
        id: "project-create-plan-error",
        tone: "danger",
        title: t("home.workspacePlanError"),
        body: errorMessage(error),
      });
    }
  }

  async function submitDraft(values: ProjectDraft, close: () => void): Promise<void> {
    try {
      const plan = await api.planWorkspace(values.workspaceRoot);
      if (plan.binding !== null) {
        close();
        void navigation.openProject(plan.binding.projectID);
        return;
      }
      if (plan.kind !== LOCAL_UNBOUND_PLAN_KIND) {
        close();
        push({
          id: "project-create-selection-required",
          tone: "info",
          title: t("home.workspaceSelectionRequired"),
          body: t("home.workspaceSelectionRequiredBody"),
        });
        return;
      }
      const binding = await creation.mutateAsync({
        name: values.name.trim(),
        key: values.key.trim().toUpperCase(),
        workspaceRoot: values.workspaceRoot,
      });
      close();
      void navigation.openProject(binding.projectID);
    } catch (error) {
      push({
        id: "project-create-submit-error",
        tone: "danger",
        title: t("home.workspacePlanError"),
        body: errorMessage(error),
      });
    }
  }

  const handleNativeProjectCreated = useCallback(
    (binding: Readonly<{ projectID: string }>) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.projects });
      void navigation.openProject(binding.projectID);
    },
    [navigation, queryClient],
  );

  useProjectCreationEvents(handleNativeProjectCreated);

  if (desktopChatEnabled) {
    const selectedProject =
      selectedProjectID === null
        ? null
        : (projectItems.find((project) => project.id === selectedProjectID) ?? null);
    const detailKey = selectedProject !== null ? `project:${selectedProject.id}` : "inbox";
    return (
      <div className="h-full min-h-0" data-testid="home-route-root">
        {projectCreationDialog.fallback}
        <div className="grid h-full min-h-0 grid-cols-[350px_minmax(0,1fr)]" data-testid="home-pane-grid">
          <HomePrototypeSidebar
            disabled={disabled}
            onChooseWorkspace={() => void chooseWorkspace()}
            onCreateWorkflow={() => {
              open({ kind: "workflowCreate", mode: sidebarMode });
            }}
            onProjectSelect={(projectID) => {
              setSelectedProjectID((current) => (current === projectID ? null : projectID));
            }}
            onCategorySelect={(category) => {
              setPrototypeCategory(category);
              setSelectedProjectID(null);
            }}
            selectedCategory={prototypeCategory}
            projectItems={projectItems}
            projectsQuery={projects}
            sidebarMode={sidebarMode}
            selectedProjectID={selectedProjectID}
          />
          <section className="island-glass my-[var(--space-2)] mr-[var(--space-2)] min-h-0 overflow-hidden rounded-[var(--radius-xl)]">
            <OverlappingCrossfade contentKey={detailKey}>
              {selectedProject !== null ? (
                <ProjectPrototypeDetail
                  key={selectedProject.id}
                  disabled={disabled}
                  onLinkWorkflow={() => {
                    open({ kind: "linkWorkflow", mode: sidebarMode, projectID: selectedProject.id });
                  }}
                  project={selectedProject}
                  sidebarMode={sidebarMode}
                />
              ) : (
                <AttentionList items={attentionItems} query={attention} sidebarMode={sidebarMode} />
              )}
            </OverlappingCrossfade>
          </section>
        </div>
      </div>
    );
  }

  return (
    <div className="h-full min-h-0" data-testid="home-route-root">
      {projectCreationDialog.fallback}
      <div
        className="grid h-full min-h-0 grid-cols-[repeat(auto-fit,minmax(min(100%,360px),1fr))] gap-[var(--space-3)]"
        data-testid="home-pane-grid"
      >
        <section
          aria-labelledby="home-primary-pane-title"
          className="island-glass min-h-0 overflow-hidden rounded-[var(--radius-xl)]"
        >
          <HomePrimaryPane
            activeTab={primaryTab}
            disabled={disabled}
            onChooseWorkspace={() => void chooseWorkspace()}
            onCreateWorkflow={() => {
              open({ kind: "workflowCreate", mode: sidebarMode });
            }}
            onTabChange={setPrimaryTab}
            projectItems={projectItems}
            projectsQuery={projects}
            sidebarMode={sidebarMode}
          />
        </section>
        <section
          aria-labelledby="attention-title"
          className="island-glass min-h-0 overflow-hidden rounded-[var(--radius-xl)]"
        >
          <AttentionList items={attentionItems} query={attention} sidebarMode={sidebarMode} />
        </section>
      </div>
    </div>
  );
}

type AttentionListProps = Readonly<{
  items: readonly AttentionItem[];
  query: ReturnType<typeof useGlobalAttentionPages>;
  sidebarMode: SidebarMode;
}>;

function AttentionList({ items, query, sidebarMode }: AttentionListProps) {
  const { t } = useTranslation();
  const { open } = useOwnedSidebarRoots();
  if (query.isPending) {
    return <LoadingState appearanceDelayMs={0} fullPage={false} reveal={false} title={t("states.loading")} />;
  }
  if (query.isError) {
    return <ErrorState body={errorMessage(query.error)} reveal={false} title={t("states.error")} />;
  }
  return (
    <VirtualizedInfiniteList
      className={`h-full min-h-0 overflow-auto px-[var(--space-4)] hide-scrollbar contain-strict [-webkit-overflow-scrolling:touch] [&>*]:mx-auto [&>*]:w-full ${homeListCardListMaxWidthClassName}`}
      empty={<HomeInlineEmptyState body={t("home.noAttentionBody")} />}
      estimateSize={() => 144}
      getItemKey={(item) => item.id}
      hasNextPage={query.hasNextPage}
      header={
        // Mirror the projects-tab pill's internal top inset (1px border + p-1 + button py-2) and label
        // typography so the "Inbox" upper edge aligns precisely with the tab labels in the adjacent pane.
        <h2
          className="m-0 mt-[calc(1px+var(--space-1)+var(--space-2))] text-base font-bold"
          id="attention-title"
        >
          {t("home.attentionPane")}
        </h2>
      }
      isFetchingNextPage={query.isFetchingNextPage}
      items={items}
      loadingLabel={t("app.loadingMore")}
      onLoadMore={() => void query.fetchNextPage()}
      paddingEnd={16}
      paddingStart={16}
      renderItem={(item) => <AttentionRow item={item} openSidebar={open} sidebarMode={sidebarMode} />}
    />
  );
}

const AttentionRow = memo(function AttentionRow({
  item,
  openSidebar,
  sidebarMode,
}: Readonly<{
  item: AttentionItem;
  openSidebar: SidebarRootController["open"];
  sidebarMode: SidebarMode;
}>) {
  const { t } = useTranslation();
  const message =
    item.message ??
    (item.kind === "approval"
      ? t("app.attention.approvalFallback")
      : t("app.attention.interruptedCurrentNodeFallback"));
  return (
    <button
      className={cx(
        "grid w-full min-w-0 gap-[var(--space-2)] rounded-[var(--radius-l)] p-[var(--space-3)] text-left text-[var(--color-on-island)]",
        islandSurfaceClassName(1),
      )}
      data-testid="attention-row"
      onClick={() => {
        openSidebar({
          kind: "taskDetail",
          initialFocus: taskDetailInitialFocusFromAttentionItem(item),
          inboxNav: true,
          mode: sidebarMode,
          onMutated: undefined,
          taskID: item.taskID,
        });
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
}, attentionRowPropsEqual);

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
    previous.occurredAt === next.occurredAt
  );
}

function HomeInlineEmptyState({ body }: Readonly<{ body: string }>) {
  return (
    <div className="rounded-[var(--radius-l)] border border-dashed border-[var(--color-outline)] p-[var(--space-4)] text-[var(--color-muted)]">
      <p>{body}</p>
    </div>
  );
}
